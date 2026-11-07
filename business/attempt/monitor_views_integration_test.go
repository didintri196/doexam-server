//go:build integration

// DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//
//	go test -tags integration ./business/attempt -run IntegrationMonitorViews -v
package attempt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	dbpg "doexam/pkg/db-pg"
)

// TestIntegrationMonitorViews runs every monitor view against the largest session in the
// database, after adding violations, locks and seats inside a transaction that is always
// rolled back. Each view, paged to the end, must hold exactly the participants the legacy
// full monitor says it should, once each, in the view's order; counts must match too.
func TestIntegrationMonitorViews(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	defer dbpg.Close(db)
	var sess struct {
		ID       string
		TenantID string
		N        int
	}
	err = db.Raw(`SELECT es.id, es.tenant_id, count(*) AS n FROM exam_sessions es
		JOIN exam_session_participants sp ON sp.session_id = es.id
		WHERE EXISTS (SELECT 1 FROM attempts a WHERE a.session_id = es.id)
		GROUP BY es.id ORDER BY n DESC LIMIT 1`).Scan(&sess).Error
	if err != nil || sess.ID == "" {
		t.Skip("no session with attempts")
	}
	t.Logf("session %s: %d participants", sess.ID, sess.N)
	repo := NewAttemptRepo(db)
	tx := dbpg.NewTxManager(db)
	now := time.Now().UTC()
	idleBefore := now.Add(-monitorIdleAfter)

	err = tx.WithTenant(context.Background(), sess.TenantID, func(ctx context.Context) error {
		conn := dbpg.Conn(ctx, db)
		// every 7th attempt gets violations (1-3 of each kind, by position), every 11th running one is locked
		setup := []string{
			`WITH a AS (SELECT id, tenant_id, row_number() OVER (ORDER BY id) AS n FROM attempts WHERE session_id = @s)
			 INSERT INTO attempt_events (tenant_id, attempt_id, type, server_ts)
			 SELECT a.tenant_id, a.id, t.type, now() - (a.n || ' seconds')::interval
			 FROM a CROSS JOIN LATERAL (VALUES ('FOCUS_LOST', a.n % 3), ('FULLSCREEN_EXIT', a.n % 2), ('DEVICE_CHANGED', (a.n % 5 = 0)::int)) t(type, k)
			 CROSS JOIN LATERAL generate_series(1, t.k) g
			 WHERE a.n % 7 = 0`,
			// two thirds run again, last seen 0-5 minutes ago (some idle); every 11th of those is locked
			`WITH a AS (SELECT id, row_number() OVER (ORDER BY id) AS n FROM attempts WHERE session_id = @s)
			 UPDATE attempts x SET status = 'in_progress', submitted_at = NULL, submit_reason = NULL,
			        last_seen_at = now() - ((a.n % 300) || ' seconds')::interval
			 FROM a WHERE x.id = a.id AND a.n % 3 <> 0`,
			`WITH a AS (SELECT id, row_number() OVER (ORDER BY id) AS n FROM attempts WHERE session_id = @s AND status = 'in_progress')
			 UPDATE attempts x SET status = 'locked' FROM a WHERE x.id = a.id AND a.n % 11 = 0`,
			// a few users of the tenant join the session without starting
			`INSERT INTO exam_session_participants (tenant_id, session_id, user_id)
			 SELECT u.tenant_id, @s, u.id FROM users u
			 WHERE u.tenant_id = (SELECT tenant_id FROM exam_sessions WHERE id = @s)
			   AND NOT EXISTS (SELECT 1 FROM exam_session_participants sp WHERE sp.session_id = @s AND sp.user_id = u.id)
			 LIMIT 20`,
			`INSERT INTO attempt_events (tenant_id, attempt_id, type, server_ts)
			 SELECT tenant_id, id, 'LOCKED', now() - (abs(hashtext(id::text)) % 600 || ' seconds')::interval FROM attempts WHERE session_id = @s AND status = 'locked'`,
			// seats A-1 .. A-n for half the participants (natural order: A-2 before A-12), the rest none
			`WITH p AS (SELECT user_id, row_number() OVER (ORDER BY user_id) AS n FROM exam_session_participants WHERE session_id = @s)
			 UPDATE exam_session_participants sp SET seat = CASE WHEN p.n % 2 = 0 THEN 'A-' || (p.n / 2) END
			 FROM p WHERE sp.session_id = @s AND sp.user_id = p.user_id`,
		}
		for _, q := range setup {
			if err := conn.Exec(q, map[string]any{"s": sess.ID}).Error; err != nil {
				return fmt.Errorf("setup: %w", err)
			}
		}

		legacy, err := repo.Monitor(ctx, sess.ID)
		if err != nil {
			return err
		}
		byID := map[string]MonitorAttempt{}
		for _, r := range legacy {
			byID[r.UserID] = r
		}
		st := func(r MonitorAttempt) string {
			if r.Status == nil {
				return ""
			}
			return string(*r.Status)
		}
		idle := func(r MonitorAttempt) bool {
			return st(r) == "in_progress" && r.LastSeenAt != nil && r.LastSeenAt.Before(idleBefore)
		}
		match := map[modelAttempt.MonitorView]func(MonitorAttempt) bool{
			modelAttempt.ViewAll:        func(MonitorAttempt) bool { return true },
			modelAttempt.ViewViolations: func(r MonitorAttempt) bool { return r.Violations > 0 },
			modelAttempt.ViewIdle:       idle,
			modelAttempt.ViewLocked:     func(r MonitorAttempt) bool { return st(r) == "locked" },
			modelAttempt.ViewNotStarted: func(r MonitorAttempt) bool { return r.Status == nil },
			modelAttempt.ViewInProgress: func(r MonitorAttempt) bool { return st(r) == "in_progress" },
			modelAttempt.ViewSubmitted:  func(r MonitorAttempt) bool { return st(r) == "submitted" || st(r) == "graded" },
		}

		// counts
		start := time.Now()
		counts, err := repo.MonitorCounts(ctx, sess.ID, modelAttempt.MonitorFilter{}, idleBefore)
		if err != nil {
			return err
		}
		t.Logf("counts: %v in %v", counts, time.Since(start))
		want := map[string]int{"total": len(legacy)}
		for _, r := range legacy {
			for k, v := range map[string]modelAttempt.MonitorView{"not_started": modelAttempt.ViewNotStarted, "in_progress": modelAttempt.ViewInProgress,
				"locked": modelAttempt.ViewLocked, "submitted": modelAttempt.ViewSubmitted, "idle": modelAttempt.ViewIdle, "violations": modelAttempt.ViewViolations} {
				if match[v](r) {
					want[k]++
				}
			}
		}
		for k, v := range want {
			if counts[k] != v {
				t.Errorf("counts[%s] = %d, legacy says %d", k, counts[k], v)
			}
		}
		if want["violations"] == 0 || want["locked"] == 0 {
			t.Errorf("setup did not produce violations/locks: %v", want)
		}

		// every view, paged to the end
		for _, view := range modelAttempt.MonitorViews {
			expected := 0
			for _, r := range legacy {
				if match[view](r) {
					expected++
				}
			}
			var got []MonitorAttempt
			var total int64
			var first time.Duration
			for page := 1; ; page++ {
				start := time.Now()
				rows, n, err := repo.MonitorPage(ctx, sess.ID, modelAttempt.MonitorFilter{View: view, Page: page, PerPage: 200}, idleBefore)
				if page == 1 {
					first = time.Since(start)
				}
				if err != nil {
					return err
				}
				total = n
				if len(rows) == 0 {
					break
				}
				got = append(got, rows...)
			}
			t.Logf("view %-11s total %5d, first page %v", view, total, first)
			if int(total) != expected || len(got) != expected {
				t.Errorf("view %s: total %d, rows %d, legacy says %d", view, total, len(got), expected)
				continue
			}
			seen := map[string]bool{}
			for i, r := range got {
				if seen[r.UserID] {
					t.Errorf("view %s: %s twice", view, r.UserID)
				}
				seen[r.UserID] = true
				l := byID[r.UserID]
				if !match[view](l) || r.FocusLost+r.FullscreenExit+r.DeviceChanged != l.Violations || r.Idle != idle(l) ||
					r.Answered != l.Answered || r.ItemCount != l.ItemCount {
					t.Errorf("view %s row %d: %+v, legacy %+v", view, i, r, l)
					break
				}
				if i > 0 && !inOrder(view, got[i-1], r) {
					t.Errorf("view %s: rows %d and %d out of order: %+v then %+v", view, i-1, i, got[i-1], r)
					break
				}
			}
		}

		// a violation type narrows the view to participants with that kind
		rows, n, err := repo.MonitorPage(ctx, sess.ID, modelAttempt.MonitorFilter{View: modelAttempt.ViewViolations, ViolationType: modelAttempt.ViolationDeviceChanged, Page: 1, PerPage: 100}, idleBefore)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.DeviceChanged == 0 {
				t.Errorf("device_changed filter returned %+v", r)
			}
		}
		if n == 0 || n >= int64(want["violations"]) {
			t.Errorf("device_changed total = %d of %d violators", n, want["violations"])
		}

		// seat prefix scopes rows and counts
		rows, n, err = repo.MonitorPage(ctx, sess.ID, modelAttempt.MonitorFilter{View: modelAttempt.ViewAll, Seat: "A-1", Page: 1, PerPage: 500}, idleBefore)
		if err != nil {
			return err
		}
		sc, _ := repo.MonitorCounts(ctx, sess.ID, modelAttempt.MonitorFilter{Seat: "A-1"}, idleBefore)
		for _, r := range rows {
			if r.Seat == nil || !strings.HasPrefix(*r.Seat, "A-1") {
				t.Errorf("seat filter returned %v", r.Seat)
			}
		}
		if n == 0 || int(n) != sc["total"] {
			t.Errorf("seat A-1: %d rows, counts total %d", n, sc["total"])
		}

		// watch: given participants only (unknown ids ignored), violators first
		var ids []string
		for _, r := range legacy[:24] {
			ids = append(ids, r.UserID)
		}
		start = time.Now()
		watched, err := repo.MonitorWatch(ctx, sess.ID, append(ids, "0192a000-0000-7000-8000-00000000dead"), idleBefore)
		if err != nil {
			return err
		}
		t.Logf("watch 25 ids: %d rows in %v", len(watched), time.Since(start))
		if len(watched) != 24 {
			t.Errorf("watch returned %d rows, want 24", len(watched))
		}
		for i := 1; i < len(watched); i++ {
			if !inOrder(modelAttempt.ViewViolations, watched[i-1], watched[i]) {
				t.Errorf("watch out of order at %d", i)
			}
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
}

var seatRe = regexp.MustCompile(`[0-9]+`)

// seatLess is the Go twin of seatOrder: no seat last, then letters, then the number.
func seatLess(a, b MonitorAttempt) (less, equal bool) {
	if (a.Seat == nil) != (b.Seat == nil) {
		return a.Seat != nil, false
	}
	if a.Seat == nil {
		return false, true
	}
	la, lb := seatRe.ReplaceAllString(*a.Seat, ""), seatRe.ReplaceAllString(*b.Seat, "")
	if la != lb {
		return la < lb, false
	}
	na, _ := strconv.Atoi(strings.Join(seatRe.FindAllString(*a.Seat, -1), ""))
	nb, _ := strconv.Atoi(strings.Join(seatRe.FindAllString(*b.Seat, -1), ""))
	return na < nb, na == nb
}

// inOrder checks the primary key of each view (ties fall back to seats, which may tie).
func inOrder(view modelAttempt.MonitorView, a, b MonitorAttempt) bool {
	byTime := func(x, y *time.Time, asc bool) (ok, tie bool) {
		switch {
		case x == nil && y == nil:
			return true, true
		case x == nil:
			return !asc, false // NULLS LAST for asc, NULLS LAST for desc too
		case y == nil:
			return true, false
		case x.Equal(*y):
			return true, true
		}
		return x.Before(*y) == asc, false
	}
	seat := func() bool { less, eq := seatLess(a, b); return less || eq }
	switch view {
	case modelAttempt.ViewViolations:
		va, vb := a.FocusLost+a.FullscreenExit+a.DeviceChanged, b.FocusLost+b.FullscreenExit+b.DeviceChanged
		if va != vb {
			return va > vb
		}
		ok, tie := byTime(a.LastViolationAt, b.LastViolationAt, false)
		return ok && (!tie || seat())
	case modelAttempt.ViewIdle:
		ok, tie := byTime(a.LastSeenAt, b.LastSeenAt, true)
		return ok && (!tie || seat())
	case modelAttempt.ViewLocked:
		ok, tie := byTime(a.LockedAt, b.LockedAt, true)
		return ok && (!tie || seat())
	case modelAttempt.ViewSubmitted:
		ok, tie := byTime(a.SubmittedAt, b.SubmittedAt, false)
		return ok && (!tie || seat())
	}
	return seat()
}
