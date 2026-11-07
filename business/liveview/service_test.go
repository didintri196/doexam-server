package liveview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelLive "doexam/models/liveview"
	"doexam/pkg/errr"
	"doexam/pkg/pglisten"
)

const (
	tenant   = "00000000-0000-0000-0000-0000000000aa"
	sessID   = "00000000-0000-0000-0000-000000000001"
	attID    = "00000000-0000-0000-0000-000000000002"
	userID   = "00000000-0000-0000-0000-000000000003" // the participant
	proctor1 = "00000000-0000-0000-0000-000000000011"
	proctor2 = "00000000-0000-0000-0000-000000000012"
	proctor3 = "00000000-0000-0000-0000-000000000013"
	otherSes = "00000000-0000-0000-0000-000000000009"
)

// fakeStore is an in-memory LiveViewStorer; Notify wakes the shared listener like a commit would.
type fakeStore struct {
	mu       sync.Mutex
	sessions map[string]SessionRow
	proctors map[string]bool // sessionID|userID
	attempts map[string]AttemptRow
	views    map[string]*modelLive.ViewEntity
	signals  []modelLive.SignalEntity
	audits   []string
	waker    *pglisten.Listener
}

func newFake() *fakeStore {
	return &fakeStore{
		sessions: map[string]SessionRow{sessID: {ID: sessID, Status: "open", RequireCamera: true, RequireScreen: true}},
		proctors: map[string]bool{sessID + "|" + proctor1: true, sessID + "|" + proctor2: true, sessID + "|" + proctor3: true},
		attempts: map[string]AttemptRow{attID: {ID: attID, SessionID: sessID, UserID: userID, Status: "in_progress"}},
		views:    map[string]*modelLive.ViewEntity{},
		waker:    pglisten.New("", "test", slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

func (f *fakeStore) FindSession(_ context.Context, id string) (SessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return s, errr.ErrNotFound
	}
	return s, nil
}
func (f *fakeStore) IsProctor(_ context.Context, sessionID, uid string) (bool, error) {
	return f.proctors[sessionID+"|"+uid], nil
}
func (f *fakeStore) FindAttempt(_ context.Context, id string) (AttemptRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.attempts[id]
	if !ok {
		return a, errr.ErrNotFound
	}
	return a, nil
}
func (f *fakeStore) ActiveViews(_ context.Context, attemptID string, now time.Time) ([]modelLive.ViewEntity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []modelLive.ViewEntity
	for _, v := range f.views {
		if v.AttemptID == attemptID && v.Active(now) {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (f *fakeStore) CountProctorViews(_ context.Context, pid string, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, v := range f.views {
		if v.ProctorID == pid && v.Active(now) {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CreateView(_ context.Context, v *modelLive.ViewEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *v
	f.views[v.ID] = &c
	return nil
}
func (f *fakeStore) FindView(_ context.Context, id string) (modelLive.ViewEntity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.views[id]
	if !ok {
		return modelLive.ViewEntity{}, errr.ErrNotFound
	}
	return *v, nil
}
func (f *fakeStore) EndView(_ context.Context, id string, reason modelLive.EndReason, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.views[id]
	if v == nil || v.EndedAt != nil {
		return false, nil
	}
	v.EndedAt, v.EndReason = &now, &reason
	return true, nil
}
func (f *fakeStore) AddSignal(_ context.Context, s *modelLive.SignalEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.ID = int64(len(f.signals) + 1)
	f.signals = append(f.signals, *s)
	return nil
}
func (f *fakeStore) Notify(_ context.Context, key string) error { f.waker.Wake(key); return nil }
func (f *fakeStore) ProctorSignals(_ context.Context, viewID string, after int64, limit int) ([]modelLive.SignalEntity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []modelLive.SignalEntity
	for _, s := range f.signals {
		if s.ViewID == viewID && s.Recipient == modelLive.RecipientProctor && s.ID > after && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) ParticipantSignals(_ context.Context, attemptID string, after int64, now, since time.Time, limit int) ([]modelLive.SignalEntity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []modelLive.SignalEntity
	for _, s := range f.signals {
		v := f.views[s.ViewID]
		if v.AttemptID == attemptID && s.Recipient == modelLive.RecipientParticipant && s.ID > after &&
			(v.Active(now) || s.CreatedAt.After(since)) && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) PurgeSignals(context.Context, string, time.Time) error { return nil }
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, l.Action)
	return nil
}

type noTx struct{}

func (noTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup() (*LiveViewService, *fakeStore, *clock) {
	f := newFake()
	svc := NewLiveViewService(f, noTx{}, f.waker, json.RawMessage(`[{"urls":"stun:stun.lab:3478"}]`), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c := &clock{t: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)}
	svc.now = c.now
	return svc, f, c
}

func proctorP(id string) modelAuth.Principal {
	return modelAuth.Principal{TenantID: tenant, UserID: id, Permissions: []string{modelAuth.PermSessionProctor}}
}

func participantP() modelAuth.Principal {
	return modelAuth.Principal{TenantID: tenant, UserID: userID}
}

func wantMsg(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("err = %v, want %q", err, msg)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := setup()

	v, err := svc.Open(ctx, proctorP(proctor1), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Camera || !v.Screen || string(v.ICEServers) == "[]" || v.ExpiresAt.Sub(v.StartedAt) != ViewDuration {
		t.Fatalf("view %+v", v)
	}
	poll, err := svc.ParticipantPoll(ctx, participantP(), attID, 0, 0)
	if err != nil || len(poll.Signals) != 1 || poll.Signals[0].Kind != modelLive.KindViewRequest || poll.Cursor != poll.Signals[0].ID {
		t.Fatalf("participant poll %+v %v", poll, err)
	}
	var req map[string]any
	_ = json.Unmarshal(poll.Signals[0].Payload, &req)
	if req["camera"] != true || req["ice_servers"] == nil {
		t.Fatalf("view_request payload %v", req)
	}

	// reopening by the same proctor replaces the view (the old one gets "end")
	v2, err := svc.Open(ctx, proctorP(proctor1), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{})
	if err != nil || v2.ID == v.ID {
		t.Fatalf("reopen: %v", err)
	}
	if r := f.views[v.ID].EndReason; r == nil || *r != modelLive.EndReplaced {
		t.Fatalf("old view reason %v", r)
	}
	// two other proctors at most per participant
	if _, err := svc.Open(ctx, proctorP(proctor2), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Open(ctx, proctorP(proctor3), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{})
	wantMsg(t, err, errTooMany.Error())

	want := []string{modelAudit.ActionLiveViewStart, modelAudit.ActionLiveViewEnd, modelAudit.ActionLiveViewStart, modelAudit.ActionLiveViewStart}
	if len(f.audits) != len(want) {
		t.Fatalf("audits %v", f.audits)
	}
	for i := range want {
		if f.audits[i] != want[i] {
			t.Fatalf("audits %v, want %v", f.audits, want)
		}
	}
}

func TestOpenRejects(t *testing.T) {
	ctx := context.Background()
	open := func(svc *LiveViewService, p modelAuth.Principal, session, attempt string) error {
		_, err := svc.Open(ctx, p, session, modelLive.OpenInput{AttemptID: attempt}, modelAudit.Client{})
		return err
	}
	t.Run("no permission", func(t *testing.T) {
		svc, _, _ := setup()
		if err := open(svc, participantP(), sessID, attID); !errors.Is(err, errr.ErrForbidden) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("proctor of another session", func(t *testing.T) {
		svc, _, _ := setup()
		if err := open(svc, proctorP("00000000-0000-0000-0000-000000000099"), sessID, attID); !errors.Is(err, errr.ErrNotFound) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("sharing not required", func(t *testing.T) {
		svc, f, _ := setup()
		f.sessions[sessID] = SessionRow{ID: sessID, Status: "open"}
		wantMsg(t, open(svc, proctorP(proctor1), sessID, attID), errSharingOff.Error())
	})
	t.Run("attempt of another session", func(t *testing.T) {
		svc, f, _ := setup()
		f.sessions[otherSes] = f.sessions[sessID]
		f.proctors[otherSes+"|"+proctor1] = true
		if err := open(svc, proctorP(proctor1), otherSes, attID); !errors.Is(err, errr.ErrNotFound) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("submitted attempt", func(t *testing.T) {
		svc, f, _ := setup()
		a := f.attempts[attID]
		a.Status = "submitted"
		f.attempts[attID] = a
		wantMsg(t, open(svc, proctorP(proctor1), sessID, attID), errNotRunning.Error())
	})
	t.Run("own limit", func(t *testing.T) {
		svc, f, _ := setup()
		for i := 0; i < MaxViewsPerProctor; i++ {
			id := "00000000-0000-0000-0000-00000000010" + string(rune('0'+i))
			f.attempts[id] = AttemptRow{ID: id, SessionID: sessID, UserID: id, Status: "in_progress"}
			if err := open(svc, proctorP(proctor1), sessID, id); err != nil {
				t.Fatal(err)
			}
		}
		wantMsg(t, open(svc, proctorP(proctor1), sessID, attID), errOwnLimit.Error())
	})
	t.Run("bad attempt id", func(t *testing.T) {
		svc, _, _ := setup()
		if err := open(svc, proctorP(proctor1), sessID, "x"); !errors.Is(err, errr.ErrInvalidInput) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestSignalingRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := setup()
	v, err := svc.Open(ctx, proctorP(proctor1), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}

	// the proctor long-polls; the participant's offer wakes it
	type res struct {
		d   modelLive.PollDTO
		err error
	}
	got := make(chan res, 1)
	go func() {
		d, err := svc.ProctorPoll(ctx, proctorP(proctor1), sessID, v.ID, 0, 5*time.Second, modelAudit.Client{})
		got <- res{d, err}
	}()
	time.Sleep(50 * time.Millisecond)
	offer := modelLive.SignalInput{ViewID: v.ID, Kind: modelLive.KindOffer, Payload: json.RawMessage(`{"sdp":"v=0"}`)}
	if err := svc.ParticipantSignal(ctx, participantP(), attID, offer, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || len(r.d.Signals) != 1 || r.d.Signals[0].Kind != modelLive.KindOffer || r.d.Ended {
			t.Fatalf("proctor poll %+v %v", r.d, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long-poll was not woken")
	}

	// answer back; another proctor cannot use this view; a participant cannot offer to another's view
	ans := modelLive.SignalInput{Kind: modelLive.KindAnswer, Payload: json.RawMessage(`{"sdp":"v=0"}`)}
	if err := svc.ProctorSignal(ctx, proctorP(proctor1), sessID, v.ID, ans); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProctorSignal(ctx, proctorP(proctor2), sessID, v.ID, ans); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("other proctor: %v", err)
	}
	if err := svc.ProctorSignal(ctx, proctorP(proctor1), sessID, v.ID, modelLive.SignalInput{Kind: modelLive.KindOffer}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("proctor offer: %v", err)
	}
	intruder := modelAuth.Principal{TenantID: tenant, UserID: "00000000-0000-0000-0000-000000000077"}
	if err := svc.ParticipantSignal(ctx, intruder, attID, offer, modelAudit.Client{}); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("intruder: %v", err)
	}
	if err := svc.ParticipantSignal(ctx, participantP(), attID, modelLive.SignalInput{ViewID: v.ID, Kind: modelLive.KindICE, Payload: json.RawMessage(`not json`)}, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("bad payload: %v", err)
	}

	// the participant ends it: the proctor sees "end" and Ended
	if err := svc.ParticipantSignal(ctx, participantP(), attID, modelLive.SignalInput{ViewID: v.ID, Kind: modelLive.KindEnd}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.ProctorPoll(ctx, proctorP(proctor1), sessID, v.ID, 1, 0, modelAudit.Client{})
	if err != nil || !d.Ended || d.Signals[len(d.Signals)-1].Kind != modelLive.KindEnd {
		t.Fatalf("after end %+v %v", d, err)
	}
	if err := svc.ProctorSignal(ctx, proctorP(proctor1), sessID, v.ID, ans); err == nil || err.Error() != errViewEnded.Error() {
		t.Fatalf("signal after end: %v", err)
	}
	if r := f.views[v.ID].EndReason; r == nil || *r != modelLive.EndParticipant {
		t.Fatalf("reason %v", r)
	}
}

func TestExpiryAndTimeout(t *testing.T) {
	ctx := context.Background()
	svc, f, c := setup()
	v, err := svc.Open(ctx, proctorP(proctor1), sessID, modelLive.OpenInput{AttemptID: attID}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	// nothing new: returns after the wait with the same cursor
	start := time.Now()
	d, err := svc.ProctorPoll(ctx, proctorP(proctor1), sessID, v.ID, 0, 100*time.Millisecond, modelAudit.Client{})
	if err != nil || len(d.Signals) != 0 || d.Ended || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("timeout poll %+v %v", d, err)
	}

	c.add(ViewDuration + time.Second)
	d, err = svc.ProctorPoll(ctx, proctorP(proctor1), sessID, v.ID, 0, time.Second, modelAudit.Client{})
	if err != nil || !d.Ended {
		t.Fatalf("expired poll %+v %v", d, err)
	}
	if r := f.views[v.ID].EndReason; r == nil || *r != modelLive.EndExpired {
		t.Fatalf("reason %v", r)
	}
	// the participant still receives "end" within the grace period, then nothing
	p, _ := svc.ParticipantPoll(ctx, participantP(), attID, 1, 0)
	if len(p.Signals) != 1 || p.Signals[0].Kind != modelLive.KindEnd {
		t.Fatalf("participant end %+v", p)
	}
	c.add(2 * endGrace)
	p, _ = svc.ParticipantPoll(ctx, participantP(), attID, 0, 0)
	if len(p.Signals) != 0 {
		t.Fatalf("stale signals delivered: %+v", p)
	}
	// polling a session that does not require sharing is refused
	f.sessions[sessID] = SessionRow{ID: sessID, Status: "open"}
	if _, err := svc.ParticipantPoll(ctx, participantP(), attID, 0, 0); err == nil || err.Error() != errSharingOff.Error() {
		t.Fatalf("sharing off: %v", err)
	}
}
