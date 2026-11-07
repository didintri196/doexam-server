package attempt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/errr"
)

const monitorSessionID = "0192a000-0000-7000-8000-0000000000c1"

// monitorStore fakes what Monitor reads; RunningAttempts (lock every attempt) panics
// through the embedded nil interface, so a regression fails loudly.
type monitorStore struct {
	AttemptStorer
	cutoffs     []time.Time
	pages       []modelAttempt.MonitorFilter
	idleBefores []time.Time
	watched     []string
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

func (f *monitorStore) MonitorPage(_ context.Context, _ string, filter modelAttempt.MonitorFilter, idleBefore time.Time) (MonitorResult, error) {
	f.pages = append(f.pages, filter)
	f.idleBefores = append(f.idleBefores, idleBefore)
	counts := map[string]int{"total": 42, "violations": 3}
	if len(filter.UserIDs) > 0 {
		f.watched = filter.UserIDs
		return MonitorResult{Rows: []MonitorAttempt{{UserID: filter.UserIDs[0]}}, Total: 1, Counts: counts}, nil
	}
	st := modelAttempt.StatusInProgress
	return MonitorResult{Rows: []MonitorAttempt{{UserID: "u1", Status: &st, FocusLost: 2, DeviceChanged: 1, Idle: true}}, Total: 42, Counts: counts}, nil
}

func monitorFixture() (*AttemptService, *monitorStore, modelAuth.Principal, time.Time) {
	store := &monitorStore{}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	svc := contentService(store, nil)
	svc.now = func() time.Time { return now }
	return svc, store, modelAuth.Principal{TenantID: "t1", UserID: "admin", Permissions: []string{modelAuth.PermSessionManage}}, now
}

func TestMonitorList(t *testing.T) {
	svc, store, p, now := monitorFixture()
	f := modelAttempt.MonitorFilter{View: modelAttempt.ViewViolations, ViolationType: modelAttempt.ViolationDeviceChanged, Seat: " A- ", Page: 2, PerPage: 10}
	out, total, err := svc.MonitorList(context.Background(), p, monitorSessionID, f, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.cutoffs) != 1 || !store.cutoffs[0].Equal(now.Add(-syncGrace)) {
		t.Fatalf("overdue attempts not ticked first: %v", store.cutoffs)
	}
	if store.pages[0].Seat != "A-" || !store.idleBefores[0].Equal(now.Add(-monitorIdleAfter)) {
		t.Fatalf("filter passed as %+v, idleBefore %v", store.pages[0], store.idleBefores[0])
	}
	r := out.Rows[0]
	if total != 42 || out.Counts["violations"] != 3 || r.Violations != 3 || r.ViolationCounts[modelAttempt.ViolationFocusLost] != 2 ||
		r.ViolationCounts[modelAttempt.ViolationDeviceChanged] != 1 || !r.Idle || !out.ServerTime.Equal(now) {
		t.Fatalf("out = %+v, total = %d", out, total)
	}

	// empty view means all
	if _, _, err := svc.MonitorList(context.Background(), p, monitorSessionID, modelAttempt.MonitorFilter{Page: 1, PerPage: 10}, modelAudit.Client{}); err != nil || store.pages[1].View != modelAttempt.ViewAll {
		t.Fatalf("default view: %v %+v", err, store.pages[1])
	}
}

func TestMonitorListRejectsBadFilters(t *testing.T) {
	svc, store, p, _ := monitorFixture()
	for name, f := range map[string]modelAttempt.MonitorFilter{
		"view":           {View: "everything"},
		"violation type": {View: modelAttempt.ViewViolations, ViolationType: "cheating"},
		"group":          {GroupID: "not-a-uuid"},
		"seat":           {Seat: strings.Repeat("A", 51)},
	} {
		f.Page, f.PerPage = 1, 10
		if _, _, err := svc.MonitorList(context.Background(), p, monitorSessionID, f, modelAudit.Client{}); !isValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	if len(store.pages) != 0 {
		t.Fatal("invalid filters reached the database")
	}
}

func TestMonitorWatch(t *testing.T) {
	svc, store, p, _ := monitorFixture()
	a, b := "0192A000-0000-7000-8000-0000000000A1", "0192a000-0000-7000-8000-0000000000a2"
	out, err := svc.MonitorWatch(context.Background(), p, monitorSessionID, modelAttempt.WatchInput{UserIDs: []string{a, " " + b, strings.ToLower(a)}}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.watched) != 2 || store.watched[0] != strings.ToLower(a) || store.watched[1] != b {
		t.Fatalf("watched = %v (want lowercased, trimmed, deduplicated)", store.watched)
	}
	if store.pages[0].Seat != "" || out.Counts["total"] != 42 || len(store.cutoffs) != 1 {
		t.Fatalf("watch filter %+v, out %+v", store.pages[0], out)
	}
	// an empty list still returns the counts
	if out, err := svc.MonitorWatch(context.Background(), p, monitorSessionID, modelAttempt.WatchInput{}, modelAudit.Client{}); err != nil || out.Counts["total"] != 42 || store.pages[1].View != modelAttempt.ViewAll || store.pages[1].PerPage != 0 {
		t.Fatalf("empty watch: %v %+v %+v", err, out, store.pages[1])
	}

	tooMany := make([]string, modelAttempt.MaxWatch+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("0192a000-0000-7000-8000-%012d", i)
	}
	for name, ids := range map[string][]string{"over 25": tooMany, "not uuid": {"x"}} {
		if _, err := svc.MonitorWatch(context.Background(), p, monitorSessionID, modelAttempt.WatchInput{UserIDs: ids}, modelAudit.Client{}); !isValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func isValidation(err error) bool {
	var v *errr.ValidationError
	return errors.As(err, &v)
}
