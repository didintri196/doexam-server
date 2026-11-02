package attempt

import (
	"fmt"
	"math/rand/v2"
	"slices"
)

// Specs describe the published exam structure the selector draws from.
type (
	sectionSpec struct {
		ID        string
		SortOrder int
		Random    bool
		Fixed     []candidate // hand-picked, in author order
		Rules     []ruleSpec
	}
	ruleSpec struct {
		ID     string
		Count  int
		Points *float64 // nil = item default
		Pool   []candidate
	}
	candidate struct {
		ItemID        string
		VersionID     string
		Type          string
		OptionIDs     []string // mcq only, author order
		DefaultPoints float64
		Points        *float64 // fixed items: override
		LockOptions   bool
	}
)

// Plan is the item list of one attempt.
type (
	plannedSection struct {
		SectionID string
		SortOrder int
		Items     []plannedItem
	}
	plannedItem struct {
		VersionID   string
		Points      float64
		OptionOrder []string // nil for non-mcq
	}
)

// buildPlan picks and orders the items of an attempt deterministically from seed:
// the same seed always gives the same plan, so an attempt can be audited and reproduced.
// Blueprint rules draw distinct items exam-wide via randomized bipartite matching,
// so overlapping rules never repeat an item (publish already proved it is feasible).
func buildPlan(sections []sectionSpec, seed int64, shuffleItems, shuffleOptions bool) ([]plannedSection, error) {
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	sections = slices.Clone(sections)
	slices.SortFunc(sections, func(a, b sectionSpec) int { return a.SortOrder - b.SortOrder })

	used := map[string]bool{}
	for _, s := range sections {
		for _, c := range s.Fixed {
			used[c.ItemID] = true
		}
	}
	drawn, err := drawRules(sections, used, rng)
	if err != nil {
		return nil, err
	}

	out := make([]plannedSection, 0, len(sections))
	for _, s := range sections {
		var picks []candidate
		var points []float64
		if s.Random {
			for _, r := range s.Rules {
				for _, c := range drawn[r.ID] {
					picks = append(picks, c)
					points = append(points, pick(r.Points, c.DefaultPoints))
				}
			}
		} else {
			for _, c := range s.Fixed {
				picks = append(picks, c)
				points = append(points, pick(c.Points, c.DefaultPoints))
			}
		}
		idx := make([]int, len(picks))
		for i := range idx {
			idx[i] = i
		}
		if shuffleItems {
			rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
		}
		ps := plannedSection{SectionID: s.ID, SortOrder: s.SortOrder, Items: make([]plannedItem, 0, len(picks))}
		for _, i := range idx {
			c := picks[i]
			var order []string
			if len(c.OptionIDs) > 0 {
				order = slices.Clone(c.OptionIDs)
				if shuffleOptions && !c.LockOptions {
					rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
				}
			}
			ps.Items = append(ps.Items, plannedItem{VersionID: c.VersionID, Points: points[i], OptionOrder: order})
		}
		out = append(out, ps)
	}
	return out, nil
}

// drawRules assigns distinct items to every rule slot of every random section.
func drawRules(sections []sectionSpec, used map[string]bool, rng *rand.Rand) (map[string][]candidate, error) {
	type slot struct{ rule *ruleSpec }
	var slots []slot
	pools := map[string][]candidate{}
	for si := range sections {
		if !sections[si].Random {
			continue
		}
		for ri := range sections[si].Rules {
			r := &sections[si].Rules[ri]
			pool := slices.DeleteFunc(slices.Clone(r.Pool), func(c candidate) bool { return used[c.ItemID] })
			rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
			pools[r.ID] = pool
			for range r.Count {
				slots = append(slots, slot{rule: r})
			}
		}
	}
	rng.Shuffle(len(slots), func(i, j int) { slots[i], slots[j] = slots[j], slots[i] })

	owner := map[string]int{} // item id → slot
	byItem := map[string]candidate{}
	var try func(si int, seen map[string]bool) bool
	try = func(si int, seen map[string]bool) bool {
		for _, c := range pools[slots[si].rule.ID] {
			if seen[c.ItemID] {
				continue
			}
			seen[c.ItemID] = true
			if cur, taken := owner[c.ItemID]; !taken || try(cur, seen) {
				owner[c.ItemID] = si
				byItem[c.ItemID] = c
				return true
			}
		}
		return false
	}
	for si := range slots {
		if !try(si, map[string]bool{}) {
			return nil, fmt.Errorf("blueprint rule %s cannot be filled from its pool", slots[si].rule.ID)
		}
	}
	out := map[string][]candidate{}
	// iterate pools (shuffled order) for a stable, seed-dependent order inside each rule
	for _, s := range sections {
		for _, r := range s.Rules {
			for _, c := range pools[r.ID] {
				if si, ok := owner[c.ItemID]; ok && slots[si].rule.ID == r.ID {
					out[r.ID] = append(out[r.ID], byItem[c.ItemID])
				}
			}
		}
	}
	return out, nil
}

func pick(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}
