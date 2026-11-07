//go:build integration

// DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//
//	go test -tags integration ./business/attempt -run Integration -v
package attempt

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	dbpg "doexam/pkg/db-pg"
)

var errRollback = errors.New("rollback")

// TestIntegrationOverdueAttemptsCoverTick checks the SQL criterion against every state in
// which tick changes an attempt, on a real attempt whose rows are rewritten inside a
// transaction that is always rolled back.
func TestIntegrationOverdueAttemptsCoverTick(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	defer dbpg.Close(db)

	// an attempt with at least two sections (development connects as the table owner)
	var a modelAttempt.AttemptEntity
	err = db.Raw(`SELECT a.* FROM attempts a WHERE (SELECT count(*) FROM attempt_sections s WHERE s.attempt_id = a.id) >= 1
		ORDER BY a.started_at DESC LIMIT 1`).Scan(&a).Error
	if err != nil || a.ID == "" {
		t.Skip("no attempt with sections in the database")
	}

	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-syncGrace)
	past, future := cutoff.Add(-time.Second), cutoff.Add(time.Second)
	type state struct {
		status        modelAttempt.Status
		deadline      time.Time
		sectionStart  bool
		sectionClosed bool
		sectionDL     *time.Time
	}
	tests := []struct {
		name string
		st   state
		want bool // tick would change the attempt
	}{
		{"attempt deadline passed", state{status: modelAttempt.StatusInProgress, deadline: past}, true},
		{"locked attempt, deadline passed", state{status: modelAttempt.StatusLocked, deadline: past}, true},
		{"attempt deadline within grace", state{status: modelAttempt.StatusInProgress, deadline: future}, false},
		{"open section deadline passed", state{status: modelAttempt.StatusInProgress, deadline: future.Add(time.Hour), sectionStart: true, sectionDL: &past}, true},
		{"open section within grace", state{status: modelAttempt.StatusInProgress, deadline: future.Add(time.Hour), sectionStart: true, sectionDL: &future}, false},
		{"closed section, deadline passed", state{status: modelAttempt.StatusInProgress, deadline: future.Add(time.Hour), sectionStart: true, sectionClosed: true, sectionDL: &past}, false},
		{"section not started, deadline passed", state{status: modelAttempt.StatusInProgress, deadline: future.Add(time.Hour), sectionDL: &past}, false},
		{"submitted attempt, deadline passed", state{status: modelAttempt.StatusSubmitted, deadline: past, sectionStart: true, sectionDL: &past}, false},
	}
	repo := NewAttemptRepo(db)
	tx := dbpg.NewTxManager(db)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tx.WithTenant(context.Background(), a.TenantID, func(ctx context.Context) error {
				conn := dbpg.Conn(ctx, db)
				if err := conn.Exec(`UPDATE attempts SET status = ?, deadline_at = ? WHERE id = ?`, tt.st.status, tt.st.deadline, a.ID).Error; err != nil {
					return err
				}
				// every section neutral (not started), then the first one in the case's state
				if err := conn.Exec(`UPDATE attempt_sections SET started_at = NULL, closed_at = NULL, deadline_at = NULL WHERE attempt_id = ?`, a.ID).Error; err != nil {
					return err
				}
				var started, closed any
				if tt.st.sectionStart {
					started = cutoff.Add(-time.Hour)
				}
				if tt.st.sectionClosed {
					closed = cutoff.Add(-time.Minute)
				}
				var dl any
				if tt.st.sectionDL != nil {
					dl = *tt.st.sectionDL
				}
				if err := conn.Exec(`UPDATE attempt_sections SET started_at = ?, closed_at = ?, deadline_at = ?
					WHERE id = (SELECT id FROM attempt_sections WHERE attempt_id = ? ORDER BY sort_order LIMIT 1)`,
					started, closed, dl, a.ID).Error; err != nil {
					return err
				}
				got, err := repo.OverdueAttempts(ctx, a.SessionID, cutoff)
				if err != nil {
					return err
				}
				found := false
				for _, g := range got {
					found = found || g.ID == a.ID
				}
				if found != tt.want {
					t.Fatalf("selected = %v, want %v", found, tt.want)
				}
				return errRollback
			})
			if !errors.Is(err, errRollback) {
				t.Fatal(err)
			}
		})
	}

	// a row another transaction holds (a participant's sync) is skipped, not waited for
	t.Run("skips locked rows", func(t *testing.T) {
		// the row must be committed as overdue (else it is not a candidate at all, and the
		// test would pass without SKIP LOCKED); its original values are restored afterwards
		var orig struct {
			Status     string
			DeadlineAt time.Time
		}
		if err := db.Raw(`SELECT status, deadline_at FROM attempts WHERE id = ?`, a.ID).Scan(&orig).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Exec(`UPDATE attempts SET status = ?, deadline_at = ? WHERE id = ?`, orig.Status, orig.DeadlineAt, a.ID).Error; err != nil {
				t.Errorf("restore attempt %s: %v", a.ID, err)
			}
		})
		if err := db.Exec(`UPDATE attempts SET status = 'in_progress', deadline_at = ? WHERE id = ?`, past, a.ID).Error; err != nil {
			t.Fatal(err)
		}
		// without the other transaction's lock the row is selected
		err := tx.WithTenant(context.Background(), a.TenantID, func(ctx context.Context) error {
			got, err := repo.OverdueAttempts(ctx, a.SessionID, cutoff)
			if err != nil {
				return err
			}
			for _, g := range got {
				if g.ID == a.ID {
					return errRollback
				}
			}
			return errors.New("committed overdue attempt not selected")
		})
		if !errors.Is(err, errRollback) {
			t.Fatal(err)
		}

		holder := db.Begin() // a participant's sync holding the attempt
		defer holder.Rollback()
		if err := holder.Exec(`SELECT id FROM attempts WHERE id = ? FOR UPDATE`, a.ID).Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		start := time.Now()
		err = tx.WithTenant(ctx, a.TenantID, func(ctx context.Context) error {
			got, err := repo.OverdueAttempts(ctx, a.SessionID, cutoff)
			if err != nil {
				return err
			}
			for _, g := range got {
				if g.ID == a.ID {
					t.Fatal("a row locked by another transaction was returned")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("waited or failed instead of skipping: %v", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("took %v: the query waited for the lock", d)
		}
	})
}
