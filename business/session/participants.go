package session

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSession "doexam/models/session"
	"doexam/pkg/errr"
)

// ListParticipants returns one page of a session's participants (managers and its proctors).
func (s *SessionService) ListParticipants(ctx context.Context, p modelAuth.Principal, sessionID string, f modelSession.ParticipantFilter) ([]modelSession.ParticipantDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.ListParticipants", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	if f.Attendance != "" && !validAttendance(f.Attendance) {
		return nil, 0, &errr.ValidationError{Fields: map[string]string{"attendance": "must be expected, present, absent or excused"}}
	}
	var (
		out   []modelSession.ParticipantDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.load(ctx, p, sessionID, false); err != nil {
			return err
		}
		var err error
		out, total, err = s.storer.ListParticipants(ctx, sessionID, f)
		return err
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	return out, total, nil
}

// AddParticipants adds users and plain members of groups (session.manage). Users already
// in the session are left unchanged, so re-adding a group after it changed is safe.
func (s *SessionService) AddParticipants(ctx context.Context, p modelAuth.Principal, sessionID string, in modelSession.AddParticipantsInput, client modelAudit.Client) (modelSession.AddResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.AddParticipants", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	userIDs, err := cleanIDs(in.UserIDs, "user_ids", modelSession.MaxParticipantsPerRequest)
	if err != nil {
		return modelSession.AddResultDTO{}, fail(span, err)
	}
	groupIDs, err := cleanIDs(in.GroupIDs, "group_ids", 100)
	if err != nil {
		return modelSession.AddResultDTO{}, fail(span, err)
	}
	v := errr.Validation{}
	v.Check(len(userIDs)+len(groupIDs) > 0, "user_ids", "give user_ids and/or group_ids")
	v.Check(in.ExtraTimeMinutes >= 0 && in.ExtraTimeMinutes <= 600, "extra_time_minutes", "0-600")
	if err := v.Err(); err != nil {
		return modelSession.AddResultDTO{}, fail(span, err)
	}

	var out modelSession.AddResultDTO
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, _, err := s.loadManaged(ctx, p, sessionID)
		if err != nil {
			return err
		}
		if sess.Status == modelSession.StatusClosed || sess.Status == modelSession.StatusCancelled {
			return errr.WithMessage(errr.ErrConflict, "closed or cancelled sessions cannot be changed")
		}
		active, err := s.storer.ActiveUserIDs(ctx, userIDs)
		if err != nil {
			return err
		}
		if missing := diff(userIDs, active); len(missing) > 0 {
			return &errr.ValidationError{Fields: map[string]string{"user_ids": "unknown or suspended users: " + short(missing)}}
		}
		groups, err := s.storer.ExistingGroupIDs(ctx, groupIDs)
		if err != nil {
			return err
		}
		if missing := diff(groupIDs, groups); len(missing) > 0 {
			return &errr.ValidationError{Fields: map[string]string{"group_ids": "unknown groups: " + short(missing)}}
		}
		members, err := s.storer.GroupMemberIDs(ctx, groupIDs)
		if err != nil {
			return err
		}
		all := userIDs
		for _, m := range members {
			if !containsStr(all, m) {
				all = append(all, m)
			}
		}
		if len(all) > modelSession.MaxParticipantsPerRequest {
			return &errr.ValidationError{Fields: map[string]string{"group_ids": fmt.Sprintf("max %d participants per request", modelSession.MaxParticipantsPerRequest)}}
		}
		now := s.now().UTC()
		rows := make([]modelSession.ParticipantEntity, len(all))
		for i, uid := range all {
			rows[i] = modelSession.ParticipantEntity{
				SessionID: sessionID, UserID: uid, TenantID: p.TenantID, ExtraTimeMinutes: in.ExtraTimeMinutes,
				Attendance: modelSession.AttendanceExpected, AddedAt: now,
			}
		}
		added, err := s.storer.AddParticipants(ctx, rows)
		if err != nil {
			return err
		}
		links := make([]modelSession.SessionGroupEntity, len(groupIDs))
		for i, gid := range groupIDs {
			links[i] = modelSession.SessionGroupEntity{SessionID: sessionID, GroupID: gid, TenantID: p.TenantID}
		}
		if err := s.storer.AddSessionGroups(ctx, links); err != nil {
			return err
		}
		out = modelSession.AddResultDTO{Added: int(added), Skipped: len(all) - int(added)}
		return s.audit(ctx, p, client, modelAudit.ActionParticipantAdd, sessionID, nil,
			map[string]any{"user_ids": userIDs, "group_ids": groupIDs, "added": added})
	})
	if err != nil {
		return modelSession.AddResultDTO{}, fail(span, err)
	}
	return out, nil
}

// UpdateParticipant records attendance, seat and notes (managers and proctors) or
// extra time (accommodation; managers only).
func (s *SessionService) UpdateParticipant(ctx context.Context, p modelAuth.Principal, sessionID, userID string, in modelSession.ParticipantUpdateInput, client modelAudit.Client) (modelSession.ParticipantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.UpdateParticipant", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	var out modelSession.ParticipantDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, r, err := s.load(ctx, p, sessionID, false)
		if err != nil {
			return err
		}
		if sess.Status == modelSession.StatusCancelled {
			return errr.WithMessage(errr.ErrConflict, "cancelled sessions cannot be changed")
		}
		if in.ExtraTimeMinutes != nil && r != roleManager {
			return errr.WithMessage(errr.ErrForbidden, "extra time needs the session.manage permission")
		}
		if cleaned, err := cleanIDs([]string{userID}, "user_id", 1); err != nil || len(cleaned) == 0 {
			return errParticipantNotFound
		}
		part, err := s.storer.FindParticipant(ctx, sessionID, strings.ToLower(userID))
		if err != nil {
			return err
		}
		before := part
		v := errr.Validation{}
		if in.ExtraTimeMinutes != nil {
			part.ExtraTimeMinutes = *in.ExtraTimeMinutes
			v.Check(part.ExtraTimeMinutes >= 0 && part.ExtraTimeMinutes <= 600, "extra_time_minutes", "0-600")
		}
		if in.Attendance != nil {
			part.Attendance = *in.Attendance
			v.Check(validAttendance(part.Attendance), "attendance", "must be expected, present, absent or excused")
		}
		if in.Seat != nil {
			seat := strings.TrimSpace(*in.Seat)
			v.Check(utf8.RuneCountInString(seat) <= 50, "seat", "max 50 chars")
			part.Seat = optional(seat)
		}
		if in.Notes != nil {
			notes := strings.TrimSpace(*in.Notes)
			v.Check(utf8.RuneCountInString(notes) <= 1000, "notes", "max 1000 chars")
			part.Notes = optional(notes)
		}
		if err := v.Err(); err != nil {
			return err
		}
		if err := s.storer.UpdateParticipant(ctx, &part); err != nil {
			return err
		}
		rows, _, err := s.storer.ListParticipants(ctx, sessionID, modelSession.ParticipantFilter{Page: 1, PerPage: 1, UserID: part.UserID})
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			out = rows[0]
		}
		return s.audit(ctx, p, client, modelAudit.ActionParticipantUpdate, sessionID,
			participantAudit(before), participantAudit(part))
	})
	if err != nil {
		return modelSession.ParticipantDTO{}, fail(span, err)
	}
	return out, nil
}

// RemoveParticipant removes a participant who has not started any attempt (session.manage).
func (s *SessionService) RemoveParticipant(ctx context.Context, p modelAuth.Principal, sessionID, userID string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "SessionService.RemoveParticipant", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.loadManaged(ctx, p, sessionID); err != nil {
			return err
		}
		ids, err := cleanIDs([]string{userID}, "user_id", 1)
		if err != nil || len(ids) == 0 {
			return errParticipantNotFound
		}
		if _, err := s.storer.FindParticipant(ctx, sessionID, ids[0]); err != nil {
			return err
		}
		n, err := s.storer.CountAttempts(ctx, sessionID, ids[0])
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "the participant already started the exam")
		}
		if err := s.storer.DeleteParticipant(ctx, sessionID, ids[0]); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionParticipantRemove, sessionID, map[string]any{"user_id": ids[0]}, nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

func participantAudit(p modelSession.ParticipantEntity) map[string]any {
	return map[string]any{"user_id": p.UserID, "extra_time_minutes": p.ExtraTimeMinutes, "attendance": p.Attendance, "seat": p.Seat}
}

func validAttendance(a modelSession.Attendance) bool {
	switch a {
	case modelSession.AttendanceExpected, modelSession.AttendancePresent, modelSession.AttendanceAbsent, modelSession.AttendanceExcused:
		return true
	}
	return false
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
