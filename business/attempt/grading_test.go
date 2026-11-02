package attempt

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	modelAttempt "doexam/models/attempt"
	"doexam/pkg/errr"
)

func essayRow(maxPoints float64, rubric string) ItemRow {
	return ItemRow{
		AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i", MaxPoints: maxPoints, Response: []byte(`{"text":"jawaban"}`)},
		Type:              typeEssay,
		Scoring:           []byte(rubric),
	}
}

func TestGradeScore(t *testing.T) {
	rubric := `{"rubric":[{"criterion":"Konsep","max":3},{"criterion":"Bahasa","max":2}]}`
	tests := []struct {
		name    string
		item    ItemRow
		in      modelAttempt.GradeInput
		want    float64
		wantErr string
	}{
		{"plain score", essayRow(4, `{}`), modelAttempt.GradeInput{Score: f(3.456)}, 3.46, ""},
		{"plain over max", essayRow(4, `{}`), modelAttempt.GradeInput{Score: f(5)}, 0, "score"},
		{"negative", essayRow(4, `{}`), modelAttempt.GradeInput{Score: f(-1)}, 0, "score"},
		{"nothing given", essayRow(4, `{}`), modelAttempt.GradeInput{}, 0, "score"},
		{"rubric equal to points", essayRow(5, rubric), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 3}, {Criterion: "Bahasa", Score: 1}}}, 4, ""},
		{"rubric scaled to points", essayRow(10, rubric), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 3}, {Criterion: "Bahasa", Score: 1}}}, 8, ""},
		{"rubric missing criterion", essayRow(5, rubric), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 3}}}, 0, "missing criterion"},
		{"rubric unknown criterion", essayRow(5, rubric), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 3}, {Criterion: "Bahasa", Score: 1}, {Criterion: "Lain", Score: 1}}}, 0, "unknown criterion"},
		{"rubric over criterion max", essayRow(5, rubric), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 4}, {Criterion: "Bahasa", Score: 1}}}, 0, "Konsep: 0-3"},
		{"rubric on item without rubric", essayRow(5, `{}`), modelAttempt.GradeInput{RubricScores: []modelAttempt.RubricScore{{Criterion: "Konsep", Score: 1}}}, 0, "no rubric"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stored, err := gradeScore(tt.in, tt.item)
			if tt.wantErr != "" {
				var ve *errr.ValidationError
				if !errors.As(err, &ve) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("score = %v, %v; want %v", got, err, tt.want)
			}
			if len(tt.in.RubricScores) > 0 && !json.Valid(stored) {
				t.Fatal("rubric scores must be stored")
			}
		})
	}
}

func TestValidateKey(t *testing.T) {
	mcq := []byte(`{"stem":"x","options":[{"id":"o1","body":"a"},{"id":"o2","body":"b"}]}`)
	tests := []struct {
		typ, key, want string
		ok             bool
	}{
		{typeMCQSingle, `{"correct":"o1"}`, `{"correct":"o1"}`, true},
		{typeMCQSingle, `{"correct":"o9"}`, "", false},
		{typeMCQMulti, `{"correct":["o2","o1","o2"]}`, `{"correct":["o2","o1"]}`, true},
		{typeMCQMulti, `{"correct":["o3"]}`, "", false},
		{typeTrueFalse, `{"value":false}`, `{"value":false}`, true},
		{typeTrueFalse, `{}`, "", false},
		{typeShortAnswer, `{"accepted":[{"value":"2,5","match":"numeric"}]}`, "", true},
		{typeShortAnswer, `{"accepted":[{"value":"abc","match":"numeric"}]}`, "", false},
		{typeEssay, `{"model_answer":"x"}`, "", false},
	}
	for _, tt := range tests {
		got, bad := validateKey(tt.typ, mcq, json.RawMessage(tt.key))
		if (bad == "") != tt.ok {
			t.Errorf("%s %s: bad = %q, want ok=%v", tt.typ, tt.key, bad, tt.ok)
		}
		if tt.want != "" && string(got) != tt.want {
			t.Errorf("%s %s: key = %s, want %s", tt.typ, tt.key, got, tt.want)
		}
	}
}

func TestScoreWithManual(t *testing.T) {
	it := essayRow(4, `{}`)
	if r := scoreWithManual(it, 0, nil, nil); r.Score != nil {
		t.Fatal("ungraded essay must stay pending")
	}
	if r := scoreWithManual(it, 0, nil, f(3)); r.Score == nil || *r.Score != 3 || r.Source != modelAttempt.SourceManual {
		t.Fatalf("manual grade must count: %+v", r)
	}
	if r := scoreWithManual(it, 0, &adjustment{Action: adjFullCredit}, f(1)); *r.Score != 4 || r.Source != modelAttempt.SourceAdjustment {
		t.Fatalf("annulment beats the manual grade: %+v", r)
	}
	if r := scoreWithManual(it, 0, &adjustment{Action: adjRemove}, f(1)); *r.Score != 0 || r.MaxPoints != 0 {
		t.Fatalf("annul_remove drops the item from the maximum: %+v", r)
	}
}

func TestSameScore(t *testing.T) {
	src := modelAttempt.SourceAuto
	e := modelAttempt.AttemptItemEntity{Score: f(1), IsCorrect: b(true), ScoreSource: &src}
	if !sameScore(e, itemScore{Score: f(1), IsCorrect: b(true), Source: src}) {
		t.Fatal("identical score must be detected")
	}
	if sameScore(e, itemScore{Score: f(1), IsCorrect: b(true), Source: modelAttempt.SourceAdjustment}) {
		t.Fatal("source change must be saved")
	}
	if sameScore(e, itemScore{Score: nil}) {
		t.Fatal("becoming pending must be saved")
	}
}
