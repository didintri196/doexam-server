package attempt

import (
	"testing"
)

func TestScoreItem(t *testing.T) {
	multiKey := `{"correct":["a","c"]}`
	short := `{"accepted":[{"value":"Jakarta","match":"case_insensitive"},{"value":"3,14","match":"numeric","tolerance":0.01}]}`
	tests := []struct {
		name          string
		typ           string
		key, scoring  string
		resp          string
		max, negative float64
		adj           *adjustment
		want          *float64
		wantCorrect   *bool
		wantMax       float64
	}{
		{"single correct", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o2"}`, 2, 0, nil, f(2), b(true), 2},
		{"single wrong", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o1"}`, 2, 0, nil, f(0), b(false), 2},
		{"single wrong negative", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o1"}`, 2, 0.5, nil, f(-0.5), b(false), 2},
		{"single blank no negative", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":null}`, 2, 0.5, nil, f(0), b(false), 2},
		{"no response", typeMCQSingle, `{"correct":"o2"}`, ``, ``, 2, 0.5, nil, f(0), b(false), 2},
		{"multi all", typeMCQMulti, multiKey, `{"method":"all_or_nothing"}`, `{"selected":["c","a"]}`, 4, 0, nil, f(4), b(true), 4},
		{"multi partial nothing in aon", typeMCQMulti, multiKey, `{"method":"all_or_nothing"}`, `{"selected":["a"]}`, 4, 0, nil, f(0), b(false), 4},
		{"multi partial half", typeMCQMulti, multiKey, `{"method":"partial"}`, `{"selected":["a"]}`, 4, 0, nil, f(2), b(false), 4},
		{"multi partial wrong cancels right", typeMCQMulti, multiKey, `{"method":"partial"}`, `{"selected":["a","b"]}`, 4, 0, nil, f(0), b(false), 4},
		{"multi duplicates ignored", typeMCQMulti, multiKey, `{"method":"partial"}`, `{"selected":["a","a","c"]}`, 4, 0, nil, f(4), b(true), 4},
		{"tf", typeTrueFalse, `{"value":false}`, ``, `{"value":false}`, 1, 0, nil, f(1), b(true), 1},
		{"tf wrong", typeTrueFalse, `{"value":false}`, ``, `{"value":true}`, 1, 0, nil, f(0), b(false), 1},
		{"short case insensitive", typeShortAnswer, short, ``, `{"text":"  jakarta "}`, 1, 0, nil, f(1), b(true), 1},
		{"short spaces collapsed", typeShortAnswer, `{"accepted":[{"value":"Ki Hajar Dewantara","match":"case_insensitive"}]}`, ``, `{"text":"ki  hajar dewantara"}`, 1, 0, nil, f(1), b(true), 1},
		{"short numeric dot", typeShortAnswer, short, ``, `{"text":"3.141"}`, 1, 0, nil, f(1), b(true), 1},
		{"short numeric outside tolerance", typeShortAnswer, short, ``, `{"text":"3.2"}`, 1, 0, nil, f(0), b(false), 1},
		{"short exact", typeShortAnswer, `{"accepted":[{"value":"H2O","match":"exact"}]}`, ``, `{"text":"h2o"}`, 1, 0, nil, f(0), b(false), 1},
		{"essay pending", typeEssay, ``, ``, `{"text":"Fotosintesis adalah..."}`, 10, 0, nil, nil, nil, 10},
		{"essay blank", typeEssay, ``, ``, `{"text":"   "}`, 10, 0, nil, f(0), nil, 10},
		{"annul full credit", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o1"}`, 2, 0, &adjustment{Action: adjFullCredit}, f(2), nil, 2},
		{"annul remove", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o2"}`, 2, 0, &adjustment{Action: adjRemove}, f(0), nil, 0},
		{"override key", typeMCQSingle, `{"correct":"o2"}`, ``, `{"selected":"o1"}`, 2, 0, &adjustment{Action: adjOverrideKey, AnswerKey: []byte(`{"correct":"o1"}`)}, f(2), b(true), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreItem(tt.typ, []byte(tt.key), []byte(tt.scoring), []byte(tt.resp), tt.max, tt.negative, tt.adj)
			if (got.Score == nil) != (tt.want == nil) || (got.Score != nil && *got.Score != *tt.want) {
				t.Fatalf("score = %v, want %v", ptrVal(got.Score), ptrVal(tt.want))
			}
			if (got.IsCorrect == nil) != (tt.wantCorrect == nil) || (got.IsCorrect != nil && *got.IsCorrect != *tt.wantCorrect) {
				t.Fatalf("is_correct = %v, want %v", got.IsCorrect, tt.wantCorrect)
			}
			if got.MaxPoints != tt.wantMax {
				t.Fatalf("max = %v, want %v", got.MaxPoints, tt.wantMax)
			}
		})
	}
}

func TestParseNumber(t *testing.T) {
	for in, want := range map[string]float64{"3,14": 3.14, "3.14": 3.14, "1.000,5": 1000.5, "1,000.5": 1000.5, " -2 ": -2} {
		got, err := parseNumber(in)
		if err != nil || got != want {
			t.Errorf("parseNumber(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseNumber("abc"); err == nil {
		t.Error("abc must fail")
	}
}

func ptrVal(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
