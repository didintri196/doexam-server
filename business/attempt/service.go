package attempt

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
	"doexam/pkg/sessiontoken"
)

// syncGrace: events that arrive after the deadline are still accepted for this long when
// their client_ts is not after the deadline (offline clients syncing late). After it the
// attempt is closed as a timeout.
const syncGrace = 2 * time.Minute

var (
	errAttemptNotFound = errr.WithMessage(errr.ErrNotFound, "attempt not found")
	errSessionNotFound = errr.WithMessage(errr.ErrNotFound, "session not found")
	errNotParticipant  = errr.WithMessage(errr.ErrForbidden, "you are not a participant of this session")
	errBadToken        = &errr.ValidationError{Fields: map[string]string{"token": "invalid or expired token"}}
	errDeviceLocked    = errr.WithMessage(errr.ErrConflict, "this attempt is bound to another device; ask the proctor to reset it")
)

// Section modes.
const (
	modeShared     = "shared"     // no section has its own timer: all sections open under the exam timer
	modeSequential = "sequential" // sections are taken one by one, each with its own timer
)

// snapshot is attempts.config_snapshot: the exam settings frozen when the attempt started.
type snapshot struct {
	ExamTitle        string          `json:"exam_title"`
	Instructions     *string         `json:"instructions"`
	Navigation       string          `json:"navigation"`
	ShuffleItems     bool            `json:"shuffle_items"`
	ShuffleOptions   bool            `json:"shuffle_options"`
	ResultVisibility string          `json:"result_visibility"`
	ShowExplanation  bool            `json:"show_explanation"`
	PassingScore     *float64        `json:"passing_score"`
	NegativeMarking  float64         `json:"negative_marking"`
	SecurityProfile  string          `json:"security_profile"`
	Settings         json.RawMessage `json:"settings"`
	DurationMinutes  *int            `json:"duration_minutes"`
	ExtraMinutes     int             `json:"extra_minutes"`
	SectionMode      string          `json:"section_mode"`
	Sections         []snapSection   `json:"sections"`
}

type snapSection struct {
	SectionID       string   `json:"section_id"`
	Title           string   `json:"title"`
	Instructions    *string  `json:"instructions"`
	DurationMinutes *int     `json:"duration_minutes"`
	AllowRevisit    bool     `json:"allow_revisit"`
	PassingScore    *float64 `json:"passing_score"`
}

// AttemptService runs attempts: start/resume, delivery without answer keys, idempotent
// answer sync, timers, submission with automatic scoring, and proctor actions.
type AttemptService struct {
	storer AttemptStorer
	tx     TxManager
	media  MediaLinker // nil: no media links (tests)
	now    func() time.Time
	seed   func() int64
	tracer trace.Tracer
	logger *slog.Logger
}

// NewAttemptService returns a concrete AttemptService.
func NewAttemptService(storer AttemptStorer, tx TxManager, media MediaLinker, logger *slog.Logger) *AttemptService {
	return &AttemptService{storer: storer, tx: tx, media: media, now: time.Now, seed: randomSeed, tracer: otel.Tracer("doexam.service.attempt"), logger: logger}
}

// Start begins an attempt for the caller in an open session, or resumes the running one.
// deviceID identifies the browser; under the "standard" security profile an attempt stays
// bound to the device it started on until a proctor resets it.
func (s *AttemptService) Start(ctx context.Context, p modelAuth.Principal, in modelAttempt.StartInput, deviceID string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Start", trace.WithAttributes(attribute.String("session.id", in.SessionID)))
	defer span.End()

	if !p.Has(modelAuth.PermAttemptTake) {
		return modelAttempt.AttemptView{}, fail(span, errr.ErrForbidden)
	}
	if uuid.Validate(in.SessionID) != nil {
		return modelAttempt.AttemptView{}, errSessionNotFound
	}
	if deviceID = strings.TrimSpace(deviceID); len(deviceID) < 8 || len(deviceID) > 64 {
		return modelAttempt.AttemptView{}, &errr.ValidationError{Fields: map[string]string{"device_id": "X-Device-ID header required (8-64 chars)"}}
	}

	var out modelAttempt.AttemptView
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		now := s.now().UTC()
		sess, err := s.storer.FindSession(ctx, in.SessionID)
		if err != nil {
			return err
		}
		part, ok, err := s.storer.FindParticipant(ctx, sess.ID, p.UserID)
		if err != nil {
			return err
		}
		if !ok {
			return errNotParticipant
		}
		if err := checkWindow(sess, now); err != nil {
			return err
		}
		if sess.TokenRequired && !sessiontoken.Verify(sess.TokenSecret, in.Token, now, time.Duration(sess.TokenRotationMinutes)*time.Minute) {
			return errBadToken
		}
		exam, err := s.storer.FindExam(ctx, sess.ExamID)
		if err != nil {
			return err
		}
		if exam.Status != modelExam.StatusPublished {
			return errr.WithMessage(errr.ErrConflict, "the exam is not published")
		}

		var a modelAttempt.AttemptEntity
		active, found, err := s.storer.ActiveAttempt(ctx, exam.ID, p.UserID)
		if err != nil {
			return err
		}
		if found {
			if a, err = s.resume(ctx, p, active, sess.ID, deviceID, client); err != nil {
				return err
			}
		}
		if a.ID == "" || (a.Status != modelAttempt.StatusInProgress && a.Status != modelAttempt.StatusLocked) {
			if a, err = s.create(ctx, p, sess, part, exam, deviceID, now, client); err != nil {
				return err
			}
		}
		if err := s.storer.MarkPresent(ctx, sess.ID, p.UserID); err != nil {
			return err
		}
		out, err = s.view(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.AttemptView{}, fail(span, err)
	}
	return out, nil
}

// checkWindow: the session must be open, inside its window, and not past late entry.
func checkWindow(sess SessionInfo, now time.Time) error {
	switch {
	case sess.Status != "open":
		return errr.WithMessage(errr.ErrConflict, "the session is not open")
	case now.Before(sess.StartsAt):
		return errr.WithMessage(errr.ErrConflict, "the session has not started yet")
	case !now.Before(sess.EndsAt):
		return errr.WithMessage(errr.ErrConflict, "the session has ended")
	}
	return nil
}

// resume continues a running attempt (after a crash or on another device). An expired
// attempt is finalized and returned with its final status.
func (s *AttemptService) resume(ctx context.Context, p modelAuth.Principal, a modelAttempt.AttemptEntity, sessionID, deviceID string, client modelAudit.Client) (modelAttempt.AttemptEntity, error) {
	if a.SessionID != sessionID {
		return a, errr.WithMessage(errr.ErrConflict, "you have a running attempt of this exam in another session")
	}
	if err := s.tick(ctx, &a, client); err != nil {
		return a, err
	}
	if a.Status != modelAttempt.StatusInProgress && a.Status != modelAttempt.StatusLocked {
		return a, nil // expired just now; the caller may start a new attempt if allowed
	}
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil {
		return a, err
	}
	now := s.now().UTC()
	if a.ActiveDeviceID != nil && *a.ActiveDeviceID != deviceID {
		if snap.SecurityProfile == modelExam.SecurityStandard {
			return a, errDeviceLocked
		}
		if err := s.event(ctx, a, modelAttempt.EventDeviceChanged, nil, map[string]any{"from": *a.ActiveDeviceID, "to": deviceID}, client); err != nil {
			return a, err
		}
	}
	a.ActiveDeviceID, a.LastSeenAt = &deviceID, &now
	if err := s.storer.UpdateAttempt(ctx, &a); err != nil {
		return a, err
	}
	return a, s.event(ctx, a, modelAttempt.EventResumed, nil, nil, client)
}

// create builds a new attempt: snapshot, seed, deadlines, sections and the drawn items.
func (s *AttemptService) create(ctx context.Context, p modelAuth.Principal, sess SessionInfo, part ParticipantInfo, exam modelExam.ExamEntity,
	deviceID string, now time.Time, client modelAudit.Client) (modelAttempt.AttemptEntity, error) {
	if sess.LateEntryMinutes != nil && now.After(sess.StartsAt.Add(time.Duration(*sess.LateEntryMinutes)*time.Minute)) {
		return modelAttempt.AttemptEntity{}, errr.WithMessage(errr.ErrConflict, "late entry is closed for this session")
	}
	n, err := s.storer.CountAttempts(ctx, exam.ID, p.UserID)
	if err != nil {
		return modelAttempt.AttemptEntity{}, err
	}
	if int(n) >= exam.MaxAttempts {
		return modelAttempt.AttemptEntity{}, errr.WithMessage(errr.ErrConflict, "no attempts left for this exam")
	}
	sections, err := s.storer.ListSections(ctx, exam.ID)
	if err != nil {
		return modelAttempt.AttemptEntity{}, err
	}
	specs, err := s.specs(ctx, exam.ID, sections)
	if err != nil {
		return modelAttempt.AttemptEntity{}, err
	}
	seed := s.seed()
	plan, err := buildPlan(specs, seed, exam.ShuffleItems, exam.ShuffleOptions)
	if err != nil {
		return modelAttempt.AttemptEntity{}, err
	}

	snap := makeSnapshot(exam, sections, part.ExtraTimeMinutes)
	extra := time.Duration(part.ExtraTimeMinutes) * time.Minute
	var total time.Duration
	if exam.DurationMinutes != nil {
		total = time.Duration(*exam.DurationMinutes)*time.Minute + extra
	} else {
		for _, sec := range sections { // publish guarantees every section has a duration here
			total += time.Duration(deref(sec.DurationMinutes))*time.Minute + extra
		}
	}
	deadline := minTime(now.Add(total), sess.EndsAt)

	a := modelAttempt.AttemptEntity{
		ID: newID(), TenantID: p.TenantID, SessionID: sess.ID, ExamID: exam.ID, UserID: p.UserID, AttemptNo: int(n) + 1,
		Status: modelAttempt.StatusInProgress, ConfigSnapshot: mustJSON(snap), Seed: seed, StartedAt: now,
		DeadlineAt: deadline, ActiveDeviceID: &deviceID, LastSeenAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	var attSections []modelAttempt.AttemptSectionEntity
	var items []modelAttempt.AttemptItemEntity
	pos := 0
	for i, ps := range plan {
		as := modelAttempt.AttemptSectionEntity{ID: newID(), TenantID: p.TenantID, AttemptID: a.ID, SectionID: ps.SectionID, SortOrder: i + 1}
		if snap.SectionMode == modeShared || i == 0 {
			as.StartedAt = &now
			if snap.SectionMode == modeSequential {
				as.DeadlineAt = sectionDeadline(now, snap.Sections[i], extra, deadline)
			}
		}
		attSections = append(attSections, as)
		for _, it := range ps.Items {
			pos++
			items = append(items, modelAttempt.AttemptItemEntity{
				ID: newID(), TenantID: p.TenantID, AttemptID: a.ID, AttemptSectionID: as.ID, Position: pos,
				ItemVersionID: it.VersionID, OptionOrder: it.OptionOrder, MaxPoints: it.Points,
			})
		}
	}
	if err := s.storer.CreateAttempt(ctx, &a, attSections, items); err != nil {
		return a, err
	}
	s.logger.InfoContext(ctx, "attempt started", slog.String("attempt_id", a.ID), slog.String("session_id", sess.ID),
		slog.String("user_id", p.UserID), slog.Int("items", len(items)))
	return a, s.event(ctx, a, modelAttempt.EventStarted, nil, map[string]any{"attempt_no": a.AttemptNo, "items": len(items), "device_id": deviceID}, client)
}

// specs loads the published structure the selector draws from.
func (s *AttemptService) specs(ctx context.Context, examID string, sections []modelExam.SectionEntity) ([]sectionSpec, error) {
	rows, err := s.storer.Candidates(ctx, examID)
	if err != nil {
		return nil, err
	}
	rules, err := s.storer.ListRules(ctx, examID)
	if err != nil {
		return nil, err
	}
	toCand := func(r CandidateRow) candidate {
		return candidate{ItemID: r.ItemID, VersionID: r.VersionID, Type: r.Type, OptionIDs: optionIDs(r.Content),
			DefaultPoints: r.DefaultPoints, Points: r.Points, LockOptions: r.LockOptions}
	}
	out := make([]sectionSpec, 0, len(sections))
	for _, sec := range sections {
		spec := sectionSpec{ID: sec.ID, SortOrder: sec.SortOrder, Random: sec.Selection == modelExam.SelectionRandom}
		for _, r := range rows {
			if r.SectionID == sec.ID {
				spec.Fixed = append(spec.Fixed, toCand(r))
			}
		}
		for _, ru := range rules {
			if ru.SectionID != sec.ID {
				continue
			}
			rs := ruleSpec{ID: ru.ID, Count: ru.ItemCount, Points: ru.Points}
			for _, r := range rows {
				if r.RuleID == ru.ID {
					rs.Pool = append(rs.Pool, toCand(r))
				}
			}
			spec.Rules = append(spec.Rules, rs)
		}
		out = append(out, spec)
	}
	return out, nil
}

func makeSnapshot(e modelExam.ExamEntity, sections []modelExam.SectionEntity, extra int) snapshot {
	var sec struct {
		Profile string `json:"profile"`
	}
	_ = json.Unmarshal(e.Security, &sec)
	if sec.Profile == "" {
		sec.Profile = modelExam.SecurityLight
	}
	snap := snapshot{
		ExamTitle: e.Title, Instructions: bodyOf(e.Instructions), Navigation: string(e.Navigation),
		ShuffleItems: e.ShuffleItems, ShuffleOptions: e.ShuffleOptions, ResultVisibility: string(e.ResultVisibility),
		ShowExplanation: e.ShowExplanation, PassingScore: e.PassingScore, NegativeMarking: e.NegativeMarking,
		SecurityProfile: sec.Profile, Settings: e.Settings, DurationMinutes: e.DurationMinutes, ExtraMinutes: extra,
		SectionMode: modeShared,
	}
	for _, s := range sections {
		if s.DurationMinutes != nil {
			snap.SectionMode = modeSequential
		}
		snap.Sections = append(snap.Sections, snapSection{
			SectionID: s.ID, Title: s.Title, Instructions: bodyOf(s.Instructions), DurationMinutes: s.DurationMinutes,
			AllowRevisit: s.AllowRevisit, PassingScore: s.PassingScore,
		})
	}
	return snap
}

// sectionDeadline: the section's own duration (+ extra time), never past the attempt deadline.
// A section without a duration in sequential mode shares the remaining attempt time.
func sectionDeadline(start time.Time, sec snapSection, extra time.Duration, attemptDeadline time.Time) *time.Time {
	d := attemptDeadline
	if sec.DurationMinutes != nil {
		d = minTime(start.Add(time.Duration(*sec.DurationMinutes)*time.Minute+extra), attemptDeadline)
	}
	return &d
}

// event appends a server-side event (no client id).
func (s *AttemptService) event(ctx context.Context, a modelAttempt.AttemptEntity, typ string, actorID *string, payload any, client modelAudit.Client) error {
	var body []byte
	if payload != nil {
		body = mustJSON(payload)
	}
	_, err := s.storer.InsertEvent(ctx, &modelAttempt.EventEntity{
		TenantID: a.TenantID, AttemptID: a.ID, Type: typ, Payload: body, ActorID: actorID,
		ServerTS: s.now().UTC(), IP: nonEmpty(client.IP),
	})
	return err
}

// own loads an attempt of the caller (others' attempts are "not found").
func (s *AttemptService) own(ctx context.Context, p modelAuth.Principal, id string, forUpdate bool) (modelAttempt.AttemptEntity, error) {
	if uuid.Validate(id) != nil {
		return modelAttempt.AttemptEntity{}, errAttemptNotFound
	}
	a, err := s.storer.FindAttempt(ctx, id, forUpdate)
	if err != nil {
		return a, err
	}
	if a.UserID != p.UserID {
		return modelAttempt.AttemptEntity{}, errAttemptNotFound
	}
	return a, nil
}

func decodeSnapshot(b []byte) (snapshot, error) {
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, fmt.Errorf("decode config snapshot: %w", err)
	}
	return snap, nil
}

func optionIDs(content []byte) []string {
	var c struct {
		Options []struct {
			ID string `json:"id"`
		} `json:"options"`
	}
	_ = json.Unmarshal(content, &c)
	var ids []string
	for _, o := range c.Options {
		ids = append(ids, o.ID)
	}
	return ids
}

func bodyOf(b []byte) *string {
	var c struct {
		Body string `json:"body"`
	}
	if len(b) == 0 || json.Unmarshal(b, &c) != nil || c.Body == "" {
		return nil
	}
	return &c.Body
}

func randomSeed() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on supported platforms
	return int64(binary.BigEndian.Uint64(b[:]) >> 1)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal attempt json: %v", err)) // plain values: unreachable
	}
	return b
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string { return uuid.Must(uuid.NewV7()).String() }
