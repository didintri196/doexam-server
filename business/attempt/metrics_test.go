package attempt

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"unsafe"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/errr"
	"doexam/pkg/metrics/metricstest"
)

// dupStore reports every event as already stored.
type dupStore struct{ *syncStore }

func (dupStore) InsertEvent(context.Context, *modelAttempt.EventEntity) (bool, error) {
	return false, nil
}

func TestSyncMetrics(t *testing.T) {
	r := metricstest.Install(t)
	p := modelAuth.Principal{TenantID: "t1", UserID: "u1"}
	ctx := context.Background()
	store := newSyncStore()
	svc := contentService(store, nil)
	events := []modelAttempt.ClientEvent{
		event("0192a000-0000-7000-8000-0000000000b1", modelAttempt.EventFlagSet, `{"flagged":true}`, syncItemID),
		event("0192a000-0000-7000-8000-0000000000b2", modelAttempt.EventFlagSet, `{"flagged":true}`, "0192a000-0000-7000-8000-0000000000ff"),
		event("0192a000-0000-7000-8000-0000000000b3", modelAttempt.EventFocusLost, `{}`, ""),
		event("not-a-uuid", modelAttempt.EventFocusLost, `{}`, ""),
	}
	if _, err := svc.Sync(ctx, p, syncAttemptID, modelAttempt.EventsInput{Events: events}, "d1", modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	dup := contentService(dupStore{newSyncStore()}, nil)
	if _, err := dup.Sync(ctx, p, syncAttemptID, modelAttempt.EventsInput{Events: events[:1]}, "d1", modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	closed := newSyncStore()
	closed.attempt.Status = modelAttempt.StatusSubmitted
	closedSvc := contentService(closed, nil)
	if _, err := closedSvc.Sync(ctx, p, syncAttemptID, modelAttempt.EventsInput{Events: events[:1]}, "d1", modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}

	const ev = "doexam.attempt.events"
	tests := []struct {
		name   string
		metric string
		match  map[string]string
		want   float64
	}{
		{"accepted flag", ev, map[string]string{"type": "FLAG_SET", "outcome": "accepted"}, 1},
		{"accepted focus lost", ev, map[string]string{"type": "FOCUS_LOST", "outcome": "accepted"}, 1},
		{"rejected total", ev, map[string]string{"outcome": "rejected"}, 3},
		{"duplicate", ev, map[string]string{"type": "FLAG_SET", "outcome": "duplicate"}, 1},
		{"unknown item", "doexam.attempt.events.rejected", map[string]string{"reason": "unknown_item"}, 1},
		{"bad id", "doexam.attempt.events.rejected", map[string]string{"reason": "invalid_id"}, 1},
		{"closed attempt", "doexam.attempt.events.rejected", map[string]string{"reason": "attempt_closed"}, 1},
		{"violation", "doexam.attempt.violations", map[string]string{"type": "FOCUS_LOST"}, 1},
		{"batches", "doexam.attempt.sync.batch.size", nil, 3},
		{"client lag samples", "doexam.attempt.sync.client_lag", nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Value(tt.metric, tt.match); got != tt.want {
				t.Fatalf("%s%v = %v, want %v", tt.metric, tt.match, got, tt.want)
			}
		})
	}
	// each service has its own presence: the running attempt is connected, the closed one is not tracked
	if c, s := svc.metrics.presence.count(time.Now()); c != 1 || s != 0 {
		t.Fatalf("presence = %d connected, %d stale; want 1, 0", c, s)
	}
	if c, _ := closedSvc.metrics.presence.count(time.Now()); c != 0 {
		t.Fatalf("closed attempt counted as connected")
	}
}

func TestPresence(t *testing.T) {
	p := &presence{seen: map[string]time.Time{}}
	now := time.Unix(10_000, 0)
	p.touch("fresh", now.Add(-10*time.Second))
	p.touch("stale", now.Add(-5*time.Minute))
	p.touch("gone", now.Add(-7*time.Hour))
	p.touch("done", now)
	p.forget("done")
	if c, s := p.count(now); c != 1 || s != 1 {
		t.Fatalf("count = %d, %d; want 1 connected, 1 stale", c, s)
	}
	if _, ok := p.seen["gone"]; ok {
		t.Fatal("abandoned attempt should be forgotten")
	}
}

func TestStartResult(t *testing.T) {
	tests := []struct {
		err     error
		resumed bool
		want    string
	}{
		{nil, false, "started"},
		{nil, true, "resumed"},
		{fmt.Errorf("x: %w", errBadToken), false, "bad_token"},
		{errNotParticipant, false, "not_participant"},
		{errDeviceLocked, false, "device_locked"},
		{&errr.ValidationError{Fields: map[string]string{"device_id": "required"}}, false, "invalid"},
		{errr.ErrForbidden, false, "forbidden"},
		{errSessionNotFound, false, "not_found"},
		{errr.WithMessage(errr.ErrConflict, "the session has ended"), false, "conflict"},
		{errors.New("db down"), false, "error"},
	}
	for _, tt := range tests {
		if got := startResult(tt.err, tt.resumed); got != tt.want {
			t.Errorf("startResult(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestRejectReasonBounded(t *testing.T) {
	tests := map[string]string{
		"after_deadline":                 "after_deadline",
		"forward_only":                   "forward_only",
		"unknown event type":             "unknown_type",
		"unknown attempt_item_id":        "unknown_item",
		"client_event_id must be a UUID": "invalid_id",
		"text max 5000 characters":       "invalid_payload",
		"unknown option":                 "invalid_payload",
	}
	for in, want := range tests {
		if got := rejectReason(in); got != want {
			t.Errorf("rejectReason(%q) = %q, want %q", in, got, want)
		}
	}
	if got := eventType("SOMETHING_NEW"); got != "other" {
		t.Errorf("eventType = %q, want other", got)
	}
}

// Fiber hands out params that point into a request buffer reused by the next request;
// presence must not keep that memory as its map key (seen in a 500-participant load test
// as thousands of phantom "stale" attempts).
func TestPresenceCopiesID(t *testing.T) {
	p := &presence{seen: map[string]time.Time{}}
	const id = "0192a000-0000-7000-8000-000000000001"
	buf := []byte(id)
	p.touch(unsafe.String(&buf[0], len(buf)), time.Now())
	copy(buf, "0192a000-0000-7000-8000-000000000002") // the next request reuses the buffer
	if _, ok := p.seen[id]; !ok {
		t.Fatalf("attempt %s lost after its request buffer was reused: %v", id, p.seen)
	}
}
