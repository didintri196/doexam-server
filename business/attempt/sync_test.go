package attempt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
)

const (
	syncAttemptID = "0192a000-0000-7000-8000-000000000001"
	syncItemID    = "0192a000-0000-7000-8000-000000000002"
	syncSectionID = "0192a000-0000-7000-8000-000000000003"
)

// syncStore counts the reads a sync batch causes; other storer methods panic.
type syncStore struct {
	AttemptStorer
	attempt     modelAttempt.AttemptEntity
	sectionRead int
	itemRead    int
	inserted    int
	flagged     []string
	updated     int
}

func (f *syncStore) FindAttempt(context.Context, string, bool) (modelAttempt.AttemptEntity, error) {
	return f.attempt, nil
}

func (f *syncStore) ListAttemptSections(context.Context, string) ([]modelAttempt.AttemptSectionEntity, error) {
	f.sectionRead++
	started := time.Now().Add(-time.Minute)
	return []modelAttempt.AttemptSectionEntity{{ID: syncSectionID, StartedAt: &started}}, nil
}

func (f *syncStore) ListAttemptItems(context.Context, string) ([]modelAttempt.AttemptItemEntity, error) {
	f.itemRead++
	return []modelAttempt.AttemptItemEntity{{ID: syncItemID, AttemptSectionID: syncSectionID, Position: 1, ItemVersionID: "v1"}}, nil
}

func (f *syncStore) ItemContents(context.Context, []string) ([]ItemContent, error) {
	return []ItemContent{{VersionID: "v1", Type: "mcq", Content: []byte(`{}`)}}, nil
}

func (f *syncStore) InsertEvent(context.Context, *modelAttempt.EventEntity) (bool, error) {
	f.inserted++
	return true, nil
}

func (f *syncStore) SetFlag(_ context.Context, itemID string, _ bool) error {
	f.flagged = append(f.flagged, itemID)
	return nil
}

func (f *syncStore) UpdateAttempt(context.Context, *modelAttempt.AttemptEntity) error {
	f.updated++
	return nil
}

func newSyncStore() *syncStore {
	return &syncStore{attempt: modelAttempt.AttemptEntity{
		ID: syncAttemptID, TenantID: "t1", UserID: "u1", SessionID: "s1",
		Status: modelAttempt.StatusInProgress, DeadlineAt: time.Now().Add(time.Hour), ConfigSnapshot: []byte(`{}`),
	}}
}

func event(id, typ, payload string, itemID string) modelAttempt.ClientEvent {
	now := time.Now()
	return modelAttempt.ClientEvent{ClientEventID: id, ClientSeq: 1, Type: typ, AttemptItemID: itemID,
		Payload: json.RawMessage(payload), ClientTS: &now}
}

func TestSyncLoadsItemsOnlyForItemEvents(t *testing.T) {
	p := modelAuth.Principal{TenantID: "t1", UserID: "u1"}
	tests := []struct {
		name       string
		events     []modelAttempt.ClientEvent
		wantLoads  int
		wantAccept int
	}{
		{"heartbeat", nil, 0, 0},
		{"announcement read", []modelAttempt.ClientEvent{
			event("0192a000-0000-7000-8000-0000000000a1", modelAttempt.EventAnnouncementRead, `{"announcement_id":"0192a000-0000-7000-8000-0000000000ab"}`, ""),
		}, 0, 1},
		{"audio played", []modelAttempt.ClientEvent{
			event("0192a000-0000-7000-8000-0000000000a2", modelAttempt.EventMediaPlayed, `{"media_id":"0192a000-0000-7000-8000-0000000000ac","play_no":1}`, ""),
		}, 0, 1},
		{"flag", []modelAttempt.ClientEvent{
			event("0192a000-0000-7000-8000-0000000000a3", modelAttempt.EventFlagSet, `{"flagged":true}`, syncItemID),
		}, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newSyncStore()
			svc := contentService(store, nil)
			out, err := svc.Sync(context.Background(), p, syncAttemptID, modelAttempt.EventsInput{Events: tt.events}, "d1", modelAudit.Client{})
			if err != nil {
				t.Fatal(err)
			}
			if store.itemRead != tt.wantLoads || store.sectionRead != tt.wantLoads {
				t.Fatalf("items read %d, sections read %d; want %d each", store.itemRead, store.sectionRead, tt.wantLoads)
			}
			if out.Accepted != tt.wantAccept || len(out.Rejected) != 0 {
				t.Fatalf("accepted %d, rejected %v; want %d accepted", out.Accepted, out.Rejected, tt.wantAccept)
			}
			// every batch, even an empty one, marks the attempt as seen
			if store.updated != 1 {
				t.Fatalf("attempt updated %d times, want 1", store.updated)
			}
		})
	}
}

func TestSyncFlagStillTargetsItem(t *testing.T) {
	store := newSyncStore()
	svc := contentService(store, nil)
	ev := event("0192a000-0000-7000-8000-0000000000a4", modelAttempt.EventFlagSet, `{"flagged":true}`, syncItemID)
	if _, err := svc.Sync(context.Background(), modelAuth.Principal{TenantID: "t1", UserID: "u1"}, syncAttemptID,
		modelAttempt.EventsInput{Events: []modelAttempt.ClientEvent{ev}}, "d1", modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	if len(store.flagged) != 1 || store.flagged[0] != syncItemID {
		t.Fatalf("flagged = %v", store.flagged)
	}

	// an unknown item is still rejected when the batch mixes it with other events
	store = newSyncStore()
	svc = contentService(store, nil)
	unknown := event("0192a000-0000-7000-8000-0000000000a5", modelAttempt.EventFlagSet, `{"flagged":true}`, "0192a000-0000-7000-8000-0000000000ff")
	out, err := svc.Sync(context.Background(), modelAuth.Principal{TenantID: "t1", UserID: "u1"}, syncAttemptID,
		modelAttempt.EventsInput{Events: []modelAttempt.ClientEvent{unknown}}, "d1", modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rejected) != 1 || out.Rejected[0].Reason != "unknown attempt_item_id" {
		t.Fatalf("rejected = %v", out.Rejected)
	}
}
