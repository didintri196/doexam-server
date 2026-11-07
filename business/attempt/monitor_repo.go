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
	modelAttempt.ViolationCameraStopped:  "camera_stopped",
	modelAttempt.ViolationScreenStopped:  "screen_stopped",
	modelAttempt.ViolationExtraDisplay:   "extra_display",
}

// MonitorResult is one poll of the monitor: the page, how many rows the view has, and the
// session's counts, all from one query.
type MonitorResult struct {
	Rows   []MonitorAttempt
	Total  int64
	Counts map[string]int
}

// MonitorPage reads one page of a view, or the watched participants when f.UserIDs is set
// (violators first, no paging), plus the counts, in a single statement: the session's rows
// (scoped by group/seat only) are built once and feed the page, its total and the counts.
func (r *AttemptRepo) MonitorPage(ctx context.Context, sessionID string, f modelAttempt.MonitorFilter, idleBefore time.Time) (MonitorResult, error) {
	view, ok := monitorViews[f.View]
	if len(f.UserIDs) > 0 {
		view, ok = monitorViews[modelAttempt.ViewViolations], true
		view.where = "true"
	}
	if !ok {
		return MonitorResult{}, fmt.Errorf("monitor page: unknown view %q", f.View)
	}
	filter, order := view.where, view.order
	if col := violationColumns[f.ViolationType]; col != "" && f.View == modelAttempt.ViewViolations && len(f.UserIDs) == 0 {
		filter, order = col+" > 0", col+" DESC, "+order
	}
	var filterArgs []any
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		filter += " AND (full_name ILIKE ? OR username ILIKE ? OR seat ILIKE ?)"
		filterArgs = append(filterArgs, p, p, p)
	}
	limit, offset := f.PerPage, (f.Page-1)*f.PerPage
	if len(f.UserIDs) > 0 {
		filter += " AND user_id IN ?"
		filterArgs = append(filterArgs, f.UserIDs)
		limit, offset = len(f.UserIDs), 0
	}

	scope := []string{"sp.session_id = ?"}
	args := []any{sessionID, sessionID}
	if f.GroupID != "" {
		scope = append(scope, "EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = ? AND gm.user_id = sp.user_id)")
		args = append(args, f.GroupID)
	}
	if f.Seat != "" {
		scope = append(scope, "sp.seat ILIKE ?")
		args = append(args, dbpg.PrefixPattern(f.Seat))
	}
	args = append(args, idleBefore)
	args = append(args, filterArgs...) // view total
	args = append(args, filterArgs...) // page
	args = append(args, limit, offset)

	sql := `
	WITH latest AS (
		SELECT DISTINCT ON (x.user_id) x.user_id, x.id, x.status, x.deadline_at, x.last_seen_at,
		       x.active_device_id, x.final_score, x.submitted_at
		FROM attempts x WHERE x.session_id = ?
		ORDER BY x.user_id, x.attempt_no DESC
	), scope AS (
		SELECT sp.user_id, u.username, u.full_name, sp.seat, sp.attendance,
		       a.id AS attempt_id, a.status, a.deadline_at, a.last_seen_at, a.active_device_id AS device_id,
		       a.final_score, a.submitted_at
		FROM exam_session_participants sp
		JOIN users u ON u.id = sp.user_id
		LEFT JOIN latest a ON a.user_id = sp.user_id
		WHERE ` + strings.Join(scope, " AND ") + `
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
	), md AS (
		-- camera/screen/display events (DOCS/PLAN/19 §6.2), through attempt_events_media_idx
		SELECT e.attempt_id,
		       count(*) FILTER (WHERE e.type = 'CAMERA_STOPPED') AS camera_stopped,
		       count(*) FILTER (WHERE e.type = 'SCREEN_SHARE_STOPPED') AS screen_stopped,
		       count(*) FILTER (WHERE e.type = 'EXTRA_DISPLAY_DETECTED') AS extra_display,
		       max(e.server_ts) FILTER (WHERE e.type IN ('CAMERA_STOPPED', 'SCREEN_SHARE_STOPPED', 'EXTRA_DISPLAY_DETECTED')) AS last_media_at,
		       coalesce(max(e.server_ts) FILTER (WHERE e.type = 'CAMERA_STOPPED') > coalesce(max(e.server_ts) FILTER (WHERE e.type = 'CAMERA_RESUMED'), '-infinity'), false) AS camera_off,
		       coalesce(max(e.server_ts) FILTER (WHERE e.type = 'SCREEN_SHARE_STOPPED') > coalesce(max(e.server_ts) FILTER (WHERE e.type = 'SCREEN_SHARE_RESUMED'), '-infinity'), false) AS screen_off,
		       coalesce(max(e.server_ts) FILTER (WHERE e.type = 'EXTRA_DISPLAY_DETECTED') > coalesce(max(e.server_ts) FILTER (WHERE e.type = 'EXTRA_DISPLAY_REMOVED'), '-infinity'), false) AS extra_display_on
		FROM attempt_events e
		WHERE e.attempt_id IN (SELECT attempt_id FROM scope WHERE attempt_id IS NOT NULL)
		  AND e.type IN ('CAMERA_STOPPED', 'CAMERA_RESUMED', 'SCREEN_SHARE_STOPPED', 'SCREEN_SHARE_RESUMED', 'EXTRA_DISPLAY_DETECTED', 'EXTRA_DISPLAY_REMOVED')
		GROUP BY e.attempt_id
	), rows AS (
		SELECT s.*,
		       coalesce(ev.focus_lost, 0) AS focus_lost,
		       coalesce(ev.fullscreen_exit, 0) AS fullscreen_exit,
		       coalesce(ev.device_changed, 0) AS device_changed,
		       coalesce(md.camera_stopped, 0) AS camera_stopped,
		       coalesce(md.screen_stopped, 0) AS screen_stopped,
		       coalesce(md.extra_display, 0) AS extra_display,
		       coalesce(ev.focus_lost, 0) + coalesce(ev.fullscreen_exit, 0) + coalesce(ev.device_changed, 0)
		         + coalesce(md.camera_stopped, 0) + coalesce(md.screen_stopped, 0) + coalesce(md.extra_display, 0) AS violations,
		       greatest(ev.last_violation_at, md.last_media_at) AS last_violation_at, ev.locked_at,
		       coalesce(md.camera_off, false) AS camera_off, coalesce(md.screen_off, false) AS screen_off,
		       coalesce(md.extra_display_on, false) AS extra_display_on,
		       coalesce(s.status = 'in_progress' AND s.last_seen_at < ?, false) AS idle
		FROM scope s LEFT JOIN ev ON ev.attempt_id = s.attempt_id LEFT JOIN md ON md.attempt_id = s.attempt_id
	), counts AS (
		SELECT count(*) AS c_total,
		       count(*) FILTER (WHERE attempt_id IS NULL) AS c_not_started,
		       count(*) FILTER (WHERE status = 'in_progress') AS c_in_progress,
		       count(*) FILTER (WHERE status = 'locked') AS c_locked,
		       count(*) FILTER (WHERE status IN ('submitted', 'graded')) AS c_submitted,
		       count(*) FILTER (WHERE idle) AS c_idle,
		       count(*) FILTER (WHERE violations > 0) AS c_violations,
		       (SELECT count(*) FROM rows WHERE ` + filter + `) AS view_total
		FROM rows
	), page AS (
		SELECT * FROM rows WHERE ` + filter + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?
	)
	SELECT p.*, p.user_id AS page_user_id,
	       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = p.attempt_id AND ai.response IS NOT NULL), 0) AS answered,
	       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = p.attempt_id), 0) AS item_count,
	       c.*
	FROM counts c LEFT JOIN page p ON true
	ORDER BY ` + order
	// counts LEFT JOIN page: one row with only the counts when the page is empty
	var out []struct {
		MonitorAttempt
		PageUserID                                                                *string
		CTotal, CNotStarted, CInProgress, CLocked, CSubmitted, CIdle, CViolations int
		ViewTotal                                                                 int64
	}
	if err := dbpg.Conn(ctx, r.db).Raw(sql, args...).Scan(&out).Error; err != nil {
		return MonitorResult{}, wrap(err, "monitor page")
	}
	res := MonitorResult{Rows: []MonitorAttempt{}, Counts: map[string]int{}}
	for i, o := range out {
		if i == 0 {
			res.Total = o.ViewTotal
			res.Counts = map[string]int{
				"total": o.CTotal, "not_started": o.CNotStarted, "in_progress": o.CInProgress, "locked": o.CLocked,
				"submitted": o.CSubmitted, "idle": o.CIdle, "violations": o.CViolations,
			}
		}
		if o.PageUserID == nil {
			continue // empty page
		}
		res.Rows = append(res.Rows, o.MonitorAttempt)
	}
	return res, nil
}
