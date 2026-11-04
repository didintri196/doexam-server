package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSession "doexam/models/session"
	"doexam/pkg/errr"
)

// announceStore keeps announcements in memory on top of the session fake.
type announceStore struct {
	*fakeStore
	anns       []modelSession.AnnouncementEntity
	recipients map[string]map[string]*time.Time // announcement → user → read_at
	running    []string
	lastReads  int // LastAnnouncementChange calls
}

func (f *announceStore) LastAnnouncementChange(_ context.Context, sessionID string) (time.Time, error) {
	f.lastReads++
	var t time.Time
	for _, a := range f.anns {
		if a.SessionID != sessionID {
			continue
		}
		if a.CreatedAt.After(t) {
			t = a.CreatedAt
		}
		if a.RetractedAt != nil && a.RetractedAt.After(t) {
			t = *a.RetractedAt
		}
	}
	return t, nil
}
func (f *announceStore) MarkAnnouncementRead(_ context.Context, aid, uid string, at time.Time) (bool, error) {
	r, ok := f.recipients[aid]
	if !ok {
		return false, nil
	}
	if read, ok := r[uid]; !ok || read != nil {
		return false, nil
	}
	r[uid] = &at
	return true, nil
}
func (f *announceStore) CountRecentAnnouncements(_ context.Context, sessionID string, since time.Time) (int64, error) {
	var n int64
	for _, a := range f.anns {
		if a.SessionID == sessionID && !a.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}
func (f *announceStore) CreateAnnouncement(_ context.Context, a *modelSession.AnnouncementEntity) error {
	f.anns = append(f.anns, *a)
	return nil
}
func (f *announceStore) AddAnnouncementRecipients(_ context.Context, _, aid, _ string, userIDs []string) (int64, error) {
	r := map[string]*time.Time{}
	for uid := range f.participants {
		if userIDs == nil || slices.Contains(userIDs, uid) {
			r[uid] = nil
		}
	}
	f.recipients[aid] = r
	return int64(len(r)), nil
}
func (f *announceStore) SetAnnouncementRecipients(_ context.Context, aid string, n int) error {
	f.find(aid).Recipients = n
	return nil
}
func (f *announceStore) ListAnnouncements(_ context.Context, sessionID string) ([]modelSession.AnnouncementDTO, error) {
	var out []modelSession.AnnouncementDTO
	for _, a := range slices.Backward(f.anns) {
		read := 0
		for _, at := range f.recipients[a.ID] {
			if at != nil {
				read++
			}
		}
		out = append(out, modelSession.AnnouncementDTO{ID: a.ID, Body: a.Body, Priority: a.Priority, Recipients: a.Recipients, Read: read, RetractedAt: a.RetractedAt})
	}
	return out, nil
}
func (f *announceStore) UnreadImportant(_ context.Context, _ string, before time.Time) ([]string, error) {
	out := []string{}
	for _, a := range f.anns {
		if a.Priority != modelSession.PriorityImportant || a.RetractedAt != nil || !a.CreatedAt.Before(before) {
			continue
		}
		for uid, at := range f.recipients[a.ID] {
			if at == nil && !slices.Contains(out, uid) {
				out = append(out, uid)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}
func (f *announceStore) FindAnnouncement(_ context.Context, id string) (modelSession.AnnouncementEntity, error) {
	if a := f.find(id); a != nil {
		return *a, nil
	}
	return modelSession.AnnouncementEntity{}, errr.ErrNotFound
}
func (f *announceStore) RetractAnnouncement(_ context.Context, id string, at time.Time) error {
	f.find(id).RetractedAt = &at
	return nil
}
func (f *announceStore) MyAnnouncements(_ context.Context, sessionID, uid string) ([]modelSession.MyAnnouncementDTO, error) {
	out := []modelSession.MyAnnouncementDTO{}
	for _, a := range slices.Backward(f.anns) {
		at, ok := f.recipients[a.ID][uid]
		if !ok {
			continue
		}
		body := a.Body
		if a.RetractedAt != nil {
			body = ""
		}
		out = append(out, modelSession.MyAnnouncementDTO{ID: a.ID, SessionID: sessionID, Body: body, Priority: a.Priority, ReadAt: at, RetractedAt: a.RetractedAt})
	}
	return out, nil
}
func (f *announceStore) IsAnnouncementRecipient(_ context.Context, aid, uid string) (bool, error) {
	_, ok := f.recipients[aid][uid]
	return ok, nil
}
func (f *announceStore) RunningSessionIDs(context.Context) ([]string, error) { return f.running, nil }
func (f *announceStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, l.Action)
	return nil
}

func (f *announceStore) find(id string) *modelSession.AnnouncementEntity {
	for i := range f.anns {
		if f.anns[i].ID == id {
			return &f.anns[i]
		}
	}
	return nil
}

// announceFixture is a session with participants u1 and u2, and a clock tests can move.
func announceFixture(status modelSession.Status) (*SessionService, *announceStore, *time.Time) {
	_, base := newFixture(status)
	base.participants[u1] = modelSession.ParticipantEntity{SessionID: sessionID, UserID: u1}
	base.participants[u2] = modelSession.ParticipantEntity{SessionID: sessionID, UserID: u2}
	store := &announceStore{fakeStore: base, recipients: map[string]map[string]*time.Time{}}
	board := NewAnnouncementBoard(store)
	svc := NewSessionService(store, fakeTx{}, &fakeFinalizer{}, fakeHasher{}, fakeSigner{}, board, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := now.Add(10 * time.Minute)
	svc.now = func() time.Time { return clock }
	board.now = svc.now
	return svc, store, &clock
}

func TestAnnounce(t *testing.T) {
	svc, store, clock := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()

	before, err := svc.MyAnnouncements(ctx, studentP, sessionID)
	if err != nil || before.Cursor != "" || len(before.Announcements) != 0 {
		t.Fatalf("before: %+v, %v", before, err)
	}

	res, err := svc.Announce(ctx, proctorP, sessionID, modelSession.AnnouncementInput{Body: "  Waktu tersisa 10 menit.  "}, modelAudit.Client{})
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if res.Recipients != 2 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	a := store.anns[0]
	if a.Body != "Waktu tersisa 10 menit." || a.Priority != modelSession.PriorityInfo || a.Target != modelSession.TargetAll || a.SenderID != proctorID || a.Recipients != 2 {
		t.Fatalf("stored = %+v", a)
	}
	if !slices.Contains(store.audits, modelAudit.ActionAnnouncementSend) {
		t.Fatalf("audits = %v", store.audits)
	}

	mine, err := svc.MyAnnouncements(ctx, studentP, sessionID)
	if err != nil || len(mine.Announcements) != 1 || mine.Cursor == "" || mine.Cursor == before.Cursor {
		t.Fatalf("after: %+v, %v", mine, err)
	}

	// selected: a non-participant is skipped, not an error
	*clock = clock.Add(time.Second)
	res, err = svc.Announce(ctx, managerP, sessionID, modelSession.AnnouncementInput{
		Body: "Kursi A-12 harap angkat tangan.", Priority: modelSession.PriorityImportant,
		Target: modelSession.TargetSelected, UserIDs: []string{u1, u1, u3},
	}, modelAudit.Client{})
	if err != nil || res.Recipients != 1 || res.Skipped != 1 {
		t.Fatalf("selected: %+v, %v", res, err)
	}
	other := modelAuth.Principal{TenantID: tenantID, UserID: u2, Permissions: []string{modelAuth.PermAttemptTake}}
	if mine, _ := svc.MyAnnouncements(ctx, other, sessionID); len(mine.Announcements) != 1 {
		t.Fatalf("u2 must not see the announcement for u1: %+v", mine.Announcements)
	}
	cursor := mine.Cursor
	if next, _ := svc.MyAnnouncements(ctx, studentP, sessionID); next.Cursor == cursor || len(next.Announcements) != 2 {
		t.Fatalf("cursor must move with every send: %q → %q", cursor, next.Cursor)
	}
}

func TestAnnounceRejected(t *testing.T) {
	long := strings.Repeat("é", modelSession.MaxAnnouncementLen+1)
	tests := []struct {
		name    string
		status  modelSession.Status
		p       modelAuth.Principal
		in      modelSession.AnnouncementInput
		wantErr error
		field   string
	}{
		{name: "empty body", status: modelSession.StatusOpen, p: proctorP, in: modelSession.AnnouncementInput{Body: "  "}, wantErr: errr.ErrInvalidInput, field: "body"},
		{name: "long body", status: modelSession.StatusOpen, p: proctorP, in: modelSession.AnnouncementInput{Body: long}, wantErr: errr.ErrInvalidInput, field: "body"},
		{name: "bad priority", status: modelSession.StatusOpen, p: proctorP, in: modelSession.AnnouncementInput{Body: "x", Priority: "urgent"}, wantErr: errr.ErrInvalidInput, field: "priority"},
		{name: "selected without ids", status: modelSession.StatusOpen, p: proctorP, in: modelSession.AnnouncementInput{Body: "x", Target: modelSession.TargetSelected}, wantErr: errr.ErrInvalidInput, field: "user_ids"},
		{name: "selected bad id", status: modelSession.StatusOpen, p: proctorP, in: modelSession.AnnouncementInput{Body: "x", Target: modelSession.TargetSelected, UserIDs: []string{"nope"}}, wantErr: errr.ErrInvalidInput, field: "user_ids"},
		{name: "unassigned proctor", status: modelSession.StatusOpen, p: otherP, in: modelSession.AnnouncementInput{Body: "x"}, wantErr: errr.ErrNotFound},
		{name: "participant", status: modelSession.StatusOpen, p: studentP, in: modelSession.AnnouncementInput{Body: "x"}, wantErr: errr.ErrForbidden},
		{name: "closed session", status: modelSession.StatusClosed, p: proctorP, in: modelSession.AnnouncementInput{Body: "x"}, wantErr: errr.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, _ := announceFixture(tt.status)
			_, err := svc.Announce(context.Background(), tt.p, sessionID, tt.in, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			var ve *errr.ValidationError
			if tt.field != "" && (!errors.As(err, &ve) || ve.Fields[tt.field] == "") {
				t.Fatalf("err = %v, want field %s", err, tt.field)
			}
			if len(store.anns) != 0 {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

func TestAnnounceRateLimit(t *testing.T) {
	svc, _, clock := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()
	for i := range modelSession.MaxAnnouncementsPerMinute {
		if _, err := svc.Announce(ctx, proctorP, sessionID, modelSession.AnnouncementInput{Body: "x"}, modelAudit.Client{}); err != nil {
			t.Fatalf("announcement %d: %v", i+1, err)
		}
	}
	if _, err := svc.Announce(ctx, proctorP, sessionID, modelSession.AnnouncementInput{Body: "x"}, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("over the limit: err = %v, want ErrConflict", err)
	}
	*clock = clock.Add(time.Minute + time.Second)
	if _, err := svc.Announce(ctx, proctorP, sessionID, modelSession.AnnouncementInput{Body: "x"}, modelAudit.Client{}); err != nil {
		t.Fatalf("a minute later: %v", err)
	}
}

func TestAnnouncementReadAndRetract(t *testing.T) {
	svc, store, clock := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()
	res, err := svc.Announce(ctx, proctorP, sessionID, modelSession.AnnouncementInput{Body: "Harap tenang.", Priority: modelSession.PriorityImportant}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}

	// unconfirmed important announcements show on the monitor after a minute
	*clock = clock.Add(2 * time.Minute)
	if err := svc.ReadAnnouncement(ctx, studentP, res.ID); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := svc.ReadAnnouncement(ctx, studentP, res.ID); err != nil {
		t.Fatalf("second read must be a no-op: %v", err)
	}
	list, err := svc.ListAnnouncements(ctx, proctorP, sessionID)
	if err != nil || list.Announcements[0].Read != 1 || !slices.Equal(list.UnreadImportant, []string{u2}) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	stranger := modelAuth.Principal{TenantID: tenantID, UserID: u3, Permissions: []string{modelAuth.PermAttemptTake}}
	if err := svc.ReadAnnouncement(ctx, stranger, res.ID); !errr.IsNotFound(err) {
		t.Fatalf("non-recipient read: err = %v", err)
	}
	if _, err := svc.MyAnnouncements(ctx, stranger, sessionID); !errr.IsNotFound(err) {
		t.Fatalf("non-participant list: err = %v", err)
	}

	cursor := mustCursor(t, svc, ctx)
	*clock = clock.Add(time.Second)
	if err := svc.RetractAnnouncement(ctx, managerP, sessionID, res.ID, modelAudit.Client{}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	mine, _ := svc.MyAnnouncements(ctx, studentP, sessionID)
	if mine.Announcements[0].Body != "" || mine.Announcements[0].RetractedAt == nil || mine.Cursor == cursor {
		t.Fatalf("after retract = %+v (cursor %q)", mine, cursor)
	}
	if list, _ := svc.ListAnnouncements(ctx, proctorP, sessionID); len(list.UnreadImportant) != 0 {
		t.Fatalf("retracted announcements are not pending: %v", list.UnreadImportant)
	}
	if !slices.Contains(store.audits, modelAudit.ActionAnnouncementRetract) {
		t.Fatalf("audits = %v", store.audits)
	}
	if err := svc.RetractAnnouncement(ctx, proctorP, sessionID, "0192a000-0000-7000-8000-000000000999", modelAudit.Client{}); !errr.IsNotFound(err) {
		t.Fatalf("unknown announcement: err = %v", err)
	}
}

func TestRetractOnlySenderOrManager(t *testing.T) {
	svc, store, _ := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()
	res, err := svc.Announce(ctx, managerP, sessionID, modelSession.AnnouncementInput{Body: "Dari panitia."}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RetractAnnouncement(ctx, proctorP, sessionID, res.ID, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor retracting the committee's announcement: err = %v", err)
	}
	if store.anns[0].RetractedAt != nil {
		t.Fatal("must stay")
	}
}

func TestBroadcast(t *testing.T) {
	svc, store, _ := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()
	store.running = []string{sessionID}
	if _, err := svc.Broadcast(ctx, proctorP, modelSession.BroadcastInput{Running: true, Body: "x"}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor broadcast: err = %v", err)
	}
	if _, err := svc.Broadcast(ctx, managerP, modelSession.BroadcastInput{Body: "x"}, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("no sessions: err = %v", err)
	}
	out, err := svc.Broadcast(ctx, managerP, modelSession.BroadcastInput{Running: true, Body: "Sesi berikutnya diundur 15 menit."}, modelAudit.Client{})
	if err != nil || out.Sessions != 1 || out.Recipients != 2 {
		t.Fatalf("broadcast = %+v, %v", out, err)
	}
}

func TestAnnouncementBoard(t *testing.T) {
	svc, store, clock := announceFixture(modelSession.StatusOpen)
	ctx := context.Background()
	b := svc.board

	c1, _ := b.Cursor(ctx, sessionID)
	c2, _ := b.Cursor(ctx, sessionID)
	if c1 != "" || c2 != "" || store.lastReads != 1 {
		t.Fatalf("cursor must come from memory after the first read: %q %q, reads %d", c1, c2, store.lastReads)
	}

	b.Touch(sessionID, *clock)
	c3, _ := b.Cursor(ctx, sessionID)
	if c3 == "" || store.lastReads != 1 {
		t.Fatalf("touch: %q, reads %d", c3, store.lastReads)
	}
	// a database read older than the cached value (another request racing a send) keeps the newer one
	b.put(sessionID, clock.Add(-time.Hour), *clock)
	if c4, _ := b.Cursor(ctx, sessionID); c4 != c3 {
		t.Fatalf("cursor moved back: %q → %q", c3, c4)
	}
	// after the TTL the database is asked again
	*clock = clock.Add(boardTTL + time.Second)
	if _, err := b.Cursor(ctx, sessionID); err != nil || store.lastReads != 2 {
		t.Fatalf("after ttl: reads %d, %v", store.lastReads, err)
	}
}

func mustCursor(t *testing.T, svc *SessionService, ctx context.Context) string {
	t.Helper()
	c, err := svc.board.Cursor(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
