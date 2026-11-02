package audit

import "testing"

func TestTruncateKeepsUTF8Valid(t *testing.T) {
	if got := Truncate("aé", 2); got != "a" {
		t.Fatalf("Truncate = %q, want %q", got, "a")
	}
}

func TestNew(t *testing.T) {
	e := New(Entry{TenantID: "t", Action: ActionUserCreate, EntityType: "user", After: map[string]string{"a": "b"}})
	if e.ActorID != nil || e.IP != nil || e.Before != nil {
		t.Fatalf("empty fields must be NULL: %+v", e)
	}
	if string(e.After) != `{"a":"b"}` {
		t.Fatalf("after = %s", e.After)
	}
}
