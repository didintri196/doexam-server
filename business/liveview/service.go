// Package liveview lets a proctor watch a participant's camera and screen on demand
// (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md). The exam client keeps both shared locally; only when a
// proctor opens a view does it send video, peer-to-peer over WebRTC. The server relays the
// signaling (offer/answer/ICE) through long-polls woken by PostgreSQL NOTIFY, and audits who
// watched whom. Video never passes through the server.
package liveview

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelLive "doexam/models/liveview"
	"doexam/pkg/errr"
)

// Limits (DOCS/PLAN/19 §5).
const (
	ViewDuration       = 10 * time.Minute // a view ends by itself; the proctor may open it again
	MaxViewsPerProctor = 4
	MaxViewersPerUser  = 2
	MaxPollWait        = 25 * time.Second // below proxy read timeouts (nginx 60s)
	maxPayload         = 16 << 10         // an SDP with candidates is a few KB
	pollLimit          = 50
	endGrace           = time.Minute      // "end" messages stay deliverable this long
	purgeAfter         = 15 * time.Minute // older signaling of an attempt is deleted
)

var (
	errSharingOff = errr.WithMessage(errr.ErrConflict, "the session does not require camera or screen sharing")
	errNotRunning = errr.WithMessage(errr.ErrConflict, "the participant is not taking the exam")
	errTooMany    = errr.WithMessage(errr.ErrConflict, "too many proctors are watching this participant")
	errOwnLimit   = errr.WithMessage(errr.ErrConflict, "close one of your live views first")
	errViewEnded  = errr.WithMessage(errr.ErrConflict, "the live view has ended")
)

// LiveViewService implements live views.
type LiveViewService struct {
	storer     LiveViewStorer
	tx         TxManager
	waker      Waker
	iceServers json.RawMessage
	tracer     trace.Tracer
	logger     *slog.Logger
	now        func() time.Time
}

// NewLiveViewService creates the service. iceServers is the RTCPeerConnection iceServers JSON
// array handed to both peers (empty = LAN only).
func NewLiveViewService(storer LiveViewStorer, tx TxManager, waker Waker, iceServers json.RawMessage, logger *slog.Logger) *LiveViewService {
	if len(iceServers) == 0 {
		iceServers = json.RawMessage("[]")
	}
	return &LiveViewService{
		storer: storer, tx: tx, waker: waker, iceServers: iceServers,
		tracer: otel.Tracer("doexam.service.liveview"), logger: logger, now: time.Now,
	}
}

func attemptKey(id string) string { return "a:" + id }
func viewKey(id string) string    { return "v:" + id }

// Open starts a live view of a running attempt for the calling proctor. The participant is
// told to start sending (view_request); the proctor then polls for the offer.
func (s *LiveViewService) Open(ctx context.Context, p modelAuth.Principal, sessionID string, in modelLive.OpenInput, client modelAudit.Client) (modelLive.ViewDTO, error) {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.Open", trace.WithAttributes(attribute.String("session.id", sessionID), attribute.String("attempt.id", in.AttemptID)))
	defer span.End()
	if uuid.Validate(in.AttemptID) != nil {
		return modelLive.ViewDTO{}, fail(span, &errr.ValidationError{Fields: map[string]string{"attempt_id": "must be a UUID"}})
	}

	var out modelLive.ViewDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, err := s.proctorSession(ctx, p, sessionID)
		if err != nil {
			return err
		}
		if !sess.RequireCamera && !sess.RequireScreen {
			return errSharingOff
		}
		a, err := s.storer.FindAttempt(ctx, in.AttemptID)
		if err != nil || a.SessionID != sessionID {
			return errr.ErrNotFound
		}
		if a.Status != "in_progress" && a.Status != "locked" {
			return errNotRunning
		}
		now := s.now().UTC().Truncate(time.Microsecond)
		active, err := s.storer.ActiveViews(ctx, a.ID, now)
		if err != nil {
			return err
		}
		others := 0
		for _, v := range active {
			if v.ProctorID == p.UserID { // reopening replaces the proctor's own view
				if err := s.end(ctx, p, v, modelLive.EndReplaced, now, client); err != nil {
					return err
				}
				continue
			}
			others++
		}
		if others >= MaxViewersPerUser {
			return errTooMany
		}
		mine, err := s.storer.CountProctorViews(ctx, p.UserID, now)
		if err != nil {
			return err
		}
		if mine >= MaxViewsPerProctor {
			return errOwnLimit
		}
		if err := s.storer.PurgeSignals(ctx, a.ID, now.Add(-purgeAfter)); err != nil {
			return err
		}

		v := modelLive.ViewEntity{
			ID: uuid.Must(uuid.NewV7()).String(), TenantID: p.TenantID, SessionID: sessionID, AttemptID: a.ID,
			ProctorID: p.UserID, StartedAt: now, ExpiresAt: now.Add(ViewDuration),
		}
		if err := s.storer.CreateView(ctx, &v); err != nil {
			return err
		}
		out = modelLive.ViewDTO{
			ID: v.ID, SessionID: sessionID, AttemptID: a.ID, Camera: sess.RequireCamera, Screen: sess.RequireScreen,
			StartedAt: v.StartedAt, ExpiresAt: v.ExpiresAt, ICEServers: s.iceServers,
		}
		req, _ := json.Marshal(map[string]any{
			"camera": sess.RequireCamera, "screen": sess.RequireScreen, "expires_at": v.ExpiresAt, "ice_servers": s.iceServers,
		})
		if err := s.signal(ctx, v, modelLive.RecipientParticipant, modelLive.KindViewRequest, req, now); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionLiveViewStart,
			EntityType: "live_view", EntityID: v.ID, Client: client,
			After: map[string]any{"session_id": sessionID, "attempt_id": a.ID, "participant_id": a.UserID, "camera": sess.RequireCamera, "screen": sess.RequireScreen},
		}))
	})
	if err != nil {
		return modelLive.ViewDTO{}, fail(span, err)
	}
	return out, nil
}

// Close ends the calling proctor's view.
func (s *LiveViewService) Close(ctx context.Context, p modelAuth.Principal, sessionID, viewID string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.Close", trace.WithAttributes(attribute.String("view.id", viewID)))
	defer span.End()
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		v, err := s.ownView(ctx, p, sessionID, viewID)
		if err != nil {
			return err
		}
		return s.end(ctx, p, v, modelLive.EndProctor, s.now().UTC(), client)
	})
	return fail(span, err)
}

// ProctorSignal relays the proctor's answer or ICE candidate to the participant.
func (s *LiveViewService) ProctorSignal(ctx context.Context, p modelAuth.Principal, sessionID, viewID string, in modelLive.SignalInput) error {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.ProctorSignal", trace.WithAttributes(attribute.String("view.id", viewID), attribute.String("signal.kind", string(in.Kind))))
	defer span.End()
	if err := checkSignal(in, modelLive.KindAnswer, modelLive.KindICE); err != nil {
		return fail(span, err)
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		v, err := s.ownView(ctx, p, sessionID, viewID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if !v.Active(now) {
			return errViewEnded
		}
		return s.signal(ctx, v, modelLive.RecipientParticipant, in.Kind, in.Payload, now)
	})
	return fail(span, err)
}

// ProctorPoll waits up to wait for messages to the proctor after the cursor. Ended reports a
// view that is over (closed by the participant, or expired: then it is ended and audited here).
func (s *LiveViewService) ProctorPoll(ctx context.Context, p modelAuth.Principal, sessionID, viewID string, after int64, wait time.Duration, client modelAudit.Client) (modelLive.PollDTO, error) {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.ProctorPoll", trace.WithAttributes(attribute.String("view.id", viewID)))
	defer span.End()
	read := func(ctx context.Context) (modelLive.PollDTO, error) {
		var out modelLive.PollDTO
		err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
			v, err := s.ownView(ctx, p, sessionID, viewID)
			if err != nil {
				return err
			}
			rows, err := s.storer.ProctorSignals(ctx, v.ID, after, pollLimit)
			if err != nil {
				return err
			}
			out = pollDTO(rows, after)
			now := s.now().UTC()
			if v.EndedAt == nil && !v.Active(now) {
				if err := s.end(ctx, p, v, modelLive.EndExpired, now, client); err != nil {
					return err
				}
				v.EndedAt = &now
			}
			out.Ended = v.EndedAt != nil
			return nil
		})
		return out, err
	}
	out, err := s.longPoll(ctx, viewKey(viewID), wait, read, func(d modelLive.PollDTO) bool { return len(d.Signals) > 0 || d.Ended })
	return out, fail(span, err)
}

// ParticipantSignal relays the participant's offer, ICE candidate or end to the proctor.
func (s *LiveViewService) ParticipantSignal(ctx context.Context, p modelAuth.Principal, attemptID string, in modelLive.SignalInput, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.ParticipantSignal", trace.WithAttributes(attribute.String("attempt.id", attemptID), attribute.String("signal.kind", string(in.Kind))))
	defer span.End()
	if err := checkSignal(in, modelLive.KindOffer, modelLive.KindICE, modelLive.KindEnd); err != nil {
		return fail(span, err)
	}
	if uuid.Validate(in.ViewID) != nil {
		return fail(span, &errr.ValidationError{Fields: map[string]string{"view_id": "must be a UUID"}})
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, err := s.ownAttempt(ctx, p, attemptID); err != nil {
			return err
		}
		v, err := s.storer.FindView(ctx, in.ViewID)
		if err != nil || v.AttemptID != attemptID {
			return errr.ErrNotFound
		}
		now := s.now().UTC()
		if !v.Active(now) {
			return errViewEnded
		}
		if in.Kind == modelLive.KindEnd {
			return s.end(ctx, p, v, modelLive.EndParticipant, now, client)
		}
		return s.signal(ctx, v, modelLive.RecipientProctor, in.Kind, in.Payload, now)
	})
	return fail(span, err)
}

// ParticipantPoll waits up to wait for messages to the participant after the cursor.
func (s *LiveViewService) ParticipantPoll(ctx context.Context, p modelAuth.Principal, attemptID string, after int64, wait time.Duration) (modelLive.PollDTO, error) {
	ctx, span := s.tracer.Start(ctx, "LiveViewService.ParticipantPoll", trace.WithAttributes(attribute.String("attempt.id", attemptID)))
	defer span.End()
	read := func(ctx context.Context) (modelLive.PollDTO, error) {
		var out modelLive.PollDTO
		err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
			a, err := s.ownAttempt(ctx, p, attemptID)
			if err != nil {
				return err
			}
			sess, err := s.storer.FindSession(ctx, a.SessionID)
			if err != nil {
				return err
			}
			if !sess.RequireCamera && !sess.RequireScreen {
				return errSharingOff
			}
			now := s.now().UTC()
			rows, err := s.storer.ParticipantSignals(ctx, attemptID, after, now, now.Add(-endGrace), pollLimit)
			if err != nil {
				return err
			}
			out = pollDTO(rows, after)
			return nil
		})
		return out, err
	}
	out, err := s.longPoll(ctx, attemptKey(attemptID), wait, read, func(d modelLive.PollDTO) bool { return len(d.Signals) > 0 })
	return out, fail(span, err)
}

// longPoll reads once; when there is nothing yet it waits (outside any transaction) for a
// notification on key or the timeout, then reads again.
func (s *LiveViewService) longPoll(ctx context.Context, key string, wait time.Duration, read func(context.Context) (modelLive.PollDTO, error), ready func(modelLive.PollDTO) bool) (modelLive.PollDTO, error) {
	wait = min(max(wait, 0), MaxPollWait)
	woken, unsubscribe := s.waker.Subscribe(key) // before reading: a commit in between still wakes us
	defer unsubscribe()
	out, err := read(ctx)
	if err != nil || ready(out) || wait == 0 {
		return out, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-woken:
	case <-timer.C:
		return out, nil
	case <-ctx.Done():
		return out, nil
	}
	return read(ctx)
}

func (s *LiveViewService) signal(ctx context.Context, v modelLive.ViewEntity, to modelLive.Recipient, kind modelLive.Kind, payload json.RawMessage, now time.Time) error {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if err := s.storer.AddSignal(ctx, &modelLive.SignalEntity{
		TenantID: v.TenantID, ViewID: v.ID, Recipient: to, Kind: kind, Payload: payload, CreatedAt: now,
	}); err != nil {
		return err
	}
	if to == modelLive.RecipientParticipant {
		return s.storer.Notify(ctx, attemptKey(v.AttemptID))
	}
	return s.storer.Notify(ctx, viewKey(v.ID))
}

// end closes a view once: both sides get "end", and the audit log records the duration.
func (s *LiveViewService) end(ctx context.Context, p modelAuth.Principal, v modelLive.ViewEntity, reason modelLive.EndReason, now time.Time, client modelAudit.Client) error {
	ended, err := s.storer.EndView(ctx, v.ID, reason, now)
	if err != nil || !ended {
		return err
	}
	body, _ := json.Marshal(map[string]string{"reason": string(reason)})
	if err := s.signal(ctx, v, modelLive.RecipientParticipant, modelLive.KindEnd, body, now); err != nil {
		return err
	}
	if err := s.signal(ctx, v, modelLive.RecipientProctor, modelLive.KindEnd, body, now); err != nil {
		return err
	}
	until := now
	if v.ExpiresAt.Before(until) {
		until = v.ExpiresAt
	}
	return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: v.TenantID, ActorID: p.UserID, Action: modelAudit.ActionLiveViewEnd,
		EntityType: "live_view", EntityID: v.ID, Client: client,
		After: map[string]any{"reason": reason, "proctor_id": v.ProctorID, "seconds": int(until.Sub(v.StartedAt).Seconds())},
	}))
}

// proctorSession: session managers, or proctors assigned to the session (others get 404).
func (s *LiveViewService) proctorSession(ctx context.Context, p modelAuth.Principal, sessionID string) (SessionRow, error) {
	if uuid.Validate(sessionID) != nil {
		return SessionRow{}, errr.ErrNotFound
	}
	if !p.Has(modelAuth.PermSessionManage) && !p.Has(modelAuth.PermSessionProctor) {
		return SessionRow{}, errr.ErrForbidden
	}
	sess, err := s.storer.FindSession(ctx, sessionID)
	if err != nil {
		return SessionRow{}, err
	}
	if !p.Has(modelAuth.PermSessionManage) {
		ok, err := s.storer.IsProctor(ctx, sessionID, p.UserID)
		if err != nil {
			return SessionRow{}, err
		}
		if !ok {
			return SessionRow{}, errr.ErrNotFound
		}
	}
	return sess, nil
}

// ownView: a view of the session opened by the caller. Views are personal: another proctor
// of the same session cannot read or steer it.
func (s *LiveViewService) ownView(ctx context.Context, p modelAuth.Principal, sessionID, viewID string) (modelLive.ViewEntity, error) {
	if uuid.Validate(viewID) != nil {
		return modelLive.ViewEntity{}, errr.ErrNotFound
	}
	if _, err := s.proctorSession(ctx, p, sessionID); err != nil {
		return modelLive.ViewEntity{}, err
	}
	v, err := s.storer.FindView(ctx, viewID)
	if err != nil || v.SessionID != sessionID || v.ProctorID != p.UserID {
		return modelLive.ViewEntity{}, errr.ErrNotFound
	}
	return v, nil
}

// ownAttempt: the caller's own attempt.
func (s *LiveViewService) ownAttempt(ctx context.Context, p modelAuth.Principal, attemptID string) (AttemptRow, error) {
	if uuid.Validate(attemptID) != nil {
		return AttemptRow{}, errr.ErrNotFound
	}
	a, err := s.storer.FindAttempt(ctx, attemptID)
	if err != nil || a.UserID != p.UserID {
		return AttemptRow{}, errr.ErrNotFound
	}
	return a, nil
}

func checkSignal(in modelLive.SignalInput, kinds ...modelLive.Kind) error {
	v := errr.Validation{}
	ok := false
	for _, k := range kinds {
		ok = ok || in.Kind == k
	}
	v.Check(ok, "kind", "not allowed here")
	v.Check(len(in.Payload) <= maxPayload, "payload", "too large")
	v.Check(len(in.Payload) == 0 || json.Valid(in.Payload), "payload", "must be JSON")
	return v.Err()
}

func pollDTO(rows []modelLive.SignalEntity, after int64) modelLive.PollDTO {
	out := modelLive.PollDTO{Signals: make([]modelLive.SignalDTO, 0, len(rows)), Cursor: after}
	for _, r := range rows {
		out.Signals = append(out.Signals, modelLive.ToSignalDTO(r))
		out.Cursor = max(out.Cursor, r.ID)
	}
	return out
}

func fail(span trace.Span, err error) error {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}
	return err
}
