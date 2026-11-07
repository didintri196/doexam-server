//go:build integration

// DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//
//	go test -tags integration ./business/liveview -run Integration -v
package liveview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelLive "doexam/models/liveview"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/pglisten"
)

var errRollback = errors.New("rollback")

// TestIntegrationRepo runs the repository against a real attempt inside a tenant transaction
// (RLS on) that is always rolled back.
func TestIntegrationRepo(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	defer dbpg.Close(db)
	var a struct{ ID, SessionID, UserID, TenantID string }
	if err := db.Raw(`SELECT id, session_id, user_id, tenant_id FROM attempts ORDER BY started_at DESC LIMIT 1`).Scan(&a).Error; err != nil || a.ID == "" {
		t.Skip("no attempt in the database")
	}
	var proctorID string
	db.Raw(`SELECT id FROM users WHERE tenant_id = ? AND id <> ? LIMIT 1`, a.TenantID, a.UserID).Scan(&proctorID)

	repo := NewLiveViewRepo(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	err = dbpg.NewTxManager(db).WithTenant(context.Background(), a.TenantID, func(ctx context.Context) error {
		if err := dbpg.Conn(ctx, db).Exec(`UPDATE exam_sessions SET require_camera = true WHERE id = ?`, a.SessionID).Error; err != nil {
			return err
		}
		sess, err := repo.FindSession(ctx, a.SessionID)
		if err != nil || !sess.RequireCamera {
			t.Fatalf("session %+v %v", sess, err)
		}
		att, err := repo.FindAttempt(ctx, a.ID)
		if err != nil || att.UserID != a.UserID {
			t.Fatalf("attempt %+v %v", att, err)
		}

		live := modelLive.ViewEntity{ID: uuid.NewString(), TenantID: a.TenantID, SessionID: a.SessionID, AttemptID: a.ID,
			ProctorID: proctorID, StartedAt: now, ExpiresAt: now.Add(ViewDuration)}
		old := live
		old.ID, old.StartedAt, old.ExpiresAt = uuid.NewString(), now.Add(-time.Hour), now.Add(-50*time.Minute)
		for _, v := range []*modelLive.ViewEntity{&live, &old} {
			if err := repo.CreateView(ctx, v); err != nil {
				return err
			}
		}
		active, err := repo.ActiveViews(ctx, a.ID, now)
		if err != nil || len(active) != 1 || active[0].ID != live.ID {
			t.Fatalf("active %+v %v", active, err)
		}
		if n, err := repo.CountProctorViews(ctx, proctorID, now); err != nil || n != 1 {
			t.Fatalf("proctor views %d %v", n, err)
		}

		sig := func(v modelLive.ViewEntity, to modelLive.Recipient, kind modelLive.Kind, at time.Time) int64 {
			s := modelLive.SignalEntity{TenantID: a.TenantID, ViewID: v.ID, Recipient: to, Kind: kind, Payload: json.RawMessage(`{"x":1}`), CreatedAt: at}
			if err := repo.AddSignal(ctx, &s); err != nil {
				t.Fatal(err)
			}
			return s.ID
		}
		req := sig(live, modelLive.RecipientParticipant, modelLive.KindViewRequest, now)
		sig(live, modelLive.RecipientProctor, modelLive.KindOffer, now)
		sig(old, modelLive.RecipientParticipant, modelLive.KindViewRequest, now.Add(-time.Hour)) // stale view: hidden
		if req == 0 {
			t.Fatal("signal id not returned")
		}

		got, err := repo.ParticipantSignals(ctx, a.ID, 0, now, now.Add(-endGrace), 50)
		if err != nil || len(got) != 1 || got[0].ID != req || got[0].Kind != modelLive.KindViewRequest {
			t.Fatalf("participant signals %+v %v", got, err)
		}
		if got, _ := repo.ParticipantSignals(ctx, a.ID, req, now, now.Add(-endGrace), 50); len(got) != 0 {
			t.Fatalf("cursor ignored: %+v", got)
		}
		if got, err := repo.ProctorSignals(ctx, live.ID, 0, 50); err != nil || len(got) != 1 || got[0].Kind != modelLive.KindOffer {
			t.Fatalf("proctor signals %+v %v", got, err)
		}

		ended, err := repo.EndView(ctx, live.ID, modelLive.EndProctor, now)
		if err != nil || !ended {
			t.Fatalf("end %v %v", ended, err)
		}
		if again, _ := repo.EndView(ctx, live.ID, modelLive.EndProctor, now); again {
			t.Fatal("a view ends only once")
		}
		end := sig(live, modelLive.RecipientParticipant, modelLive.KindEnd, now)
		// ended view: only messages newer than the grace cutoff are delivered
		if got, _ := repo.ParticipantSignals(ctx, a.ID, req, now, now.Add(-endGrace), 50); len(got) != 1 || got[0].ID != end {
			t.Fatalf("end not delivered: %+v", got)
		}
		if err := repo.Notify(ctx, "a:"+a.ID); err != nil {
			return err
		}
		if err := repo.PurgeSignals(ctx, a.ID, now.Add(-time.Minute)); err != nil {
			return err
		}
		var left int64
		dbpg.Conn(ctx, db).Model(&modelLive.SignalEntity{}).Where("view_id = ?", old.ID).Count(&left)
		if left != 0 {
			t.Fatalf("old signals not purged: %d", left)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
}

// TestIntegrationTwoReplicas runs the whole signaling flow through committed transactions with
// two listeners (as two server replicas): the proctor polls on one, the participant posts its
// offer through the other, and NOTIFY wakes the poll. Rows and the session flag are restored.
func TestIntegrationTwoReplicas(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbpg.Close(db) }) // runs after the cleanup below (LIFO)
	var a struct {
		ID, SessionID, UserID, TenantID string
		RequireCamera                   bool
	}
	if err := db.Raw(`SELECT a.id, a.session_id, a.user_id, a.tenant_id, es.require_camera FROM attempts a
		JOIN exam_sessions es ON es.id = a.session_id WHERE a.status = 'in_progress' ORDER BY a.started_at DESC LIMIT 1`).Scan(&a).Error; err != nil || a.ID == "" {
		t.Skip("no running attempt in the database")
	}
	db.Exec(`UPDATE exam_sessions SET require_camera = true WHERE id = ?`, a.SessionID)
	t.Cleanup(func() {
		db.Exec(`DELETE FROM live_views WHERE attempt_id = ?`, a.ID)
		db.Exec(`UPDATE exam_sessions SET require_camera = ? WHERE id = ?`, a.RequireCamera, a.SessionID)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	tx := dbpg.NewTxManager(db)
	replica := func() *LiveViewService {
		l := pglisten.New(dsn, NotifyChannel, quiet)
		ready, unsub := l.Subscribe("ready")
		go l.Run(ctx)
		<-ready // LISTEN is in place (Run wakes every subscriber once)
		unsub()
		return NewLiveViewService(NewLiveViewRepo(db), tx, l, nil, quiet)
	}
	r1, r2 := replica(), replica()
	proctor := modelAuth.Principal{TenantID: a.TenantID, UserID: a.UserID, Permissions: []string{modelAuth.PermSessionManage}}
	participant := modelAuth.Principal{TenantID: a.TenantID, UserID: a.UserID}

	v, err := r1.Open(ctx, proctor, a.SessionID, modelLive.OpenInput{AttemptID: a.ID}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	req, err := r2.ParticipantPoll(ctx, participant, a.ID, 0, 5*time.Second)
	if err != nil || len(req.Signals) == 0 || req.Signals[0].Kind != modelLive.KindViewRequest {
		t.Fatalf("view request %+v %v", req, err)
	}

	got := make(chan modelLive.PollDTO, 1)
	go func() {
		d, err := r1.ProctorPoll(ctx, proctor, a.SessionID, v.ID, 0, 10*time.Second, modelAudit.Client{})
		if err != nil {
			t.Error(err)
		}
		got <- d
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := r2.ParticipantSignal(ctx, participant, a.ID, modelLive.SignalInput{ViewID: v.ID, Kind: modelLive.KindOffer, Payload: json.RawMessage(`{"sdp":"v=0"}`)}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if len(d.Signals) != 1 || d.Signals[0].Kind != modelLive.KindOffer {
			t.Fatalf("proctor poll %+v", d)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("woken after %v: NOTIFY did not cross replicas", time.Since(start))
		}
	case <-ctx.Done():
		t.Fatal("proctor poll never returned")
	}
	if err := r2.Close(ctx, proctor, a.SessionID, v.ID, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	var audits int64
	db.Raw(`SELECT count(*) FROM audit_logs WHERE entity_id = ? AND action IN (?, ?)`, v.ID, modelAudit.ActionLiveViewStart, modelAudit.ActionLiveViewEnd).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit entries %d, want start and end", audits)
	}
}
