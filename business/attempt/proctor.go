package attempt

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/errr"
)

// Extend adds minutes to a running attempt (and its current timed section), never past the
// session end; extend the session first when more time is needed.
func (s *AttemptService) Extend(ctx context.Context, p modelAuth.Principal, id string, in modelAttempt.ExtendInput, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	if in.Minutes < 1 || in.Minutes > 240 {
		return modelAttempt.AttemptView{}, &errr.ValidationError{Fields: map[string]string{"minutes": "1-240"}}
	}
	return s.proctorAction(ctx, p, id, "AttemptService.Extend", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, sess SessionInfo) error {
		add := time.Duration(in.Minutes) * time.Minute
		before := a.DeadlineAt
		a.DeadlineAt = minTime(a.DeadlineAt.Add(add), sess.EndsAt)
		if !a.DeadlineAt.After(before) {
			return errr.WithMessage(errr.ErrConflict, "the attempt already ends with the session; extend the session first")
		}
		if err := s.storer.UpdateAttempt(ctx, a); err != nil {
			return err
		}
		sections, err := s.storer.ListAttemptSections(ctx, a.ID)
		if err != nil {
			return err
		}
		for i := range sections {
			sec := &sections[i]
			if sectionState(*sec) == stateActive && sec.DeadlineAt != nil {
				d := minTime(sec.DeadlineAt.Add(add), a.DeadlineAt)
				sec.DeadlineAt = &d
				if err := s.storer.UpdateAttemptSection(ctx, sec); err != nil {
					return err
				}
			}
		}
		return s.event(ctx, *a, modelAttempt.EventTimeExtended, &p.UserID,
			map[string]any{"minutes": in.Minutes, "deadline_at": a.DeadlineAt}, client)
	})
}

// GrantPlays lets a participant play every limited audio clip `plays` more times
// (e.g. the speaker failed). Clips without a limit are unaffected.
func (s *AttemptService) GrantPlays(ctx context.Context, p modelAuth.Principal, id string, in modelAttempt.GrantPlaysInput, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	v := errr.Validation{}
	v.Check(in.Plays >= 1 && in.Plays <= 5, "plays", "1-5")
	if err := v.Err(); err != nil {
		return modelAttempt.AttemptView{}, err
	}
	return s.proctorAction(ctx, p, id, "AttemptService.GrantPlays", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, _ SessionInfo) error {
		return s.event(ctx, *a, modelAttempt.EventMediaPlaysGranted, &p.UserID, map[string]int{"plays": in.Plays}, client)
	})
}

// Lock stops a participant from answering (the timer keeps running).
func (s *AttemptService) Lock(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	return s.proctorAction(ctx, p, id, "AttemptService.Lock", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, _ SessionInfo) error {
		if a.Status != modelAttempt.StatusInProgress {
			return errr.WithMessage(errr.ErrConflict, "only attempts in progress can be locked")
		}
		a.Status = modelAttempt.StatusLocked
		if err := s.storer.UpdateAttempt(ctx, a); err != nil {
			return err
		}
		return s.event(ctx, *a, modelAttempt.EventLocked, &p.UserID, nil, client)
	})
}

// Unlock lets a locked participant continue.
func (s *AttemptService) Unlock(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	return s.proctorAction(ctx, p, id, "AttemptService.Unlock", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, _ SessionInfo) error {
		if a.Status != modelAttempt.StatusLocked {
			return errr.WithMessage(errr.ErrConflict, "the attempt is not locked")
		}
		a.Status = modelAttempt.StatusInProgress
		if err := s.storer.UpdateAttempt(ctx, a); err != nil {
			return err
		}
		return s.event(ctx, *a, modelAttempt.EventUnlocked, &p.UserID, nil, client)
	})
}

// ResetDevice unbinds the attempt from its device so the participant can continue elsewhere.
func (s *AttemptService) ResetDevice(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	return s.proctorAction(ctx, p, id, "AttemptService.ResetDevice", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, _ SessionInfo) error {
		a.ActiveDeviceID = nil
		if err := s.storer.UpdateAttempt(ctx, a); err != nil {
			return err
		}
		return s.event(ctx, *a, modelAttempt.EventDeviceReset, &p.UserID, nil, client)
	})
}

// ForceSubmit hands in a participant's attempt on the proctor's behalf.
func (s *AttemptService) ForceSubmit(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	return s.proctorAction(ctx, p, id, "AttemptService.ForceSubmit", client, func(ctx context.Context, a *modelAttempt.AttemptEntity, _ SessionInfo) error {
		return s.finalize(ctx, a, modelAttempt.ReasonProctor, &p.UserID, client)
	})
}

// FinalizeSessionAttempts submits every running attempt of a session (reason session_closed).
// It runs inside the caller's tenant transaction (the session service calls it while closing).
func (s *AttemptService) FinalizeSessionAttempts(ctx context.Context, sessionID, actorID string) (int, error) {
	running, err := s.storer.RunningAttempts(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	for i := range running {
		if err := s.finalize(ctx, &running[i], modelAttempt.ReasonSessionClosed, &actorID, modelAudit.Client{}); err != nil {
			return 0, err
		}
	}
	if len(running) > 0 {
		s.logger.InfoContext(ctx, "session closed: attempts submitted", slog.String("session_id", sessionID), slog.Int("count", len(running)))
	}
	return len(running), nil
}

// proctorAction loads an attempt for a proctor of its session (or a session manager), applies
// the clock, runs fn on a running attempt and returns the attempt view.
func (s *AttemptService) proctorAction(ctx context.Context, p modelAuth.Principal, id, spanName string, client modelAudit.Client,
	fn func(ctx context.Context, a *modelAttempt.AttemptEntity, sess SessionInfo) error) (modelAttempt.AttemptView, error) {
	ctx, span := s.tracer.Start(ctx, spanName, trace.WithAttributes(attribute.String("attempt.id", id)))
	defer span.End()

	var out modelAttempt.AttemptView
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if uuid.Validate(id) != nil {
			return errAttemptNotFound
		}
		a, err := s.storer.FindAttempt(ctx, id, true)
		if err != nil {
			return err
		}
		sess, err := s.proctorSession(ctx, p, a.SessionID)
		if err != nil {
			if errr.IsNotFound(err) {
				return errAttemptNotFound
			}
			return err
		}
		if err := s.tick(ctx, &a, client); err != nil {
			return err
		}
		if a.Status != modelAttempt.StatusInProgress && a.Status != modelAttempt.StatusLocked {
			return errr.WithMessage(errr.ErrConflict, "the attempt is already submitted")
		}
		if err := fn(ctx, &a, sess); err != nil {
			return err
		}
		out, err = s.view(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.AttemptView{}, fail(span, err)
	}
	s.metrics.proctorAction(ctx, strings.TrimPrefix(spanName, "AttemptService."))
	return out, nil
}

// proctorSession checks the caller manages sessions or proctors this one (else "not found").
func (s *AttemptService) proctorSession(ctx context.Context, p modelAuth.Principal, sessionID string) (SessionInfo, error) {
	if uuid.Validate(sessionID) != nil {
		return SessionInfo{}, errSessionNotFound
	}
	if !p.Has(modelAuth.PermSessionManage) && !p.Has(modelAuth.PermSessionProctor) {
		return SessionInfo{}, errr.ErrForbidden
	}
	sess, err := s.storer.FindSession(ctx, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	if !p.Has(modelAuth.PermSessionManage) {
		ok, err := s.storer.IsProctor(ctx, sessionID, p.UserID)
		if err != nil {
			return SessionInfo{}, err
		}
		if !ok {
			return SessionInfo{}, errSessionNotFound
		}
	}
	return sess, nil
}
