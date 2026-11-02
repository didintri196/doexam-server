package report

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"unicode/utf8"

	modelReport "doexam/models/report"
	"doexam/pkg/psychometrics"
)

// Interpretation thresholds commonly used for classroom tests.
const (
	easyAbove        = 0.70 // p > 0.70 easy, 0.30-0.70 medium, < 0.30 hard
	hardBelow        = 0.30
	minFunctionalPct = 0.05 // a distractor chosen by < 5% does not work
	smallSample      = 30   // below this the statistics are unstable
)

// analyze computes classical item and test statistics from each participant's first graded
// attempt. Discrimination is the corrected item-rest correlation, using the rest score as a
// percentage so it also works when participants received different items (blueprints).
// Reliability (alpha / KR-20) uses only items every participant received.
func analyze(attempts []AnalysisAttempt, items []AnalysisItem, passingScore *float64) (modelReport.ExamStat, []modelReport.ItemStat, []string) {
	var warnings []string
	byAttempt := map[string]AnalysisAttempt{}
	finals := make([]float64, 0, len(attempts))
	passed := 0
	for _, a := range attempts {
		byAttempt[a.ID] = a
		finals = append(finals, a.FinalScore)
		if a.Passed != nil && *a.Passed {
			passed++
		}
	}
	exam := modelReport.ExamStat{NAttempts: len(attempts), Distribution: psychometrics.Histogram(finals, 0, 100, 10)}
	if len(attempts) > 0 {
		d := psychometrics.Describe(finals)
		exam.Mean, exam.SD, exam.Min, exam.Max, exam.Median = r4(d.Mean, 2), r4(d.SD, 2), r4(d.Min, 2), r4(d.Max, 2), r4(d.Median, 2)
		if passingScore != nil {
			exam.PassRate = r4(float64(passed)/float64(len(attempts)), 4)
		}
	}
	if len(attempts) < smallSample {
		warnings = append(warnings, fmt.Sprintf("small_sample: %d graded attempts; statistics are unstable below %d", len(attempts), smallSample))
	}

	type obs struct {
		frac, score, rest float64
		response          []byte
	}
	byVersion := map[string][]obs{}
	meta := map[string]AnalysisItem{}
	scoreOf := map[string]map[string]float64{} // attempt → version → points (for alpha)
	for _, it := range items {
		a, ok := byAttempt[it.AttemptID]
		if !ok || it.Score == nil {
			continue
		}
		meta[it.ItemVersionID] = it
		if scoreOf[it.AttemptID] == nil {
			scoreOf[it.AttemptID] = map[string]float64{}
		}
		scoreOf[it.AttemptID][it.ItemVersionID] = *it.Score
		restMax := a.MaxScore - it.MaxPoints
		if it.MaxPoints <= 0 || restMax <= 0 {
			continue // annulled-and-removed item, or the only item: no meaningful rest score
		}
		byVersion[it.ItemVersionID] = append(byVersion[it.ItemVersionID], obs{
			frac:     math.Max(0, *it.Score) / it.MaxPoints,
			score:    *it.Score,
			rest:     (a.RawScore - *it.Score) / restMax * 100,
			response: it.Response,
		})
	}

	stats := make([]modelReport.ItemStat, 0, len(byVersion))
	for vid, os := range byVersion {
		m := meta[vid]
		st := modelReport.ItemStat{ItemVersionID: vid, ItemID: m.ItemID, Code: m.Code, Type: m.Type, StemPreview: preview(m.SearchText), N: len(os)}
		fracs, rests := make([]float64, len(os)), make([]float64, len(os))
		for i, o := range os {
			fracs[i], rests[i] = o.frac, o.rest
			if len(o.response) == 0 {
				st.Omitted++
			}
		}
		st.PValue = r4(psychometrics.Mean(fracs), 4)
		if r, ok := psychometrics.Pearson(fracs, rests); ok {
			st.Discrimination = r4(r, 4)
		}
		if ids := optionIDs(m.Content); len(ids) > 0 {
			key := keyIDs(m.AnswerKey)
			st.Options = map[string]modelReport.OptionStat{}
			for _, id := range ids {
				chosen := make([]float64, len(os))
				n := 0
				for i, o := range os {
					if slices.Contains(selected(o.response), id) {
						chosen[i], n = 1, n+1
					}
				}
				opt := modelReport.OptionStat{N: n, Pct: *r4(float64(n)/float64(len(os)), 4), IsKey: slices.Contains(key, id)}
				if r, ok := psychometrics.Pearson(chosen, rests); ok {
					opt.Pbis = r4(r, 4)
				}
				st.Options[id] = opt
			}
		}
		stats = append(stats, st)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].ItemVersionID < stats[j].ItemVersionID })

	// reliability over the items every analysed attempt received
	var common []string
	for vid := range meta {
		all := true
		for _, a := range attempts {
			if _, ok := scoreOf[a.ID][vid]; !ok {
				all = false
				break
			}
		}
		if all {
			common = append(common, vid)
		}
	}
	sort.Strings(common)
	exam.CommonItems = len(common)
	if len(common) >= 2 {
		matrix := make([][]float64, 0, len(attempts))
		for _, a := range attempts {
			row := make([]float64, len(common))
			for j, vid := range common {
				row[j] = scoreOf[a.ID][vid]
			}
			matrix = append(matrix, row)
		}
		if alpha, ok := psychometrics.Alpha(matrix); ok {
			exam.Reliability = r4(alpha, 4)
		}
	} else if len(attempts) > 0 {
		warnings = append(warnings, "reliability: fewer than 2 items were given to every participant")
	}
	return exam, stats, warnings
}

// interpret adds the human-readable levels, the recommendation and distractor flags.
func interpret(st *modelReport.ItemStat) {
	if st.PValue != nil {
		switch p := *st.PValue; {
		case p > easyAbove:
			st.DifficultyLevel = "easy"
		case p >= hardBelow:
			st.DifficultyLevel = "medium"
		default:
			st.DifficultyLevel = "hard"
		}
	}
	st.Recommendation = "keep"
	if st.Discrimination != nil {
		switch d := *st.Discrimination; {
		case d >= 0.4:
			st.DiscriminationLvl = "excellent"
		case d >= 0.3:
			st.DiscriminationLvl = "good"
		case d >= 0.2:
			st.DiscriminationLvl = "fair"
		default:
			st.DiscriminationLvl = "poor"
		}
		switch {
		case *st.Discrimination < 0:
			st.Recommendation = "check_key" // stronger participants do worse: wrong key or flawed item
		case *st.Discrimination < 0.2:
			st.Recommendation = "revise"
		}
	}
	if st.PValue != nil && (*st.PValue < 0.1 || *st.PValue > 0.95) && st.Recommendation == "keep" {
		st.Recommendation = "revise"
	}
	for id, o := range st.Options {
		if o.IsKey {
			continue
		}
		switch {
		case o.Pct < minFunctionalPct:
			o.Flag = "non_functional"
		case o.Pbis != nil && *o.Pbis > 0:
			o.Flag = "attracts_high_scorers"
		}
		st.Options[id] = o
	}
}

func optionIDs(content []byte) []string {
	var c struct {
		Options []struct {
			ID string `json:"id"`
		} `json:"options"`
	}
	_ = json.Unmarshal(content, &c)
	var ids []string
	for _, o := range c.Options {
		ids = append(ids, o.ID)
	}
	return ids
}

// keyIDs returns the correct option ids of an mcq key ("o2" or ["a","c"]).
func keyIDs(key []byte) []string {
	var k struct {
		Correct json.RawMessage `json:"correct"`
	}
	_ = json.Unmarshal(key, &k)
	var one string
	if json.Unmarshal(k.Correct, &one) == nil && one != "" {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(k.Correct, &many)
	return many
}

// selected returns the option ids in a response ("o2" or ["a","c"]).
func selected(response []byte) []string {
	if len(response) == 0 {
		return nil
	}
	return keyIDs([]byte(`{"correct":` + string(selectedRaw(response)) + `}`))
}

func selectedRaw(response []byte) json.RawMessage {
	var r struct {
		Selected json.RawMessage `json:"selected"`
	}
	if json.Unmarshal(response, &r) != nil || len(r.Selected) == 0 {
		return json.RawMessage("null")
	}
	return r.Selected
}

func r4(x float64, places int) *float64 {
	p := math.Pow(10, float64(places))
	v := math.Round(x*p) / p
	return &v
}

func preview(s *string) string {
	if s == nil {
		return ""
	}
	if utf8.RuneCountInString(*s) <= 160 {
		return *s
	}
	return string([]rune(*s)[:160]) + "…"
}
