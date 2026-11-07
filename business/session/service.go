package session

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	modelSession "doexam/models/session"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
	"doexam/pkg/sessiontoken"
)

// Limits.
const (
	maxWindow       = 31 * 24 * time.Hour
	defaultRotation = 15
)

var (
	errSessionNotFound     = errr.WithMessage(errr.ErrNotFound, "session not found")
	errParticipantNotFound = errr.WithMessage(errr.ErrNotFound, "participant not found")
)

// role is how the caller relates to a session.
type role string

const (
	roleManager role = "manager" // session.manage: everything
	roleProctor role = "proctor" // session.proctor + assigned: token, status, attendance
)

// transitions lists the allowed status changes.
var transitions = map[modelSession.Status][]modelSession.Status{
	modelSession.StatusScheduled: {modelSession.StatusOpen, modelSession.StatusCancelled},
	modelSession.StatusOpen:      {modelSession.StatusPaused, modelSession.StatusClosed},
	modelSession.StatusPaused:    {modelSession.StatusOpen, modelSession.StatusClosed},
}

// SessionService schedules exam sessions: time window, room token, proctors and participants.
//
// session.manage may do everything. session.proctor only sees sessions they are
// assigned to, and there may read the token, change the status (except cancel) and
// record attendance/seat/notes. Other sessions are "not found" for them.
type SessionService struct {
	hasher   PasswordHasher // card passwords
	storer   SessionStorer
	tx       TxManager
	attempts Attempts
	links    LinkSigner // card signature image
	board    *AnnouncementBoard
	now      func() time.Time
	tracer   trace.Tracer
	logger   *slog.Logger
}

// NewSessionService returns a concrete SessionService.
func NewSessionService(storer SessionStorer, tx TxManager, attempts Attempts, hasher PasswordHasher, links LinkSigner, board *AnnouncementBoard, logger *slog.Logger) *SessionService {
	return &SessionService{storer: storer, tx: tx, attempts: attempts, hasher: hasher, links: links, board: board, now: time.Now, tracer: otel.Tracer("doexam.service.session"), logger: logger}
}

// List returns sessions: all for managers, assigned ones for proctors.
func (s *SessionService) List(ctx context.Context, p modelAuth.Principal, f modelSession.ListFilter) ([]modelSession.SessionSummaryDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.List")
	defer span.End()

	all := p.Has(modelAuth.PermSessionManage)
	if !all && !p.Has(modelAuth.PermSessionProctor) {
		return nil, 0, fail(span, errr.ErrForbidden)
	}
	if f.ExamID != "" && uuid.Validate(f.ExamID) != nil {
		return nil, 0, &errr.ValidationError{Fields: map[string]string{"exam_id": "must be a UUID"}}
	}
	var (
		out   []modelSession.SessionSummaryDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		out, total, err = s.storer.ListSessions(ctx, f, p.UserID, all)
		return err
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	return out, total, nil
}

// Get returns one session with exam, proctors, groups and attendance counts.
func (s *SessionService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelSession.SessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Get", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.SessionDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, r, err := s.load(ctx, p, id, false)
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, sess, r)
		return err
	})
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	return out, nil
}

// Print returns what the session documents need: participant cards, attendance list,
// minutes and the proctor sheet (session managers and the session's proctors).
func (s *SessionService) Print(ctx context.Context, p modelAuth.Principal, id string) (modelSession.PrintDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Print", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.PrintDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, r, err := s.load(ctx, p, id, false)
		if err != nil {
			return err
		}
		if out.Session, err = s.detail(ctx, sess, r); err != nil {
			return err
		}
		if out.Participants, err = s.storer.PrintParticipants(ctx, id); err != nil {
			return err
		}
		t, err := s.storer.FindTenant(ctx, p.TenantID)
		if err != nil {
			return err
		}
		st := modelTenant.DecodeSettings(t.Settings)
		var signature *string
		if id := st.Card.SignatureFileID; id != "" {
			u := s.links.Path(modelMedia.URLPrefix, t.ID, id, s.now())
			signature = &u
		}
		out.Institution = modelSession.InstitutionDTO{
			Name: t.Name, Letterhead: st.Letterhead, ExamRules: st.ExamRules, LogoURL: modelTenant.LogoURL(t.Slug, st.LogoFileID),
			Card: modelTenant.ToCardDTO(st.Card, signature),
		}
		return nil
	})
	if err != nil {
		return modelSession.PrintDTO{}, fail(span, err)
	}
	out.Stats = printStats(out.Participants)
	out.GeneratedAt = s.now().UTC()
	return out, nil
}

// printStats counts attendance for the minutes. Someone who started an attempt was
// present even if the proctor did not tick the attendance list.
func printStats(ps []modelSession.PrintParticipantDTO) modelSession.PrintStatsDTO {
	st := modelSession.PrintStatsDTO{Registered: len(ps)}
	for _, p := range ps {
		started := p.AttemptStatus != nil
		if started {
			st.Started++
			if *p.AttemptStatus == "submitted" || *p.AttemptStatus == "graded" {
				st.Submitted++
			}
		}
		switch {
		case p.Attendance == modelSession.AttendancePresent || started:
			st.Present++
		case p.Attendance == modelSession.AttendanceAbsent:
			st.Absent++
		case p.Attendance == modelSession.AttendanceExcused:
			st.Excused++
		}
	}
	return st
}

// Create schedules a session for a published exam.
func (s *SessionService) Create(ctx context.Context, p modelAuth.Principal, in modelSession.SessionInput, client modelAudit.Client) (modelSession.SessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Create")
	defer span.End()

	if !p.Has(modelAuth.PermSessionManage) {
		return modelSession.SessionDTO{}, fail(span, errr.ErrForbidden)
	}
	now := s.now().UTC()
	sess := modelSession.SessionEntity{
		ID: newID(), TenantID: p.TenantID, TokenRequired: true, TokenRotationMinutes: defaultRotation,
		Status: modelSession.StatusScheduled, CreatedBy: &p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	v := errr.Validation{}
	v.Check(in.ExamID != nil, "exam_id", "required")
	v.Check(in.Name != nil, "name", "required")
	v.Check(in.StartsAt != nil, "starts_at", "required")
	v.Check(in.EndsAt != nil, "ends_at", "required")
	applyInput(&sess, in, v)
	if err := v.Err(); err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	if err := ensureSecret(&sess); err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}

	var out modelSession.SessionDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.checkExam(ctx, sess); err != nil {
			return err
		}
		if err := s.storer.CreateSession(ctx, &sess); err != nil {
			return err
		}
		var err error
		if out, err = s.detail(ctx, sess, roleManager); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSessionCreate, sess.ID, nil, out)
	})
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	return out, nil
}

// runningEditable reports whether in only touches fields that may change while a session runs.
func runningEditable(in modelSession.SessionInput) bool {
	// camera/screen requirements are fixed once participants may have started without them
	return in.ExamID == nil && in.StartsAt == nil && in.IsMakeup == nil && in.RequireCamera == nil && in.RequireScreen == nil
}

// Update changes a session (session.manage). A running session (open/paused) only
// accepts name, room, ends_at, late_entry_minutes and token settings; closed and
// cancelled sessions are read-only.
func (s *SessionService) Update(ctx context.Context, p modelAuth.Principal, id string, in modelSession.SessionInput, client modelAudit.Client) (modelSession.SessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Update", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.SessionDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.loadManaged(ctx, p, id)
		if err != nil {
			return err
		}
		switch sess.Status {
		case modelSession.StatusClosed, modelSession.StatusCancelled:
			return errr.WithMessage(errr.ErrConflict, "closed or cancelled sessions cannot be changed")
		case modelSession.StatusOpen, modelSession.StatusPaused:
			if !runningEditable(in) {
				return errr.WithMessage(errr.ErrConflict, "a running session only accepts name, room, ends_at, late_entry_minutes and token settings")
			}
			if in.EndsAt != nil && !in.EndsAt.After(s.now()) {
				return &errr.ValidationError{Fields: map[string]string{"ends_at": "must be in the future while the session runs"}}
			}
		}
		before, err := s.detail(ctx, sess, roleManager)
		if err != nil {
			return err
		}
		if in.ExamID != nil && *in.ExamID != sess.ExamID {
			n, err := s.storer.CountAttempts(ctx, id, "")
			if err != nil {
				return err
			}
			if n > 0 {
				return errr.WithMessage(errr.ErrConflict, "the session already has attempts; its exam cannot change")
			}
		}
		v := errr.Validation{}
		applyInput(&sess, in, v)
		if err := v.Err(); err != nil {
			return err
		}
		if err := ensureSecret(&sess); err != nil {
			return err
		}
		if err := s.checkExam(ctx, sess); err != nil {
			return err
		}
		if err := s.storer.UpdateSession(ctx, &sess); err != nil {
			return err
		}
		sess.UpdatedAt = s.now().UTC()
		if out, err = s.detail(ctx, sess, roleManager); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSessionUpdate, id, before, out)
	})
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	return out, nil
}

// Delete removes a scheduled session that has no attempts (otherwise cancel it).
func (s *SessionService) Delete(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "SessionService.Delete", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.loadManaged(ctx, p, id)
		if err != nil {
			return err
		}
		n, err := s.storer.CountAttempts(ctx, id, "")
		if err != nil {
			return err
		}
		if sess.Status != modelSession.StatusScheduled || n > 0 {
			return errr.WithMessage(errr.ErrConflict, "only scheduled sessions without attempts can be deleted; cancel or close it instead")
		}
		if err := s.storer.DeleteSession(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSessionDelete, id, map[string]any{"name": sess.Name, "exam_id": sess.ExamID}, nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// SetStatus moves a session along scheduled → open ⇄ paused → closed (or scheduled → cancelled).
// Proctors of the session may open, pause and close; only managers cancel.
func (s *SessionService) SetStatus(ctx context.Context, p modelAuth.Principal, id string, in modelSession.StatusInput, client modelAudit.Client) (modelSession.SessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.SetStatus", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.SessionDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, r, err := s.load(ctx, p, id, true)
		if err != nil {
			return err
		}
		if in.Status == modelSession.StatusCancelled && r != roleManager {
			return errr.WithMessage(errr.ErrForbidden, "only session managers can cancel a session")
		}
		if !slices.Contains(transitions[sess.Status], in.Status) {
			return errr.WithMessage(errr.ErrConflict, fmt.Sprintf("cannot change a %s session to %q", sess.Status, in.Status))
		}
		if in.Status == modelSession.StatusOpen {
			if !s.now().Before(sess.EndsAt) {
				return errr.WithMessage(errr.ErrConflict, "the session window has ended")
			}
			exam, err := s.storer.ExamRef(ctx, sess.ExamID)
			if err != nil {
				return err
			}
			if exam.Status != "published" {
				return errr.WithMessage(errr.ErrConflict, "the exam is no longer published")
			}
		}
		from := sess.Status
		sess.Status = in.Status
		if err := s.storer.UpdateSession(ctx, &sess); err != nil {
			return err
		}
		if in.Status == modelSession.StatusClosed {
			// nobody may keep answering after the session is closed
			if _, err := s.attempts.FinalizeSessionAttempts(ctx, id, p.UserID); err != nil {
				return err
			}
		}
		if out, err = s.detail(ctx, sess, r); err != nil {
			return err
		}
		s.logger.InfoContext(ctx, "session status changed", slog.String("session_id", id),
			slog.String("from", string(from)), slog.String("to", string(in.Status)))
		return s.audit(ctx, p, client, modelAudit.ActionSessionStatus, id, map[string]any{"status": from}, map[string]any{"status": in.Status})
	})
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	if in.Status == modelSession.StatusOpen {
		// participants start right after opening: have their questions in the cache by then
		s.attempts.WarmExam(ctx, p.TenantID, out.Exam.ID)
	}
	return out, nil
}

// Token returns the current room token (managers and the session's proctors).
func (s *SessionService) Token(ctx context.Context, p modelAuth.Principal, id string) (modelSession.TokenDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Token", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.TokenDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.load(ctx, p, id, false)
		if err != nil {
			return err
		}
		out, err = s.tokenOf(sess)
		return err
	})
	if err != nil {
		return modelSession.TokenDTO{}, fail(span, err)
	}
	return out, nil
}

// RotateToken replaces the secret so the current code stops working at once (e.g. it leaked).
func (s *SessionService) RotateToken(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelSession.TokenDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.RotateToken", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	var out modelSession.TokenDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.loadManaged(ctx, p, id)
		if err != nil {
			return err
		}
		if !sess.TokenRequired {
			return errr.WithMessage(errr.ErrConflict, "this session does not use a token")
		}
		if sess.TokenSecret, err = sessiontoken.NewSecret(); err != nil {
			return err
		}
		if err := s.storer.UpdateSession(ctx, &sess); err != nil {
			return err
		}
		if out, err = s.tokenOf(sess); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSessionTokenRotate, id, nil, nil)
	})
	if err != nil {
		return modelSession.TokenDTO{}, fail(span, err)
	}
	return out, nil
}

// SetProctors replaces the proctor list. Every user must hold session.proctor.
func (s *SessionService) SetProctors(ctx context.Context, p modelAuth.Principal, id string, in modelSession.ProctorsInput, client modelAudit.Client) (modelSession.SessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.SetProctors", trace.WithAttributes(attribute.String("session.id", id)))
	defer span.End()

	ids, err := cleanIDs(in.UserIDs, "user_ids", 50)
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	var out modelSession.SessionDTO
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.loadManaged(ctx, p, id)
		if err != nil {
			return err
		}
		if sess.Status == modelSession.StatusClosed || sess.Status == modelSession.StatusCancelled {
			return errr.WithMessage(errr.ErrConflict, "closed or cancelled sessions cannot be changed")
		}
		ok, err := s.storer.UsersWithPermission(ctx, ids, modelAuth.PermSessionProctor)
		if err != nil {
			return err
		}
		if missing := diff(ids, ok); len(missing) > 0 {
			return &errr.ValidationError{Fields: map[string]string{"user_ids": "not active proctors: " + short(missing)}}
		}
		if err := s.storer.ReplaceProctors(ctx, p.TenantID, id, ids); err != nil {
			return err
		}
		if out, err = s.detail(ctx, sess, roleManager); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSessionProctors, id, nil, map[string]any{"user_ids": ids})
	})
	if err != nil {
		return modelSession.SessionDTO{}, fail(span, err)
	}
	return out, nil
}

// MySessions lists the caller's upcoming and running sessions (participant view, no token).
func (s *SessionService) MySessions(ctx context.Context, p modelAuth.Principal) ([]modelSession.MySessionDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.MySessions")
	defer span.End()

	var out []modelSession.MySessionDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		out, err = s.storer.MySessions(ctx, p.UserID, s.now().UTC())
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// applyInput validates in and applies the given fields.
func applyInput(sess *modelSession.SessionEntity, in modelSession.SessionInput, v errr.Validation) {
	if in.ExamID != nil {
		sess.ExamID = strings.ToLower(strings.TrimSpace(*in.ExamID))
		v.Check(uuid.Validate(sess.ExamID) == nil, "exam_id", "must be a UUID")
	}
	if in.Name != nil {
		sess.Name = strings.TrimSpace(*in.Name)
		v.Check(sess.Name != "" && utf8.RuneCountInString(sess.Name) <= 200, "name", "required, max 200 chars")
	}
	if in.Room != nil {
		r := strings.TrimSpace(*in.Room)
		v.Check(utf8.RuneCountInString(r) <= 100, "room", "max 100 chars")
		sess.Room = optional(r)
	}
	if in.StartsAt != nil {
		sess.StartsAt = in.StartsAt.UTC()
	}
	if in.EndsAt != nil {
		sess.EndsAt = in.EndsAt.UTC()
	}
	if in.LateEntryMinutes.Set {
		sess.LateEntryMinutes = in.LateEntryMinutes.Ptr()
		v.Check(sess.LateEntryMinutes == nil || (*sess.LateEntryMinutes >= 0 && *sess.LateEntryMinutes <= 1440), "late_entry_minutes", "0-1440 or null")
	}
	if in.TokenRequired != nil {
		sess.TokenRequired = *in.TokenRequired
	}
	if in.TokenRotationMinutes != nil {
		sess.TokenRotationMinutes = *in.TokenRotationMinutes
		v.Check(sess.TokenRotationMinutes >= 1 && sess.TokenRotationMinutes <= 1440, "token_rotation_minutes", "1-1440")
	}
	if in.IsMakeup != nil {
		sess.IsMakeup = *in.IsMakeup
	}
	if in.RequireCamera != nil {
		sess.RequireCamera = *in.RequireCamera
	}
	if in.RequireScreen != nil {
		sess.RequireScreen = *in.RequireScreen
	}
	if !sess.StartsAt.IsZero() && !sess.EndsAt.IsZero() {
		v.Check(sess.EndsAt.After(sess.StartsAt), "ends_at", "must be after starts_at")
		v.Check(sess.EndsAt.Sub(sess.StartsAt) <= maxWindow, "ends_at", "window max 31 days")
	}
}

// checkExam: the exam must exist, be published, and fit in the window.
func (s *SessionService) checkExam(ctx context.Context, sess modelSession.SessionEntity) error {
	exam, err := s.storer.ExamRef(ctx, sess.ExamID)
	if err != nil {
		return err
	}
	switch {
	case exam.ID == "":
		return &errr.ValidationError{Fields: map[string]string{"exam_id": "exam not found"}}
	case exam.Status != "published":
		return &errr.ValidationError{Fields: map[string]string{"exam_id": "exam must be published"}}
	case exam.TotalMinutes != nil && sess.EndsAt.Sub(sess.StartsAt) < time.Duration(*exam.TotalMinutes)*time.Minute:
		return &errr.ValidationError{Fields: map[string]string{"ends_at": fmt.Sprintf("the window is shorter than the exam (%d minutes)", *exam.TotalMinutes)}}
	}
	return nil
}

// load fetches a session and the caller's role on it; sessions the caller may not see are "not found".
func (s *SessionService) load(ctx context.Context, p modelAuth.Principal, id string, forUpdate bool) (modelSession.SessionEntity, role, error) {
	if uuid.Validate(id) != nil {
		return modelSession.SessionEntity{}, "", errSessionNotFound
	}
	if !p.Has(modelAuth.PermSessionManage) && !p.Has(modelAuth.PermSessionProctor) {
		return modelSession.SessionEntity{}, "", errr.ErrForbidden
	}
	sess, err := s.storer.FindSession(ctx, id, forUpdate)
	if err != nil {
		return modelSession.SessionEntity{}, "", err
	}
	if p.Has(modelAuth.PermSessionManage) {
		return sess, roleManager, nil
	}
	ok, err := s.storer.IsProctor(ctx, id, p.UserID)
	if err != nil {
		return modelSession.SessionEntity{}, "", err
	}
	if !ok {
		return modelSession.SessionEntity{}, "", errSessionNotFound
	}
	return sess, roleProctor, nil
}

// loadManaged is load for actions that need session.manage.
func (s *SessionService) loadManaged(ctx context.Context, p modelAuth.Principal, id string) (modelSession.SessionEntity, role, error) {
	sess, r, err := s.load(ctx, p, id, true)
	if err != nil {
		return sess, r, err
	}
	if r != roleManager {
		return modelSession.SessionEntity{}, "", errr.WithMessage(errr.ErrForbidden, "needs the session.manage permission")
	}
	return sess, r, nil
}

func (s *SessionService) detail(ctx context.Context, sess modelSession.SessionEntity, r role) (modelSession.SessionDTO, error) {
	exam, err := s.storer.ExamRef(ctx, sess.ExamID)
	if err != nil {
		return modelSession.SessionDTO{}, err
	}
	proctors, err := s.storer.ListProctors(ctx, sess.ID)
	if err != nil {
		return modelSession.SessionDTO{}, err
	}
	groups, err := s.storer.ListSessionGroups(ctx, sess.ID)
	if err != nil {
		return modelSession.SessionDTO{}, err
	}
	att, err := s.storer.AttendanceCounts(ctx, sess.ID)
	if err != nil {
		return modelSession.SessionDTO{}, err
	}
	var count int64
	for _, n := range att {
		count += n
	}
	return modelSession.SessionDTO{
		ID: sess.ID, Exam: exam, Name: sess.Name, Room: sess.Room, StartsAt: sess.StartsAt, EndsAt: sess.EndsAt,
		LateEntryMinutes: sess.LateEntryMinutes, TokenRequired: sess.TokenRequired, TokenRotationMinutes: sess.TokenRotationMinutes,
		Status: sess.Status, IsMakeup: sess.IsMakeup, RequireCamera: sess.RequireCamera, RequireScreen: sess.RequireScreen,
		ParticipantCount: count, Attendance: att,
		Proctors: proctors, Groups: groups, MyRole: string(r), CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
	}, nil
}

func (s *SessionService) tokenOf(sess modelSession.SessionEntity) (modelSession.TokenDTO, error) {
	if !sess.TokenRequired || len(sess.TokenSecret) == 0 {
		return modelSession.TokenDTO{}, errr.WithMessage(errr.ErrConflict, "this session does not use a token")
	}
	rot := time.Duration(sess.TokenRotationMinutes) * time.Minute
	now := s.now()
	return modelSession.TokenDTO{
		Code: sessiontoken.Code(sess.TokenSecret, now, rot), ExpiresAt: sessiontoken.ExpiresAt(now, rot),
		RotationMinutes: sess.TokenRotationMinutes,
	}, nil
}

func (s *SessionService) audit(ctx context.Context, p modelAuth.Principal, client modelAudit.Client, action, id string, before, after any) error {
	return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: p.TenantID, ActorID: p.UserID, Action: action, EntityType: "exam_session", EntityID: id,
		Before: before, After: after, Client: client,
	}))
}

// ensureSecret creates a token secret when the session needs one and has none.
func ensureSecret(sess *modelSession.SessionEntity) error {
	if !sess.TokenRequired || len(sess.TokenSecret) > 0 {
		return nil
	}
	secret, err := sessiontoken.NewSecret()
	if err != nil {
		return err
	}
	sess.TokenSecret = secret
	return nil
}

// cleanIDs lowercases, trims, de-duplicates and validates UUIDs.
func cleanIDs(in []string, field string, maxN int) ([]string, error) {
	out := []string{}
	for _, id := range in {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || slices.Contains(out, id) {
			continue
		}
		if uuid.Validate(id) != nil {
			return nil, &errr.ValidationError{Fields: map[string]string{field: "must be UUIDs"}}
		}
		out = append(out, id)
	}
	if len(out) > maxN {
		return nil, &errr.ValidationError{Fields: map[string]string{field: fmt.Sprintf("max %d", maxN)}}
	}
	return out, nil
}

func diff(want, have []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(have, w) {
			out = append(out, w)
		}
	}
	return out
}

func short(ids []string) string {
	if len(ids) > 5 {
		return strings.Join(ids[:5], ", ") + ", ..."
	}
	return strings.Join(ids, ", ")
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string { return uuid.Must(uuid.NewV7()).String() }
