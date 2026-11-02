package session

import (
	"context"
	"encoding/json"
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
	"doexam/pkg/sessiontoken"
)

const (
	tenantID  = "0192a000-0000-7000-8000-000000000001"
	sessionID = "0192a000-0000-7000-8000-0000000000f1"
	examID    = "0192a000-0000-7000-8000-0000000000e1"
	proctorID = "0192a000-0000-7000-8000-0000000000p1"
	u1        = "0192a000-0000-7000-8000-0000000000b1"
	u2        = "0192a000-0000-7000-8000-0000000000b2"
	u3        = "0192a000-0000-7000-8000-0000000000b3"
	groupID   = "0192a000-0000-7000-8000-0000000000c1"
)

// fakeStore implements only what the tests touch; other methods panic via the nil interface.
type fakeStore struct {
	SessionStorer
	sess         modelSession.SessionEntity
	examStatus   string
	examMinutes  *int
	proctors     []string
	participants map[string]modelSession.ParticipantEntity
	attempts     map[string]int64
	activeUsers  []string
	groupMembers []string
	audits       []string
}

func (f *fakeStore) FindSession(_ context.Context, id string, _ bool) (modelSession.SessionEntity, error) {
	if id != f.sess.ID {
		return modelSession.SessionEntity{}, errSessionNotFound
	}
	return f.sess, nil
}
func (f *fakeStore) CreateSession(_ context.Context, s *modelSession.SessionEntity) error {
	f.sess = *s
	return nil
}
func (f *fakeStore) UpdateSession(_ context.Context, s *modelSession.SessionEntity) error {
	f.sess = *s
	return nil
}
func (f *fakeStore) ExamRef(context.Context, string) (modelSession.ExamRefDTO, error) {
	return modelSession.ExamRefDTO{ID: examID, Title: "PTS", Status: f.examStatus, TotalMinutes: f.examMinutes}, nil
}
func (f *fakeStore) CountAttempts(_ context.Context, _, userID string) (int64, error) {
	if userID == "" {
		var n int64
		for _, v := range f.attempts {
			n += v
		}
		return n, nil
	}
	return f.attempts[userID], nil
}
func (f *fakeStore) IsProctor(_ context.Context, _, userID string) (bool, error) {
	return slices.Contains(f.proctors, userID), nil
}
func (f *fakeStore) ListProctors(context.Context, string) ([]modelSession.PersonDTO, error) {
	return []modelSession.PersonDTO{}, nil
}
func (f *fakeStore) ListSessionGroups(context.Context, string) ([]modelSession.GroupRefDTO, error) {
	return []modelSession.GroupRefDTO{}, nil
}
func (f *fakeStore) AttendanceCounts(context.Context, string) (map[string]int64, error) {
	return map[string]int64{"expected": int64(len(f.participants))}, nil
}
func (f *fakeStore) ActiveUserIDs(_ context.Context, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if slices.Contains(f.activeUsers, id) {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *fakeStore) ExistingGroupIDs(_ context.Context, ids []string) ([]string, error) {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id != groupID }), nil
}
func (f *fakeStore) GroupMemberIDs(context.Context, []string) ([]string, error) {
	return f.groupMembers, nil
}
func (f *fakeStore) AddParticipants(_ context.Context, rows []modelSession.ParticipantEntity) (int64, error) {
	var n int64
	for _, r := range rows {
		if _, ok := f.participants[r.UserID]; !ok {
			f.participants[r.UserID] = r
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) AddSessionGroups(context.Context, []modelSession.SessionGroupEntity) error {
	return nil
}
func (f *fakeStore) FindParticipant(_ context.Context, _, userID string) (modelSession.ParticipantEntity, error) {
	p, ok := f.participants[userID]
	if !ok {
		return p, errParticipantNotFound
	}
	return p, nil
}
func (f *fakeStore) UpdateParticipant(_ context.Context, p *modelSession.ParticipantEntity) error {
	f.participants[p.UserID] = *p
	return nil
}
func (f *fakeStore) ListParticipants(_ context.Context, _ string, fl modelSession.ParticipantFilter) ([]modelSession.ParticipantDTO, int64, error) {
	p := f.participants[fl.UserID]
	return []modelSession.ParticipantDTO{{UserID: p.UserID, Attendance: p.Attendance, ExtraTimeMinutes: p.ExtraTimeMinutes}}, 1, nil
}
func (f *fakeStore) DeleteParticipant(_ context.Context, _, userID string) error {
	delete(f.participants, userID)
	return nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, l.Action)
	return nil
}

type fakeFinalizer struct{ closed []string }

func (f *fakeFinalizer) FinalizeSessionAttempts(_ context.Context, sessionID, _ string) (int, error) {
	f.closed = append(f.closed, sessionID)
	return 0, nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

var (
	managerP = modelAuth.Principal{TenantID: tenantID, UserID: "admin", Permissions: []string{modelAuth.PermSessionManage, modelAuth.PermSessionProctor}}
	proctorP = modelAuth.Principal{TenantID: tenantID, UserID: proctorID, Permissions: []string{modelAuth.PermSessionProctor, modelAuth.PermExamRead}}
	otherP   = modelAuth.Principal{TenantID: tenantID, UserID: "other-proctor", Permissions: []string{modelAuth.PermSessionProctor}}
	studentP = modelAuth.Principal{TenantID: tenantID, UserID: u1, Permissions: []string{modelAuth.PermAttemptTake}}
)

var now = time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)

func newFixture(status modelSession.Status) (*SessionService, *fakeStore) {
	ninety := 90
	store := &fakeStore{
		sess: modelSession.SessionEntity{
			ID: sessionID, ExamID: examID, Name: "Sesi 1", StartsAt: now, EndsAt: now.Add(3 * time.Hour),
			TokenRequired: true, TokenSecret: []byte("secret-secret-secret-secret-1234"), TokenRotationMinutes: 15, Status: status,
		},
		examStatus: "published", examMinutes: &ninety, proctors: []string{proctorID},
		participants: map[string]modelSession.ParticipantEntity{}, attempts: map[string]int64{},
		activeUsers: []string{u1, u2, u3},
	}
	svc := NewSessionService(store, fakeTx{}, &fakeFinalizer{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now.Add(10 * time.Minute) }
	return svc, store
}

func TestAccess(t *testing.T) {
	svc, _ := newFixture(modelSession.StatusScheduled)
	ctx := context.Background()
	if dto, err := svc.Get(ctx, proctorP, sessionID); err != nil || dto.MyRole != "proctor" {
		t.Fatalf("assigned proctor: %v %v", dto.MyRole, err)
	}
	if _, err := svc.Get(ctx, otherP, sessionID); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("unassigned proctor: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Get(ctx, studentP, sessionID); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("student: err = %v, want ErrForbidden", err)
	}
	name := "x"
	if _, err := svc.Update(ctx, proctorP, sessionID, modelSession.SessionInput{Name: &name}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor edit: err = %v, want ErrForbidden", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	tests := []struct {
		from    modelSession.Status
		to      modelSession.Status
		p       modelAuth.Principal
		wantErr error
	}{
		{modelSession.StatusScheduled, modelSession.StatusOpen, proctorP, nil},
		{modelSession.StatusOpen, modelSession.StatusPaused, proctorP, nil},
		{modelSession.StatusPaused, modelSession.StatusOpen, proctorP, nil},
		{modelSession.StatusOpen, modelSession.StatusClosed, proctorP, nil},
		{modelSession.StatusScheduled, modelSession.StatusCancelled, managerP, nil},
		{modelSession.StatusScheduled, modelSession.StatusCancelled, proctorP, errr.ErrForbidden},
		{modelSession.StatusOpen, modelSession.StatusCancelled, managerP, errr.ErrConflict},
		{modelSession.StatusClosed, modelSession.StatusOpen, managerP, errr.ErrConflict},
		{modelSession.StatusScheduled, modelSession.StatusClosed, managerP, errr.ErrConflict},
		{modelSession.StatusScheduled, "deleted", managerP, errr.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			svc, store := newFixture(tt.from)
			_, err := svc.SetStatus(context.Background(), tt.p, sessionID, modelSession.StatusInput{Status: tt.to}, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && store.sess.Status != tt.to {
				t.Fatalf("status = %s, want %s", store.sess.Status, tt.to)
			}
			closed := len(svc.attempts.(*fakeFinalizer).closed) == 1
			if closed != (tt.wantErr == nil && tt.to == modelSession.StatusClosed) {
				t.Fatalf("running attempts finalized = %v", closed)
			}
		})
	}
}

func TestOpenRefusals(t *testing.T) {
	svc, store := newFixture(modelSession.StatusScheduled)
	store.examStatus = "archived"
	if _, err := svc.SetStatus(context.Background(), managerP, sessionID, modelSession.StatusInput{Status: modelSession.StatusOpen}, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("archived exam: err = %v", err)
	}
	svc, _ = newFixture(modelSession.StatusScheduled)
	svc.now = func() time.Time { return now.Add(4 * time.Hour) }
	if _, err := svc.SetStatus(context.Background(), managerP, sessionID, modelSession.StatusInput{Status: modelSession.StatusOpen}, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("window ended: err = %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, store := newFixture(modelSession.StatusScheduled)
	ctx := context.Background()
	id, name := examID, "Sesi Lab A"
	start := now.Add(24 * time.Hour)
	tooShort := start.Add(60 * time.Minute)
	if _, err := svc.Create(ctx, managerP, modelSession.SessionInput{ExamID: &id, Name: &name, StartsAt: &start, EndsAt: &tooShort}, modelAudit.Client{}); err == nil ||
		!strings.Contains(fieldsOf(err)["ends_at"], "shorter than the exam") {
		t.Fatalf("window shorter than 90 min exam: err = %v", err)
	}
	before := start.Add(-time.Minute)
	if _, err := svc.Create(ctx, managerP, modelSession.SessionInput{ExamID: &id, Name: &name, StartsAt: &start, EndsAt: &before}, modelAudit.Client{}); fieldsOf(err)["ends_at"] == "" {
		t.Fatalf("ends before starts: err = %v", err)
	}
	store.examStatus = "draft"
	end := start.Add(2 * time.Hour)
	if _, err := svc.Create(ctx, managerP, modelSession.SessionInput{ExamID: &id, Name: &name, StartsAt: &start, EndsAt: &end}, modelAudit.Client{}); fieldsOf(err)["exam_id"] == "" {
		t.Fatalf("draft exam: err = %v", err)
	}
	store.examStatus = "published"
	dto, err := svc.Create(ctx, managerP, modelSession.SessionInput{ExamID: &id, Name: &name, StartsAt: &start, EndsAt: &end}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if dto.Status != modelSession.StatusScheduled || !dto.TokenRequired || len(store.sess.TokenSecret) != sessiontoken.SecretLen {
		t.Fatalf("created = %+v secret=%d", dto, len(store.sess.TokenSecret))
	}
	b, _ := json.Marshal(dto)
	if strings.Contains(strings.ToLower(string(b)), "secret") {
		t.Fatalf("DTO must never contain the secret: %s", b)
	}
}

func TestRunningSessionEdits(t *testing.T) {
	svc, _ := newFixture(modelSession.StatusOpen)
	ctx := context.Background()
	later := now.Add(4 * time.Hour)
	if _, err := svc.Update(ctx, managerP, sessionID, modelSession.SessionInput{EndsAt: &later}, modelAudit.Client{}); err != nil {
		t.Fatalf("extend running session: %v", err)
	}
	start := now.Add(time.Hour)
	if _, err := svc.Update(ctx, managerP, sessionID, modelSession.SessionInput{StartsAt: &start}, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("move start of running session: err = %v", err)
	}
	past := now.Add(5 * time.Minute)
	if _, err := svc.Update(ctx, managerP, sessionID, modelSession.SessionInput{EndsAt: &past}, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("end in the past: err = %v", err)
	}
}

func TestTokenAndRotate(t *testing.T) {
	svc, store := newFixture(modelSession.StatusOpen)
	ctx := context.Background()
	tok, err := svc.Token(ctx, proctorP, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !sessiontoken.Verify(store.sess.TokenSecret, tok.Code, svc.now(), 15*time.Minute) || !tok.ExpiresAt.After(svc.now()) {
		t.Fatalf("token %+v does not verify", tok)
	}
	if _, err := svc.RotateToken(ctx, proctorP, sessionID, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor rotate: err = %v", err)
	}
	old := store.sess.TokenSecret
	if _, err := svc.RotateToken(ctx, managerP, sessionID, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	if sessiontoken.Verify(store.sess.TokenSecret, tok.Code, svc.now(), 15*time.Minute) || string(old) == string(store.sess.TokenSecret) {
		t.Fatal("old code must stop working after rotation")
	}
	store.sess.TokenRequired = false
	if _, err := svc.Token(ctx, managerP, sessionID); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("no token session: err = %v", err)
	}
}

func TestParticipants(t *testing.T) {
	svc, store := newFixture(modelSession.StatusScheduled)
	ctx := context.Background()
	store.groupMembers = []string{u2, u3}

	res, err := svc.AddParticipants(ctx, managerP, sessionID,
		modelSession.AddParticipantsInput{UserIDs: []string{u1, strings.ToUpper(u1), u2}, GroupIDs: []string{groupID}}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want 3 added", res)
	}
	res, _ = svc.AddParticipants(ctx, managerP, sessionID, modelSession.AddParticipantsInput{GroupIDs: []string{groupID}}, modelAudit.Client{})
	if res.Added != 0 || res.Skipped != 2 {
		t.Fatalf("re-adding a group: %+v, want 2 skipped", res)
	}
	if _, err := svc.AddParticipants(ctx, managerP, sessionID, modelSession.AddParticipantsInput{UserIDs: []string{"0192a000-0000-7000-8000-0000000000ff"}}, modelAudit.Client{}); fieldsOf(err)["user_ids"] == "" {
		t.Fatalf("unknown user: err = %v", err)
	}
	if _, err := svc.AddParticipants(ctx, proctorP, sessionID, modelSession.AddParticipantsInput{UserIDs: []string{u1}}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor add: err = %v", err)
	}

	present, extra := modelSession.AttendancePresent, 30
	if _, err := svc.UpdateParticipant(ctx, proctorP, sessionID, u1, modelSession.ParticipantUpdateInput{Attendance: &present}, modelAudit.Client{}); err != nil {
		t.Fatalf("proctor marks attendance: %v", err)
	}
	if _, err := svc.UpdateParticipant(ctx, proctorP, sessionID, u1, modelSession.ParticipantUpdateInput{ExtraTimeMinutes: &extra}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor extra time: err = %v", err)
	}
	if dto, err := svc.UpdateParticipant(ctx, managerP, sessionID, u1, modelSession.ParticipantUpdateInput{ExtraTimeMinutes: &extra}, modelAudit.Client{}); err != nil || dto.ExtraTimeMinutes != 30 || dto.Attendance != present {
		t.Fatalf("manager extra time: %+v %v", dto, err)
	}

	store.attempts[u1] = 1
	if err := svc.RemoveParticipant(ctx, managerP, sessionID, u1, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("remove started participant: err = %v", err)
	}
	if err := svc.RemoveParticipant(ctx, managerP, sessionID, u2, modelAudit.Client{}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := svc.Delete(ctx, managerP, sessionID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete session with attempts: err = %v", err)
	}
}

func fieldsOf(err error) map[string]string {
	var ve *errr.ValidationError
	if errors.As(err, &ve) {
		return ve.Fields
	}
	return map[string]string{}
}
