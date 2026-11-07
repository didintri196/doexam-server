package attempt

import (
	"context"
	"fmt"
	"strings"
	"time"

	modelAttempt "doexam/models/attempt"
	dbpg "doexam/pkg/db-pg"
)

// Monitor views in SQL (DOCS/PLAN/15-MONITOR-KARTU.md §5). Every query starts from the same
// rows: the session's participants (scoped by group/seat/search/ids), their latest attempt,
// and per-attempt violation and lock aggregates read through attempt_events_monitor_idx, so
// heartbeats and answers are never scanned. Pages are cut in SQL; the costly answer counts
// are computed only for the rows of the page.

// seatOrder sorts seats naturally ("A-2" before "A-12") without an ICU collation (the bundled
// PostgreSQL may lack ICU): letters first, then the number; participants without a seat last.
const seatOrder = `seat IS NULL, regexp_replace(coalesce(seat, ''), '[0-9]+', '', 'g'),
	nullif(regexp_replace(coalesce(seat, ''), '[^0-9]', '', 'g'), '')::numeric NULLS FIRST, full_name, user_id`

// monitorViews maps each view to its row filter and order. Values are constants, never input.
var monitorViews = map[modelAttempt.MonitorView]struct{ where, order string }{
	modelAttempt.ViewAll:        {"true", seatOrder},
	modelAttempt.ViewViolations: {"violations > 0", "violations DESC, last_violation_at DESC NULLS LAST, " + seatOrder},
	modelAttempt.ViewIdle:       {"idle", "last_seen_at, " + seatOrder},
	modelAttempt.ViewLocked:     {"status = 'locked'", "locked_at NULLS LAST, " + seatOrder},
	modelAttempt.ViewNotStarted: {"attempt_id IS NULL", seatOrder},
	modelAttempt.ViewInProgress: {"status = 'in_progress'", seatOrder},
	modelAttempt.ViewSubmitted:  {"status IN ('submitted', 'graded')", "submitted_at DESC NULLS LAST, " + seatOrder},
}

// violationColumns maps a violation_type filter to its aggregate column.
var violationColumns = map[string]string{
	modelAttempt.ViolationFocusLost:      "focus_lost",
	modelAttempt.ViolationFullscreenExit: "fullscreen_exit",
	modelAttempt.ViolationDeviceChanged:  "device_changed",
}

// monitorRowsSQL is the WITH clause that defines "rows"; scope filters come from f.
func monitorRowsSQL(sessionID string, f modelAttempt.MonitorFilter, idleBefore time.Time) (string, []any) {
	where := []string{"sp.session_id = ?"}
	args := []any{sessionID}
	if f.GroupID != "" {
		where = append(where, "EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = ? AND gm.user_id = sp.user_id)")
		args = append(args, f.GroupID)
	}
	if f.Seat != "" {
		where = append(where, "sp.seat ILIKE ?")
		args = append(args, dbpg.PrefixPattern(f.Seat))
	}
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		where = append(where, "(u.full_name ILIKE ? OR u.username ILIKE ? OR sp.seat ILIKE ?)")
		args = append(args, p, p, p)
	}
	if len(f.UserIDs) > 0 {
		where = append(where, "sp.user_id IN ?")
		args = append(args, f.UserIDs)
	}
	sql := `
	WITH scope AS (
		SELECT sp.user_id, u.username, u.full_name, sp.seat, sp.attendance,
		       a.id AS attempt_id, a.status, a.deadline_at, a.last_seen_at, a.active_device_id AS device_id,
		       a.final_score, a.submitted_at
		FROM exam_session_participants sp
		JOIN users u ON u.id = sp.user_id
		LEFT JOIN LATERAL (
			SELECT * FROM attempts x WHERE x.session_id = sp.session_id AND x.user_id = sp.user_id
			ORDER BY x.attempt_no DESC LIMIT 1
		) a ON true
		WHERE ` + strings.Join(where, " AND ") + `
	), ev AS (
		SELECT e.attempt_id,
		       count(*) FILTER (WHERE e.type = 'FOCUS_LOST') AS focus_lost,
		       count(*) FILTER (WHERE e.type = 'FULLSCREEN_EXIT') AS fullscreen_exit,
		       count(*) FILTER (WHERE e.type = 'DEVICE_CHANGED') AS device_changed,
		       max(e.server_ts) FILTER (WHERE e.type <> 'LOCKED') AS last_violation_at,
		       max(e.server_ts) FILTER (WHERE e.type = 'LOCKED') AS locked_at
		FROM attempt_events e
		WHERE e.attempt_id IN (SELECT attempt_id FROM scope WHERE attempt_id IS NOT NULL)
		  AND e.type IN ('FOCUS_LOST', 'FULLSCREEN_EXIT', 'DEVICE_CHANGED', 'LOCKED')
		GROUP BY e.attempt_id
	), rows AS (
		SELECT s.*,
		       coalesce(ev.focus_lost, 0) AS focus_lost,
		       coalesce(ev.fullscreen_exit, 0) AS fullscreen_exit,
		       coalesce(ev.device_changed, 0) AS device_changed,
		       coalesce(ev.focus_lost, 0) + coalesce(ev.fullscreen_exit, 0) + coalesce(ev.device_changed, 0) AS violations,
		       ev.last_violation_at, ev.locked_at,
		       coalesce(s.status = 'in_progress' AND s.last_seen_at < ?, false) AS idle
		FROM scope s LEFT JOIN ev ON ev.attempt_id = s.attempt_id
	)`
	return sql, append(args, idleBefore)
}

// withDetails adds the per-attempt answer counts to an already limited row set.
const withDetails = `
	SELECT p.*,
	       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = p.attempt_id AND ai.response IS NOT NULL), 0) AS answered,
	       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = p.attempt_id), 0) AS item_count
	FROM (%s) p
	ORDER BY %s`

func (r *AttemptRepo) MonitorPage(ctx context.Context, sessionID string, f modelAttempt.MonitorFilter, idleBefore time.Time) ([]MonitorAttempt, int64, error) {
	view, ok := monitorViews[f.View]
	if !ok {
		return nil, 0, fmt.Errorf("monitor page: unknown view %q", f.View)
	}
	filter, order := view.where, view.order
	if col := violationColumns[f.ViolationType]; col != "" && f.View == modelAttempt.ViewViolations {
		filter, order = col+" > 0", col+" DESC, "+order
	}
	rowsSQL, args := monitorRowsSQL(sessionID, f, idleBefore)
	inner := fmt.Sprintf("SELECT r.*, count(*) OVER () AS total FROM rows r WHERE %s ORDER BY %s LIMIT ? OFFSET ?", filter, order)
	var out []struct {
		MonitorAttempt
		Total int64
	}
	err := dbpg.Conn(ctx, r.db).Raw(rowsSQL+fmt.Sprintf(withDetails, inner, order),
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...).Scan(&out).Error
	if err != nil {
		return nil, 0, wrap(err, "monitor page")
	}
	if len(out) == 0 {
		if f.Page <= 1 {
			return []MonitorAttempt{}, 0, nil
		}
		// past the last page: still report how many rows the view has
		var total int64
		err := dbpg.Conn(ctx, r.db).Raw(rowsSQL+fmt.Sprintf(" SELECT count(*) FROM rows WHERE %s", filter), args...).Scan(&total).Error
		return []MonitorAttempt{}, total, wrap(err, "monitor page total")
	}
	rows := make([]MonitorAttempt, len(out))
	for i := range out {
		rows[i] = out[i].MonitorAttempt
	}
	return rows, out[0].Total, nil
}

func (r *AttemptRepo) MonitorWatch(ctx context.Context, sessionID string, userIDs []string, idleBefore time.Time) ([]MonitorAttempt, error) {
	if len(userIDs) == 0 {
		return []MonitorAttempt{}, nil
	}
	order := monitorViews[modelAttempt.ViewViolations].order
	rowsSQL, args := monitorRowsSQL(sessionID, modelAttempt.MonitorFilter{UserIDs: userIDs}, idleBefore)
	out := []MonitorAttempt{}
	err := dbpg.Conn(ctx, r.db).Raw(rowsSQL+fmt.Sprintf(withDetails, "SELECT * FROM rows", order), args...).Scan(&out).Error
	return out, wrap(err, "monitor watch")
}

func (r *AttemptRepo) MonitorCounts(ctx context.Context, sessionID string, f modelAttempt.MonitorFilter, idleBefore time.Time) (map[string]int, error) {
	scope := modelAttempt.MonitorFilter{GroupID: f.GroupID, Seat: f.Seat} // counts ignore search, view and ids
	rowsSQL, args := monitorRowsSQL(sessionID, scope, idleBefore)
	var c struct {
		Total, NotStarted, InProgress, Locked, Submitted, Idle, Violations int
	}
	err := dbpg.Conn(ctx, r.db).Raw(rowsSQL+`
	SELECT count(*) AS total,
	       count(*) FILTER (WHERE attempt_id IS NULL) AS not_started,
	       count(*) FILTER (WHERE status = 'in_progress') AS in_progress,
	       count(*) FILTER (WHERE status = 'locked') AS locked,
	       count(*) FILTER (WHERE status IN ('submitted', 'graded')) AS submitted,
	       count(*) FILTER (WHERE idle) AS idle,
	       count(*) FILTER (WHERE violations > 0) AS violations
	FROM rows`, args...).Scan(&c).Error
	if err != nil {
		return nil, wrap(err, "monitor counts")
	}
	return map[string]int{
		"total": c.Total, "not_started": c.NotStarted, "in_progress": c.InProgress, "locked": c.Locked,
		"submitted": c.Submitted, "idle": c.Idle, "violations": c.Violations,
	}, nil
}
