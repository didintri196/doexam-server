package attempt

import (
	"context"
	"slices"
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

// monitorIdleAfter: a running attempt without a sync for this long shows as idle.
const monitorIdleAfter = 2 * time.Minute

// MonitorList returns one page of a monitor view (DOCS/PLAN/15-MONITOR-KARTU.md) with the
// session's counts within the group/seat scope, and how many rows the view has.
func (s *AttemptService) MonitorList(ctx context.Context, p modelAuth.Principal, sessionID string, f modelAttempt.MonitorFilter, client modelAudit.Client) (modelAttempt.MonitorDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.MonitorList", trace.WithAttributes(attribute.String("session.id", sessionID), attribute.String("monitor.view", string(f.View))))
	defer span.End()

	if f.View == "" {
		f.View = modelAttempt.ViewAll
	}
	f.Seat, f.Query = strings.TrimSpace(f.Seat), strings.TrimSpace(f.Query)
	v := errr.Validation{}
	v.Check(slices.Contains(modelAttempt.MonitorViews, f.View), "view", "unknown view")
	v.Check(f.ViolationType == "" || slices.Contains(modelAttempt.ViolationTypes, f.ViolationType), "violation_type", "unknown violation type")
	v.Check(f.GroupID == "" || uuid.Validate(f.GroupID) == nil, "group_id", "must be a UUID")
	v.Check(len(f.Seat) <= 50, "seat", "at most 50 characters")
	v.Check(len(f.Query) <= 100, "q", "at most 100 characters")
	if err := v.Err(); err != nil {
		return modelAttempt.MonitorDTO{}, 0, err
	}

	var out modelAttempt.MonitorDTO
	var total int64
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, now, err := s.monitorSession(ctx, p, sessionID, client)
		if err != nil {
			return err
		}
		res, err := s.storer.MonitorPage(ctx, sessionID, f, now.Add(-monitorIdleAfter))
		if err != nil {
			return err
		}
		out, total = monitorDTO(sess, now, res.Counts, res.Rows), res.Total
		return nil
	})
	if err != nil {
		return modelAttempt.MonitorDTO{}, 0, fail(span, err)
	}
	return out, total, nil
}

// MonitorWatch returns the participants a proctor watches (at most MaxWatch, violators
// first) with the session's counts. IDs that are not participants of the session are ignored.
func (s *AttemptService) MonitorWatch(ctx context.Context, p modelAuth.Principal, sessionID string, in modelAttempt.WatchInput, client modelAudit.Client) (modelAttempt.MonitorDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.MonitorWatch", trace.WithAttributes(attribute.String("session.id", sessionID), attribute.Int("monitor.watch", len(in.UserIDs))))
	defer span.End()

	ids := make([]string, 0, len(in.UserIDs))
	v := errr.Validation{}
	for _, id := range in.UserIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if uuid.Validate(id) != nil {
			v.Check(false, "user_ids", "must be UUIDs")
			break
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	v.Check(len(ids) <= modelAttempt.MaxWatch, "user_ids", "at most 25 participants")
	if err := v.Err(); err != nil {
		return modelAttempt.MonitorDTO{}, err
	}

	var out modelAttempt.MonitorDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		sess, now, err := s.monitorSession(ctx, p, sessionID, client)
		if err != nil {
			return err
		}
		f := modelAttempt.MonitorFilter{UserIDs: ids}
		if len(ids) == 0 {
			f = modelAttempt.MonitorFilter{View: modelAttempt.ViewAll, Page: 1, PerPage: 0} // counts only
		}
		res, err := s.storer.MonitorPage(ctx, sessionID, f, now.Add(-monitorIdleAfter))
		if err != nil {
			return err
		}
		out = monitorDTO(sess, now, res.Counts, res.Rows)
		return nil
	})
	if err != nil {
		return modelAttempt.MonitorDTO{}, fail(span, err)
	}
	return out, nil
}

// monitorSession checks access and finalizes overdue attempts first (like Monitor), so the
// board never shows stale timers. Only attempts tick may change are locked.
func (s *AttemptService) monitorSession(ctx context.Context, p modelAuth.Principal, sessionID string, client modelAudit.Client) (SessionInfo, time.Time, error) {
	sess, err := s.proctorSession(ctx, p, sessionID)
	if err != nil {
		return SessionInfo{}, time.Time{}, err
	}
	now := s.now().UTC()
	overdue, err := s.storer.OverdueAttempts(ctx, sessionID, now.Add(-syncGrace))
	if err != nil {
		return SessionInfo{}, time.Time{}, err
	}
	for i := range overdue {
		if err := s.tick(ctx, &overdue[i], client); err != nil {
			return SessionInfo{}, time.Time{}, err
		}
	}
	return sess, now, nil
}

func monitorDTO(sess SessionInfo, now time.Time, counts map[string]int, rows []MonitorAttempt) modelAttempt.MonitorDTO {
	out := modelAttempt.MonitorDTO{SessionID: sess.ID, Status: sess.Status, ServerTime: now, Counts: counts, Rows: make([]modelAttempt.MonitorRow, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, modelAttempt.MonitorRow{
			UserID: r.UserID, Username: r.Username, FullName: r.FullName, Seat: r.Seat, Attendance: r.Attendance,
			AttemptID: r.AttemptID, Status: r.Status, Answered: r.Answered, ItemCount: r.ItemCount,
			DeadlineAt: r.DeadlineAt, LastSeenAt: r.LastSeenAt, Violations: r.FocusLost + r.FullscreenExit + r.DeviceChanged,
			DeviceBound: r.DeviceID != nil, FinalScore: r.FinalScore, Idle: r.Idle,
			ViolationCounts: map[string]int{
				modelAttempt.ViolationFocusLost: r.FocusLost, modelAttempt.ViolationFullscreenExit: r.FullscreenExit,
				modelAttempt.ViolationDeviceChanged: r.DeviceChanged,
			},
			LastViolationAt: r.LastViolationAt, LockedAt: r.LockedAt, SubmittedAt: r.SubmittedAt,
		})
	}
	return out
}
