package report

import (
	"fmt"
	"math"
	"testing"
	"time"

	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
)

// Reference values were computed independently with Python's statistics module.
var matrix = [][]float64{{1, 1, 1, 0}, {1, 1, 0, 0}, {1, 0, 0, 0}, {1, 1, 1, 1}, {0, 0, 0, 0}, {1, 1, 1, 0}}

func fixture() ([]AnalysisAttempt, []AnalysisItem) {
	mcq := []byte(`{"stem":"q1","options":[{"id":"o1","body":"a"},{"id":"o2","body":"b"},{"id":"o3","body":"c"}]}`)
	choice := []string{"o1", "o1", "o1", "o1", "o2", "o1"}
	var attempts []AnalysisAttempt
	var items []AnalysisItem
	for i, row := range matrix {
		raw := row[0] + row[1] + row[2] + row[3]
		passed := raw >= 3
		a := AnalysisAttempt{ID: fmt.Sprintf("a%d", i), RawScore: raw, MaxScore: 4, FinalScore: raw / 4 * 100, Passed: &passed}
		attempts = append(attempts, a)
		for j, sc := range row {
			sc := sc
			it := AnalysisItem{AttemptID: a.ID, ItemVersionID: fmt.Sprintf("v%d", j), Type: "true_false", Score: &sc, MaxPoints: 1,
				Response: []byte(`{"value":true}`)}
			if j == 0 {
				it.Type, it.Content, it.AnswerKey = "mcq_single", mcq, []byte(`{"correct":"o1"}`)
				it.Response = []byte(`{"selected":"` + choice[i] + `"}`)
			}
			if j == 3 && sc == 0 && i == 4 {
				it.Response = nil // omitted
			}
			items = append(items, it)
		}
	}
	return attempts, items
}

func approx(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-4 }

func TestAnalyze(t *testing.T) {
	attempts, items := fixture()
	pass := 70.0
	exam, stats, warnings := analyze(attempts, items, &pass)

	if exam.NAttempts != 6 || exam.CommonItems != 4 || !approx(exam.Reliability, 0.7795) {
		t.Fatalf("exam = %+v", exam)
	}
	if !approx(exam.Mean, 54.17) || !approx(exam.SD, 36.8) || !approx(exam.Median, 62.5) || !approx(exam.PassRate, 0.5) {
		t.Fatalf("exam summary = mean %v sd %v median %v pass %v", *exam.Mean, *exam.SD, *exam.Median, *exam.PassRate)
	}
	if exam.Distribution[0] != 1 || exam.Distribution[9] != 1 {
		t.Fatalf("distribution = %v", exam.Distribution)
	}
	if len(warnings) == 0 {
		t.Fatal("6 attempts must warn about the small sample")
	}
	want := map[string][2]float64{"v0": {0.8333, 0.5394}, "v1": {0.6667, 0.7385}, "v2": {0.5, 0.7071}, "v3": {0.1667, 0.3873}}
	for _, st := range stats {
		w := want[st.ItemVersionID]
		if !approx(st.PValue, w[0]) || !approx(st.Discrimination, w[1]) {
			t.Errorf("%s: p=%v disc=%v, want %v", st.ItemVersionID, *st.PValue, *st.Discrimination, w)
		}
	}
	v0 := stats[0]
	if v0.Options["o1"].N != 5 || !v0.Options["o1"].IsKey || !approx(v0.Options["o2"].Pbis, -0.5394) || v0.Options["o3"].Pbis != nil {
		t.Fatalf("options = %+v", v0.Options)
	}
	if stats[3].Omitted != 1 {
		t.Fatalf("omitted = %d", stats[3].Omitted)
	}
}

func TestAnalyzeBlueprintReliability(t *testing.T) {
	attempts, items := fixture()
	// drop v3 for one attempt: only v0..v2 are common to everyone
	var kept []AnalysisItem
	for _, it := range items {
		if !(it.AttemptID == "a0" && it.ItemVersionID == "v3") {
			kept = append(kept, it)
		}
	}
	exam, _, _ := analyze(attempts, kept, nil)
	if exam.CommonItems != 3 || exam.Reliability == nil || exam.PassRate != nil {
		t.Fatalf("exam = %+v", exam)
	}
}

func TestInterpret(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	tests := []struct {
		p, d          float64
		level, dl, rc string
	}{
		{0.85, 0.45, "easy", "excellent", "keep"},
		{0.5, 0.32, "medium", "good", "keep"},
		{0.2, 0.15, "hard", "poor", "revise"},
		{0.6, -0.3, "medium", "poor", "check_key"},
		{0.98, 0.25, "easy", "fair", "revise"},
	}
	for _, tt := range tests {
		st := modelReport.ItemStat{PValue: f(tt.p), Discrimination: f(tt.d), Options: map[string]modelReport.OptionStat{
			"o1": {IsKey: true, Pct: 0.6}, "o2": {Pct: 0.02}, "o3": {Pct: 0.3, Pbis: f(0.2)}, "o4": {Pct: 0.08, Pbis: f(-0.3)},
		}}
		interpret(&st)
		if st.DifficultyLevel != tt.level || st.DiscriminationLvl != tt.dl || st.Recommendation != tt.rc {
			t.Errorf("p=%v d=%v → %s/%s/%s", tt.p, tt.d, st.DifficultyLevel, st.DiscriminationLvl, st.Recommendation)
		}
		if st.Options["o2"].Flag != "non_functional" || st.Options["o3"].Flag != "attracts_high_scorers" || st.Options["o4"].Flag != "" || st.Options["o1"].Flag != "" {
			t.Errorf("flags = %+v", st.Options)
		}
	}
}

func TestBuildRecap(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	s := func(x string) *string { return &x }
	now := time.Now()
	pass := 75.0
	e := modelExam.ExamEntity{ID: "e", Title: "UH", AttemptScoring: modelExam.ScoringHighest, PassingScore: &pass}
	rows := []RecapAttempt{
		{UserID: "u1", FullName: "Ani", SessionName: "S1", AttemptID: s("a1"), Status: s("graded"), FinalScore: f(60), SubmittedAt: &now},
		{UserID: "u1", FullName: "Ani", SessionName: "S2", AttemptID: s("a2"), Status: s("graded"), FinalScore: f(80), SubmittedAt: &now},
		{UserID: "u2", FullName: "Budi", SessionName: "S1"},
		{UserID: "u3", FullName: "Citra", SessionName: "S1", AttemptID: s("a3"), Status: s("submitted"), FinalScore: f(40)},
		{UserID: "u4", FullName: "Dodi", SessionName: "S1", AttemptID: s("a4"), Status: s("in_progress")},
	}
	r := buildRecap(e, rows)
	if len(r.Rows) != 4 {
		t.Fatalf("rows = %d", len(r.Rows))
	}
	ani := r.Rows[0]
	if *ani.Score != 80 || !*ani.Passed || ani.Attempts != 2 || ani.SessionName != "S2" || ani.Status != "graded" {
		t.Fatalf("ani = %+v", ani)
	}
	if r.Rows[1].Status != "not_started" || r.Rows[2].Status != "pending_grading" || r.Rows[2].Passed != nil || r.Rows[3].Status != "in_progress" {
		t.Fatalf("statuses = %v %v %v", r.Rows[1].Status, r.Rows[2].Status, r.Rows[3].Status)
	}
	if countedScore([]float64{60, 80}, modelExam.ScoringLatest) != 80 || countedScore([]float64{60, 81}, modelExam.ScoringAverage) != 70.5 {
		t.Fatal("attempt scoring")
	}
	headers, table := RecapTable(r)
	if len(headers) != 10 || table[0][7] != "80,00" || table[0][8] != "ya" {
		t.Fatalf("table = %v", table[0])
	}
}

func TestAttemptReportCompetencies(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	s := func(x string) *string { return &x }
	items := []ReportItem{
		{Position: 1, Score: f(1), MaxPoints: 1, CompetencyID: s("c1"), CompetencyName: s("Pecahan")},
		{Position: 2, Score: f(0), MaxPoints: 1, CompetencyID: s("c1"), CompetencyName: s("Pecahan")},
		{Position: 3, Score: f(-0.5), MaxPoints: 2, CompetencyID: s("c2"), CompetencyName: s("Desimal")},
		{Position: 4, Score: nil, MaxPoints: 5, CompetencyID: s("c2")}, // pending essay: not counted yet
	}
	r := buildAttemptReport(ReportAttempt{ConfigSnapshot: []byte(`{"exam_title":"UH"}`)}, nil, items, map[string]int{})
	if len(r.Competencies) != 2 || r.Competencies[0].Percent != 50 || r.Competencies[1].Score != 0 || r.Competencies[1].MaxScore != 2 {
		t.Fatalf("competencies = %+v", r.Competencies)
	}
	if r.ExamTitle != "UH" || len(r.Items) != 4 || string(r.Items[3].Response) != "null" {
		t.Fatalf("report = %+v", r)
	}
}
