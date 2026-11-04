package attempt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	modelExam "doexam/models/exam"
)

func row(typ string, opts ...string) *ItemRow {
	return &ItemRow{AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i1", AttemptSectionID: "s1", Position: 1, OptionOrder: opts}, Type: typ}
}

func TestNormalizeResponse(t *testing.T) {
	tests := []struct {
		name    string
		item    *ItemRow
		payload string
		want    string // "" = cleared
		reason  string
	}{
		{"single ok", row(typeMCQSingle, "o1", "o2"), `{"response":{"selected":"o2"}}`, `{"selected":"o2"}`, ""},
		{"single unknown option", row(typeMCQSingle, "o1"), `{"response":{"selected":"o9"}}`, "", "unknown option"},
		{"single clear", row(typeMCQSingle, "o1"), `{"response":{"selected":null}}`, "", ""},
		{"response null clears", row(typeMCQSingle, "o1"), `{"response":null}`, "", ""},
		{"multi dedup", row(typeMCQMulti, "a", "b"), `{"response":{"selected":["b","a","b"]}}`, `{"selected":["b","a"]}`, ""},
		{"multi empty clears", row(typeMCQMulti, "a"), `{"response":{"selected":[]}}`, "", ""},
		{"tf", row(typeTrueFalse), `{"response":{"value":false}}`, `{"value":false}`, ""},
		{"short", row(typeShortAnswer), `{"response":{"text":"Jakarta"}}`, `{"text":"Jakarta"}`, ""},
		{"short too long", row(typeShortAnswer), `{"response":{"text":"` + strings.Repeat("a", 501) + `"}}`, "", "text max 500"},
		{"essay blank clears", row(typeEssay), `{"response":{"text":"  "}}`, "", ""},
		{"not an object", row(typeEssay), `[1]`, "", "payload must be"},
		{"wrong shape", row(typeMCQSingle, "o1"), `{"response":{"selected":["o1"]}}`, "", "selected must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := normalizeResponse(tt.item, json.RawMessage(tt.payload))
			if !strings.HasPrefix(reason, tt.reason) || (tt.reason == "" && reason != "") {
				t.Fatalf("reason = %q, want %q", reason, tt.reason)
			}
			if string(got) != tt.want {
				t.Fatalf("response = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCheckEvent(t *testing.T) {
	deadline := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	before, after := deadline.Add(-time.Minute), deadline.Add(time.Minute)
	a := modelAttempt.AttemptEntity{Status: modelAttempt.StatusInProgress, DeadlineAt: deadline}
	i1 := row(typeMCQSingle, "o1")
	i2 := &ItemRow{AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i2", AttemptSectionID: "s1", Position: 2}, Type: typeTrueFalse}
	i3 := &ItemRow{AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i3", AttemptSectionID: "s2", Position: 3}, Type: typeTrueFalse}
	byID := map[string]*ItemRow{"i1": i1, "i2": i2, "i3": i3}
	items := []ItemRow{*i1, *i2, *i3}
	open := map[string]bool{"s1": true, "s2": false}
	free := snapshot{Navigation: "free"}
	ev := func(typ, item string, ts *time.Time) modelAttempt.ClientEvent {
		return modelAttempt.ClientEvent{ClientEventID: "0192a000-0000-7000-8000-000000000001", Type: typ, AttemptItemID: item, ClientTS: ts}
	}
	played := func(payload string) modelAttempt.ClientEvent {
		return modelAttempt.ClientEvent{ClientEventID: "0192a000-0000-7000-8000-000000000001", Type: modelAttempt.EventMediaPlayed, Payload: json.RawMessage(payload)}
	}
	svc := &AttemptService{}
	tests := []struct {
		name  string
		ev    modelAttempt.ClientEvent
		a     modelAttempt.AttemptEntity
		snap  snapshot
		items []ItemRow
		now   time.Time
		want  string
	}{
		{"ok", ev(modelAttempt.EventResponseSaved, "i1", nil), a, free, items, before, ""},
		{"bad id", modelAttempt.ClientEvent{ClientEventID: "x", Type: modelAttempt.EventFocusLost}, a, free, items, before, "client_event_id"},
		{"server-only type", ev(modelAttempt.EventSubmitted, "", nil), a, free, items, before, "unknown event type"},
		{"unknown item", ev(modelAttempt.EventResponseSaved, "nope", nil), a, free, items, before, "unknown attempt_item_id"},
		{"closed section", ev(modelAttempt.EventResponseSaved, "i3", nil), a, free, items, before, "section_closed"},
		{"late without client ts", ev(modelAttempt.EventResponseSaved, "i1", nil), a, free, items, after, "after_deadline"},
		{"late but recorded in time", ev(modelAttempt.EventResponseSaved, "i1", &before), a, free, items, after, ""},
		{"late and recorded late", ev(modelAttempt.EventResponseSaved, "i1", &after), a, free, items, after, "after_deadline"},
		{"locked answer", ev(modelAttempt.EventResponseSaved, "i1", nil), modelAttempt.AttemptEntity{Status: modelAttempt.StatusLocked, DeadlineAt: deadline}, free, items, before, "locked"},
		{"locked telemetry ok", ev(modelAttempt.EventFocusLost, "", nil), modelAttempt.AttemptEntity{Status: modelAttempt.StatusLocked, DeadlineAt: deadline}, free, items, before, ""},
		{"media played", played(`{"media_id":"0192a000-0000-7000-8000-0000000000aa","play_no":2}`), a, free, items, before, ""},
		{"media played while locked", played(`{"media_id":"0192a000-0000-7000-8000-0000000000aa","play_no":1}`), modelAttempt.AttemptEntity{Status: modelAttempt.StatusLocked, DeadlineAt: deadline}, free, items, before, ""},
		{"media played bad id", played(`{"media_id":"x","play_no":1}`), a, free, items, before, "payload must be"},
		{"media played no number", played(`{"media_id":"0192a000-0000-7000-8000-0000000000aa"}`), a, free, items, before, "payload must be"},
		{"media played late", modelAttempt.ClientEvent{ClientEventID: "0192a000-0000-7000-8000-000000000001", Type: modelAttempt.EventMediaPlayed, ClientTS: &after,
			Payload: json.RawMessage(`{"media_id":"0192a000-0000-7000-8000-0000000000aa","play_no":1}`)}, a, free, items, after, "after_deadline"},
		{"forward only after visiting a later item", ev(modelAttempt.EventResponseSaved, "i1", nil), a, snapshot{Navigation: string(modelExam.NavigationForwardOnly)},
			[]ItemRow{*i1, {AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i2", AttemptSectionID: "s1", Position: 2, VisitCount: 1}}}, before, "forward_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, _ := svc.checkEvent(tt.ev, tt.a, tt.snap, byID, open, tt.items, tt.now)
			if !strings.HasPrefix(reason, tt.want) || (tt.want == "" && reason != "") {
				t.Fatalf("reason = %q, want %q", reason, tt.want)
			}
		})
	}
}

func TestItemViewHidesKeyAndOrdersOptions(t *testing.T) {
	it := ItemRow{
		AttemptItemEntity: modelAttempt.AttemptItemEntity{ID: "i1", Position: 4, OptionOrder: []string{"o3", "o1", "o2"}, MaxPoints: 2},
		Type:              typeMCQSingle,
		Content:           []byte(`{"stem":"<p>Q</p>","options":[{"id":"o1","body":"A"},{"id":"o2","body":"B"},{"id":"o3","body":"C"}]}`),
		AnswerKey:         []byte(`{"correct":"o2"}`),
		Explanation:       []byte(`{"body":"because"}`),
	}
	v := itemView(it)
	if len(v.Options) != 3 || v.Options[0].ID != "o3" || v.Options[1].ID != "o1" {
		t.Fatalf("options = %+v", v.Options)
	}
	b, _ := json.Marshal(v)
	for _, leak := range []string{"correct", "because", "answer_key", "explanation"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("participant view leaks %q: %s", leak, b)
		}
	}
	if string(v.Response) != "null" {
		t.Fatalf("response = %s", v.Response)
	}
}

func TestSnapshotModeAndDeadlines(t *testing.T) {
	sixty := 60
	e := modelExam.ExamEntity{Title: "T", Security: []byte(`{"profile":"standard"}`), Settings: []byte(`{}`)}
	shared := makeSnapshot(e, []modelExam.SectionEntity{{ID: "a"}, {ID: "b"}}, 0)
	if shared.SectionMode != modeShared || shared.SecurityProfile != "standard" {
		t.Fatalf("shared = %+v", shared)
	}
	seq := makeSnapshot(e, []modelExam.SectionEntity{{ID: "a", DurationMinutes: &sixty}, {ID: "b"}}, 15)
	if seq.SectionMode != modeSequential || seq.ExtraMinutes != 15 {
		t.Fatalf("sequential = %+v", seq)
	}
	start := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	end := start.Add(70 * time.Minute)
	if d := sectionDeadline(start, seq.Sections[0], 15*time.Minute, end); !d.Equal(end) {
		t.Fatalf("section deadline must be capped by the attempt: %v", d)
	}
	if d := sectionDeadline(start, seq.Sections[0], 5*time.Minute, start.Add(3*time.Hour)); !d.Equal(start.Add(65 * time.Minute)) {
		t.Fatalf("section deadline = %v", d)
	}
	if d := sectionDeadline(start, seq.Sections[1], 0, end); !d.Equal(end) {
		t.Fatal("a section without duration shares the remaining time")
	}
}

func TestPercent(t *testing.T) {
	for _, tt := range []struct{ raw, max, want float64 }{{7, 10, 70}, {-2, 10, 0}, {1, 3, 33.33}, {5, 0, 0}} {
		if got := percent(tt.raw, tt.max); got != tt.want {
			t.Errorf("percent(%v,%v) = %v, want %v", tt.raw, tt.max, got, tt.want)
		}
	}
}
