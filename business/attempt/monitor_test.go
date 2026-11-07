package attempt

import (
	"context"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
)

const monitorSessionID = "0192a000-0000-7000-8000-0000000000c1"

// monitorStore fakes what Monitor reads; RunningAttempts (lock every attempt) panics
// through the embedded nil interface, so a regression fails loudly.
type monitorStore struct {
	AttemptStorer
	cutoffs []time.Time
}

func (f *monitorStore) FindSession(context.Context, string) (SessionInfo, error) {
	return SessionInfo{ID: monitorSessionID, Status: "open"}, nil
}

func (f *monitorStore) OverdueAttempts(_ context.Context, _ string, cutoff time.Time) ([]modelAttempt.AttemptEntity, error) {
	f.cutoffs = append(f.cutoffs, cutoff)
	return nil, nil
}

func (f *monitorStore) Monitor(context.Context, string) ([]MonitorAttempt, error) {
	st := modelAttempt.StatusInProgress
	return []MonitorAttempt{{UserID: "u1", Status: &st, Answered: 3, ItemCount: 12}, {UserID: "u2"}}, nil
}

func TestMonitorLocksOnlyOverdueAttempts(t *testing.T) {
	store := &monitorStore{}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	svc := contentService(store, nil)
	svc.now = func() time.Time { return now }
	p := modelAuth.Principal{TenantID: "t1", UserID: "admin", Permissions: []string{modelAuth.PermSessionManage}}

	out, err := svc.Monitor(context.Background(), p, monitorSessionID, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	// the cutoff matches tick: an attempt or section is closed once its deadline + grace has passed
	if len(store.cutoffs) != 1 || !store.cutoffs[0].Equal(now.Add(-syncGrace)) {
		t.Fatalf("cutoffs = %v, want [%v]", store.cutoffs, now.Add(-syncGrace))
	}
	if len(out.Rows) != 2 || out.Counts["in_progress"] != 1 || out.Counts["not_started"] != 1 {
		t.Fatalf("monitor = %+v", out)
	}
}
