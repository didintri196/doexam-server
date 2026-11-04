package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSession "doexam/models/session"
	"doexam/pkg/errr"
)

// importantUnreadAfter is how long an important announcement may stay unconfirmed
// before the monitor points at the participant.
const importantUnreadAfter = time.Minute

var (
	errAnnouncementNotFound = errr.WithMessage(errr.ErrNotFound, "announcement not found")
	errSessionEnded         = errr.WithMessage(errr.ErrConflict, "the session has ended")
	errTooManyAnnouncements = errr.WithMessage(errr.ErrConflict, "too many announcements, try again in a minute")
)

// Announce sends an announcement to every participant of a session or to some of them
// (the session's proctors and session managers). Recipients are fixed when it is sent.
func (s *SessionService) Announce(ctx context.Context, p modelAuth.Principal, sessionID string, in modelSession.AnnouncementInput, client modelAudit.Client) (modelSession.SendResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Announce", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	in.Normalize()
	if err := validateAnnouncement(in.Body, in.Priority, in.Target, in.UserIDs); err != nil {
		return modelSession.SendResultDTO{}, fail(span, err)
	}
	var out modelSession.SendResultDTO
	now := s.now().UTC()
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.load(ctx, p, sessionID, false)
		if err != nil {
			return err
		}
		a, err := s.send(ctx, p, sess, in.Body, in.Priority, in.Target, in.UserIDs, now, client)
		if err != nil {
			return err
		}
		out = modelSession.SendResultDTO{ID: a.ID, Recipients: a.Recipients}
		if in.Target == modelSession.TargetSelected {
			out.Skipped = len(uniqueIDs(in.UserIDs)) - a.Recipients
		}
		return nil
	})
	if err != nil {
		return modelSession.SendResultDTO{}, fail(span, err)
	}
	s.board.Touch(sessionID, now)
	return out, nil
}

// Broadcast sends one announcement to every participant of several sessions: the given
// ones, or every open or paused session (session.manage only).
func (s *SessionService) Broadcast(ctx context.Context, p modelAuth.Principal, in modelSession.BroadcastInput, client modelAudit.Client) (modelSession.BroadcastResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.Broadcast")
	defer span.End()

	if !p.Has(modelAuth.PermSessionManage) {
		return modelSession.BroadcastResultDTO{}, fail(span, errr.ErrForbidden)
	}
	one := modelSession.AnnouncementInput{Body: in.Body, Priority: in.Priority, Target: modelSession.TargetAll}
	one.Normalize()
	v := errr.Validation{}
	ids := uniqueIDs(in.SessionIDs)
	if !in.Running {
		v.Check(len(ids) > 0, "session_ids", "required unless running is true")
		v.Check(len(ids) <= modelSession.MaxAnnouncementSessions, "session_ids", fmt.Sprintf("max %d sessions", modelSession.MaxAnnouncementSessions))
	}
	if err := v.Err(); err != nil {
		return modelSession.BroadcastResultDTO{}, fail(span, err)
	}
	if err := validateAnnouncement(one.Body, one.Priority, one.Target, nil); err != nil {
		return modelSession.BroadcastResultDTO{}, fail(span, err)
	}

	var out modelSession.BroadcastResultDTO
	var sent []string
	now := s.now().UTC()
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if in.Running {
			var err error
			if ids, err = s.storer.RunningSessionIDs(ctx); err != nil {
				return err
			}
			if len(ids) > modelSession.MaxAnnouncementSessions {
				return &errr.ValidationError{Fields: map[string]string{"running": fmt.Sprintf("more than %d running sessions", modelSession.MaxAnnouncementSessions)}}
			}
		}
		for _, id := range ids {
			sess, _, err := s.load(ctx, p, id, false)
			if err != nil {
				return err
			}
			a, err := s.send(ctx, p, sess, one.Body, one.Priority, modelSession.TargetAll, nil, now, client)
			if err != nil {
				return fmt.Errorf("session %s: %w", id, err)
			}
			out.Sessions++
			out.Recipients += a.Recipients
			sent = append(sent, id)
		}
		return nil
	})
	if err != nil {
		return modelSession.BroadcastResultDTO{}, fail(span, err)
	}
	for _, id := range sent {
		s.board.Touch(id, now)
	}
	return out, nil
}

// send writes one announcement with its recipients and audit entry inside the caller's
// transaction. userIDs is used for TargetSelected only.
func (s *SessionService) send(ctx context.Context, p modelAuth.Principal, sess modelSession.SessionEntity, body string, priority modelSession.AnnouncementPriority,
	target modelSession.AnnouncementTarget, userIDs []string, now time.Time, client modelAudit.Client) (modelSession.AnnouncementEntity, error) {
	if sess.Status != modelSession.StatusScheduled && sess.Status != modelSession.StatusOpen && sess.Status != modelSession.StatusPaused {
		return modelSession.AnnouncementEntity{}, errSessionEnded
	}
	recent, err := s.storer.CountRecentAnnouncements(ctx, sess.ID, now.Add(-time.Minute))
	if err != nil {
		return modelSession.AnnouncementEntity{}, err
	}
	if recent >= modelSession.MaxAnnouncementsPerMinute {
		return modelSession.AnnouncementEntity{}, errTooManyAnnouncements
	}
	a := modelSession.AnnouncementEntity{
		ID: newID(), TenantID: p.TenantID, SessionID: sess.ID, SenderID: p.UserID,
		Body: body, Priority: priority, Target: target, CreatedAt: now,
	}
	if err := s.storer.CreateAnnouncement(ctx, &a); err != nil {
		return modelSession.AnnouncementEntity{}, err
	}
	var only []string // nil = every participant
	if target == modelSession.TargetSelected {
		only = uniqueIDs(userIDs)
	}
	n, err := s.storer.AddAnnouncementRecipients(ctx, a.TenantID, a.ID, sess.ID, only)
	if err != nil {
		return modelSession.AnnouncementEntity{}, err
	}
	a.Recipients = int(n)
	if err := s.storer.SetAnnouncementRecipients(ctx, a.ID, a.Recipients); err != nil {
		return modelSession.AnnouncementEntity{}, err
	}
	// The body is in the audit log (who told the room what), never in application logs.
	return a, s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionAnnouncementSend,
		EntityType: "session_announcement", EntityID: a.ID, Client: client,
		After: map[string]any{"session_id": sess.ID, "body": body, "priority": priority, "target": target, "recipients": a.Recipients},
	}))
}

// ListAnnouncements returns a session's announcements (newest first) with read counts,
// and the participants who leave an important announcement unconfirmed.
func (s *SessionService) ListAnnouncements(ctx context.Context, p modelAuth.Principal, sessionID string) (modelSession.AnnouncementListDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.ListAnnouncements", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	var out modelSession.AnnouncementListDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.load(ctx, p, sessionID, false); err != nil {
			return err
		}
		var err error
		if out.Announcements, err = s.storer.ListAnnouncements(ctx, sessionID); err != nil {
			return err
		}
		out.UnreadImportant, err = s.storer.UnreadImportant(ctx, sessionID, s.now().UTC().Add(-importantUnreadAfter))
		return err
	})
	if err != nil {
		return modelSession.AnnouncementListDTO{}, fail(span, err)
	}
	return out, nil
}

// AnnouncementRecipients lists who received an announcement and who read it.
func (s *SessionService) AnnouncementRecipients(ctx context.Context, p modelAuth.Principal, sessionID, announcementID string) ([]modelSession.RecipientDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.AnnouncementRecipients", trace.WithAttributes(attribute.String("announcement.id", announcementID)))
	defer span.End()

	var out []modelSession.RecipientDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, err := s.loadAnnouncement(ctx, p, sessionID, announcementID); err != nil {
			return err
		}
		var err error
		out, err = s.storer.ListRecipients(ctx, announcementID)
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// RetractAnnouncement withdraws an announcement (its sender or a session manager).
// Participants then see that it was withdrawn instead of its text.
func (s *SessionService) RetractAnnouncement(ctx context.Context, p modelAuth.Principal, sessionID, announcementID string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "SessionService.RetractAnnouncement", trace.WithAttributes(attribute.String("announcement.id", announcementID)))
	defer span.End()

	now := s.now().UTC()
	changed := false
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		a, err := s.loadAnnouncement(ctx, p, sessionID, announcementID)
		if err != nil {
			return err
		}
		if a.SenderID != p.UserID && !p.Has(modelAuth.PermSessionManage) {
			return errr.WithMessage(errr.ErrForbidden, "only the sender or a session manager can retract")
		}
		if a.RetractedAt != nil {
			return nil
		}
		if err := s.storer.RetractAnnouncement(ctx, a.ID, now); err != nil {
			return err
		}
		changed = true
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionAnnouncementRetract,
			EntityType: "session_announcement", EntityID: a.ID, Client: client,
			Before: map[string]any{"session_id": a.SessionID, "body": a.Body},
		}))
	})
	if err != nil {
		return fail(span, err)
	}
	if changed {
		s.board.Touch(sessionID, now)
	}
	return nil
}

// MyAnnouncements returns the caller's announcements in a session they take part in,
// with the cursor the attempt heartbeat reports for that session.
func (s *SessionService) MyAnnouncements(ctx context.Context, p modelAuth.Principal, sessionID string) (modelSession.MyAnnouncementsDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.MyAnnouncements", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	out := modelSession.MyAnnouncementsDTO{Announcements: []modelSession.MyAnnouncementDTO{}}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.ownSession(ctx, p, sessionID); err != nil {
			return err
		}
		var err error
		// the cursor is read first: an announcement arriving in between makes the next
		// heartbeat report a newer cursor, never hides it
		if out.Cursor, err = s.board.Cursor(ctx, sessionID); err != nil {
			return err
		}
		out.Announcements, err = s.storer.MyAnnouncements(ctx, sessionID, p.UserID)
		return err
	})
	if err != nil {
		return modelSession.MyAnnouncementsDTO{}, fail(span, err)
	}
	return out, nil
}

// ReadAnnouncement records that the caller confirmed an announcement (used before an
// attempt exists; during an attempt the confirmation travels as an ANNOUNCEMENT_READ event).
func (s *SessionService) ReadAnnouncement(ctx context.Context, p modelAuth.Principal, announcementID string) error {
	ctx, span := s.tracer.Start(ctx, "SessionService.ReadAnnouncement", trace.WithAttributes(attribute.String("announcement.id", announcementID)))
	defer span.End()

	if !p.Has(modelAuth.PermAttemptTake) {
		return fail(span, errr.ErrForbidden)
	}
	if uuid.Validate(announcementID) != nil {
		return fail(span, errAnnouncementNotFound)
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		ok, err := s.storer.IsAnnouncementRecipient(ctx, announcementID, p.UserID)
		if err != nil {
			return err
		}
		if !ok {
			return errAnnouncementNotFound
		}
		return s.board.MarkRead(ctx, announcementID, p.UserID, s.now().UTC())
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// loadAnnouncement checks session access and that the announcement belongs to it.
func (s *SessionService) loadAnnouncement(ctx context.Context, p modelAuth.Principal, sessionID, announcementID string) (modelSession.AnnouncementEntity, error) {
	if _, _, err := s.load(ctx, p, sessionID, false); err != nil {
		return modelSession.AnnouncementEntity{}, err
	}
	if uuid.Validate(announcementID) != nil {
		return modelSession.AnnouncementEntity{}, errAnnouncementNotFound
	}
	a, err := s.storer.FindAnnouncement(ctx, announcementID)
	if errr.IsNotFound(err) || (err == nil && a.SessionID != sessionID) {
		return modelSession.AnnouncementEntity{}, errAnnouncementNotFound
	}
	return a, err
}

// ownSession checks that the caller is a participant of the session (404 otherwise).
func (s *SessionService) ownSession(ctx context.Context, p modelAuth.Principal, sessionID string) error {
	if !p.Has(modelAuth.PermAttemptTake) {
		return errr.ErrForbidden
	}
	if uuid.Validate(sessionID) != nil {
		return errSessionNotFound
	}
	if _, err := s.storer.FindParticipant(ctx, sessionID, p.UserID); err != nil {
		if errr.IsNotFound(err) {
			return errSessionNotFound
		}
		return err
	}
	return nil
}

func validateAnnouncement(body string, priority modelSession.AnnouncementPriority, target modelSession.AnnouncementTarget, userIDs []string) error {
	v := errr.Validation{}
	n := utf8.RuneCountInString(body)
	v.Check(n > 0 && n <= modelSession.MaxAnnouncementLen, "body", fmt.Sprintf("required, max %d chars", modelSession.MaxAnnouncementLen))
	v.Check(priority == modelSession.PriorityInfo || priority == modelSession.PriorityImportant, "priority", "must be info or important")
	v.Check(target == modelSession.TargetAll || target == modelSession.TargetSelected, "target", "must be all or selected")
	if target == modelSession.TargetSelected {
		v.Check(len(userIDs) > 0, "user_ids", "required for target selected")
		v.Check(len(userIDs) <= modelSession.MaxParticipantsPerRequest, "user_ids", fmt.Sprintf("max %d", modelSession.MaxParticipantsPerRequest))
		v.Check(!slices.ContainsFunc(userIDs, func(id string) bool { return uuid.Validate(id) != nil }), "user_ids", "must be UUIDs")
	}
	return v.Err()
}

func uniqueIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.ToLower(strings.TrimSpace(id))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
