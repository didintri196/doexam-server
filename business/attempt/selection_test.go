package attempt

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func pool(prefix string, n int) []candidate {
	out := make([]candidate, n)
	for i := range out {
		id := fmt.Sprintf("%s%d", prefix, i)
		out[i] = candidate{ItemID: id, VersionID: "v-" + id, Type: typeTrueFalse, DefaultPoints: 1}
	}
	return out
}

func testSections() []sectionSpec {
	shared := pool("x", 6) // x4 is also hand-picked, so r1+r2 draw 5 of x0..x3,x5
	return []sectionSpec{
		{ID: "s2", SortOrder: 2, Random: true, Rules: []ruleSpec{
			{ID: "r1", Count: 3, Points: f(2), Pool: shared[:3]}, // only x0..x2
			{ID: "r2", Count: 2, Pool: shared},                   // overlaps r1 entirely
		}},
		{ID: "s1", SortOrder: 1, Fixed: []candidate{
			{ItemID: "a", VersionID: "v-a", Type: typeMCQSingle, OptionIDs: []string{"o1", "o2", "o3", "o4"}, DefaultPoints: 1, Points: f(5)},
			{ItemID: "b", VersionID: "v-b", Type: typeMCQSingle, OptionIDs: []string{"o1", "o2", "o3"}, DefaultPoints: 1, LockOptions: true},
			{ItemID: "x4", VersionID: "v-x4", Type: typeTrueFalse, DefaultPoints: 1}, // hand-picked, so r2 cannot use it
		}},
	}
}

func TestBuildPlanDeterministicAndValid(t *testing.T) {
	plan, err := buildPlan(testSections(), 42, true, true)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := buildPlan(testSections(), 42, true, true)
	if !reflect.DeepEqual(plan, again) {
		t.Fatal("same seed must give the same plan")
	}
	if plan[0].SectionID != "s1" || plan[1].SectionID != "s2" {
		t.Fatalf("sections must follow sort order: %v, %v", plan[0].SectionID, plan[1].SectionID)
	}
	seen := map[string]bool{}
	for _, s := range plan {
		for _, it := range s.Items {
			if seen[it.VersionID] {
				t.Fatalf("item %s drawn twice", it.VersionID)
			}
			seen[it.VersionID] = true
		}
	}
	if len(plan[1].Items) != 5 || seen["v-x4"] && countIn(plan[1], "v-x4") > 0 {
		t.Fatalf("random section = %+v", plan[1].Items)
	}
	r1 := 0
	for _, it := range plan[1].Items {
		if it.Points == 2 {
			r1++
		}
	}
	if r1 != 3 {
		t.Fatalf("rule 1 must contribute 3 items with points 2, got %d", r1)
	}
	for _, it := range plan[0].Items {
		switch it.VersionID {
		case "v-a":
			if it.Points != 5 || len(it.OptionOrder) != 4 {
				t.Fatalf("a = %+v", it)
			}
		case "v-b":
			if !slices.Equal(it.OptionOrder, []string{"o1", "o2", "o3"}) {
				t.Fatalf("lock_options must keep the author order: %v", it.OptionOrder)
			}
		case "v-x4":
			if it.OptionOrder != nil {
				t.Fatal("non-mcq has no option order")
			}
		}
	}
}

func TestBuildPlanVariesWithSeed(t *testing.T) {
	differs := false
	base, _ := buildPlan(testSections(), 1, true, true)
	for seed := int64(2); seed < 30 && !differs; seed++ {
		p, err := buildPlan(testSections(), seed, true, true)
		if err != nil {
			t.Fatal(err)
		}
		differs = !reflect.DeepEqual(base, p)
	}
	if !differs {
		t.Fatal("different seeds should give different plans")
	}
}

func TestBuildPlanWithoutShuffle(t *testing.T) {
	plan, _ := buildPlan(testSections(), 7, false, false)
	got := []string{}
	for _, it := range plan[0].Items {
		got = append(got, it.VersionID)
	}
	if !slices.Equal(got, []string{"v-a", "v-b", "v-x4"}) {
		t.Fatalf("fixed order = %v", got)
	}
	if !slices.Equal(plan[0].Items[0].OptionOrder, []string{"o1", "o2", "o3", "o4"}) {
		t.Fatal("options must keep order when shuffle_options is off")
	}
}

func TestBuildPlanInfeasible(t *testing.T) {
	s := []sectionSpec{{ID: "s", Random: true, Rules: []ruleSpec{{ID: "r", Count: 3, Pool: pool("y", 2)}}}}
	if _, err := buildPlan(s, 1, true, true); err == nil {
		t.Fatal("an unfillable rule must fail")
	}
}

func countIn(s plannedSection, v string) int {
	n := 0
	for _, it := range s.Items {
		if it.VersionID == v {
			n++
		}
	}
	return n
}
