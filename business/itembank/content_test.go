package itembank

import (
	"encoding/json"
	"strings"
	"testing"

	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestNormalizeBodyValid(t *testing.T) {
	tests := []struct {
		name                    string
		typ                     modelBank.ItemType
		content, key, scoring   string
		wantContent, wantKey    string
		wantScoring, wantInText string
	}{
		{name: "mcq single sanitizes html", typ: modelBank.TypeMCQSingle,
			content:     `{"stem":"<p onclick=\"x()\">Ibu kota <b>RI</b>?<script>alert(1)</script></p>","options":[{"id":"o1","body":"Jakarta"},{"id":"o2","body":"Bandung"}]}`,
			key:         `{"correct":"o1"}`,
			wantContent: `{"stem":"<p>Ibu kota RI?</p>","options":[{"id":"o1","body":"Jakarta"},{"id":"o2","body":"Bandung"}]}`,
			wantKey:     `{"correct":"o1"}`, wantScoring: `{}`, wantInText: "Ibu kota RI? Jakarta Bandung"},
		{name: "mcq multi default scoring", typ: modelBank.TypeMCQMulti,
			content: `{"stem":"Bilangan prima?","options":[{"id":"a","body":"2"},{"id":"b","body":"4"},{"id":"c","body":"5"}]}`,
			key:     `{"correct":["a","c"]}`,
			wantKey: `{"correct":["a","c"]}`, wantScoring: `{"method":"all_or_nothing"}`},
		{name: "true false drops stray fields", typ: modelBank.TypeTrueFalse,
			content: `{"stem":"Air mendidih 100°C","options":[{"id":"o1","body":"x"}]}`, key: `{"value":true}`,
			wantContent: `{"stem":"Air mendidih 100°C"}`, wantKey: `{"value":true}`},
		{name: "short answer number defaults to numeric match", typ: modelBank.TypeShortAnswer,
			content: `{"stem":"Nilai pi?","input":"number"}`, key: `{"accepted":[{"value":"3,14","tolerance":0.01}]}`,
			wantKey: `{"accepted":[{"value":"3,14","match":"numeric","tolerance":0.01}]}`},
		{name: "short answer text default case insensitive", typ: modelBank.TypeShortAnswer,
			content: `{"stem":"Ibu kota?"}`, key: `{"accepted":[{"value":" Jakarta "}]}`,
			wantContent: `{"stem":"Ibu kota?","input":"text"}`, wantKey: `{"accepted":[{"value":"Jakarta","match":"case_insensitive"}]}`},
		{name: "essay without key", typ: modelBank.TypeEssay,
			content: `{"stem":"Jelaskan fotosintesis","max_words":200}`, scoring: `{"rubric":[{"criterion":"Konsep","max":5}]}`,
			wantKey: ``, wantScoring: `{"rubric":[{"criterion":"Konsep","max":5}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := errr.Validation{}
			got := normalizeBody(tt.typ, raw(tt.content), raw(tt.key), raw(tt.scoring), v)
			if err := v.Err(); err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if tt.wantContent != "" && string(got.Content) != tt.wantContent {
				t.Errorf("content = %s\nwant      %s", got.Content, tt.wantContent)
			}
			if string(got.AnswerKey) != tt.wantKey && tt.wantKey != "" || (tt.wantKey == "" && tt.typ == modelBank.TypeEssay && got.AnswerKey != nil) {
				t.Errorf("answer_key = %s, want %s", got.AnswerKey, tt.wantKey)
			}
			if tt.wantScoring != "" && string(got.Scoring) != tt.wantScoring {
				t.Errorf("scoring = %s, want %s", got.Scoring, tt.wantScoring)
			}
			if tt.wantInText != "" && got.SearchText != tt.wantInText {
				t.Errorf("search_text = %q, want %q", got.SearchText, tt.wantInText)
			}
			// normalizing the stored output again must be a no-op (decides new versions)
			again := normalizeBody(tt.typ, got.Content, got.AnswerKey, got.Scoring, errr.Validation{})
			if !again.equal(got) {
				t.Errorf("normalization is not idempotent:\n%s\n%s", got.Content, again.Content)
			}
		})
	}
}

func TestNormalizeBodyInvalid(t *testing.T) {
	opts := `"options":[{"id":"o1","body":"A"},{"id":"o2","body":"B"}]`
	tests := []struct {
		name                  string
		typ                   modelBank.ItemType
		content, key, scoring string
		wantField             string
	}{
		{"unknown type", "matching", `{"stem":"x"}`, ``, ``, "type"},
		{"missing content", modelBank.TypeMCQSingle, ``, `{"correct":"o1"}`, ``, "content"},
		{"unknown content field", modelBank.TypeTrueFalse, `{"stem":"x","answer":true}`, `{"value":true}`, ``, "content"},
		{"blank stem", modelBank.TypeTrueFalse, `{"stem":"<p> </p><script>x</script>"}`, `{"value":true}`, ``, "content.stem"},
		{"one option", modelBank.TypeMCQSingle, `{"stem":"x","options":[{"id":"o1","body":"A"}]}`, `{"correct":"o1"}`, ``, "content.options"},
		{"duplicate option id", modelBank.TypeMCQSingle, `{"stem":"x","options":[{"id":"o1","body":"A"},{"id":"o1","body":"B"}]}`, `{"correct":"o1"}`, ``, "content.options[1].id"},
		{"bad option id", modelBank.TypeMCQSingle, `{"stem":"x","options":[{"id":"O 1","body":"A"},{"id":"o2","body":"B"}]}`, `{"correct":"o2"}`, ``, "content.options[0].id"},
		{"blank option", modelBank.TypeMCQSingle, `{"stem":"x","options":[{"id":"o1","body":""},{"id":"o2","body":"B"}]}`, `{"correct":"o2"}`, ``, "content.options[0].body"},
		{"key not an option", modelBank.TypeMCQSingle, `{"stem":"x",` + opts + `}`, `{"correct":"o9"}`, ``, "answer_key.correct"},
		{"single with list key", modelBank.TypeMCQSingle, `{"stem":"x",` + opts + `}`, `{"correct":["o1"]}`, ``, "answer_key.correct"},
		{"missing key", modelBank.TypeMCQSingle, `{"stem":"x",` + opts + `}`, ``, ``, "answer_key.correct"},
		{"multi empty key", modelBank.TypeMCQMulti, `{"stem":"x",` + opts + `}`, `{"correct":[]}`, ``, "answer_key.correct"},
		{"multi duplicate key", modelBank.TypeMCQMulti, `{"stem":"x",` + opts + `}`, `{"correct":["o1","o1"]}`, ``, "answer_key.correct"},
		{"multi bad method", modelBank.TypeMCQMulti, `{"stem":"x",` + opts + `}`, `{"correct":["o1"]}`, `{"method":"best"}`, "scoring.method"},
		{"tf missing value", modelBank.TypeTrueFalse, `{"stem":"x"}`, `{}`, ``, "answer_key.value"},
		{"short no accepted", modelBank.TypeShortAnswer, `{"stem":"x"}`, `{"accepted":[]}`, ``, "answer_key.accepted"},
		{"short numeric not number", modelBank.TypeShortAnswer, `{"stem":"x"}`, `{"accepted":[{"value":"abc","match":"numeric"}]}`, ``, "answer_key.accepted[0].value"},
		{"tolerance on text", modelBank.TypeShortAnswer, `{"stem":"x"}`, `{"accepted":[{"value":"a","tolerance":1}]}`, ``, "answer_key.accepted[0].tolerance"},
		{"essay bad rubric", modelBank.TypeEssay, `{"stem":"x"}`, ``, `{"rubric":[{"criterion":"","max":0}]}`, "scoring.rubric[0].criterion"},
		{"huge stem", modelBank.TypeTrueFalse, `{"stem":"` + strings.Repeat("a", 70000) + `"}`, `{"value":true}`, ``, "content.stem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := errr.Validation{}
			normalizeBody(tt.typ, raw(tt.content), raw(tt.key), raw(tt.scoring), v)
			if v[tt.wantField] == "" {
				t.Fatalf("want error on %q, got %v", tt.wantField, v)
			}
		})
	}
}

func TestNormalizeMeta(t *testing.T) {
	pts, diff, tags, expl := 2.345, " HARD ", []string{" Aljabar", "aljabar", "", "HOTS"}, `<p>Karena <em>x</em></p><iframe></iframe>`
	in := modelBank.VersionInput{DefaultPoints: &pts, Difficulty: &diff, Tags: &tags, Explanation: &expl}
	v := errr.Validation{}
	normalizeMeta(&in, v)
	if v.Err() != nil {
		t.Fatal(v.Err())
	}
	if *in.DefaultPoints != 2.35 || *in.Difficulty != "hard" || strings.Join(*in.Tags, ",") != "aljabar,hots" || *in.Explanation != `<p>Karena <em>x</em></p>` {
		t.Fatalf("normalized = %v %v %v %v", *in.DefaultPoints, *in.Difficulty, *in.Tags, *in.Explanation)
	}
	bad := "extreme"
	v = errr.Validation{}
	normalizeMeta(&modelBank.VersionInput{Difficulty: &bad}, v)
	if v["difficulty"] == "" {
		t.Fatal("unknown difficulty must fail")
	}
}
