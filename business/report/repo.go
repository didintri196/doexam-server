package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
	dbpg "doexam/pkg/db-pg"
)

// Compile-time check: ReportRepo must satisfy ReportStorer.
var _ ReportStorer = (*ReportRepo)(nil)

// ReportRepo is the GORM implementation of ReportStorer.
type ReportRepo struct {
	db *gorm.DB
}

// NewReportRepo returns a concrete ReportRepo.
func NewReportRepo(db *gorm.DB) *ReportRepo {
	return &ReportRepo{db: db}
}

func (r *ReportRepo) FindExam(ctx context.Context, id string) (modelExam.ExamEntity, error) {
	var e modelExam.ExamEntity
	err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return e, errExamNotFound
	}
	return e, wrap(err, "find exam")
}

func (r *ReportRepo) SessionBelongs(ctx context.Context, sessionID, examID string) (bool, error) {
	var ok bool
	err := dbpg.Conn(ctx, r.db).Raw("SELECT EXISTS (SELECT 1 FROM exam_sessions WHERE id = ? AND exam_id = ?)", sessionID, examID).Scan(&ok).Error
	return ok, wrap(err, "session belongs")
}

func (r *ReportRepo) RecapRows(ctx context.Context, examID string, f modelReport.RecapFilter) ([]RecapAttempt, error) {
	q := dbpg.Conn(ctx, r.db).Table("exam_session_participants sp").
		Select(`sp.user_id, u.username, u.full_name, u.external_id, es.name AS session_name, es.starts_at AS session_at,
			a.id AS attempt_id, a.status, a.final_score, a.submitted_at`).
		Joins("JOIN exam_sessions es ON es.id = sp.session_id AND es.status <> 'cancelled'").
		Joins("JOIN users u ON u.id = sp.user_id").
		Joins("LEFT JOIN attempts a ON a.session_id = sp.session_id AND a.user_id = sp.user_id AND a.status <> 'void'").
		Where("es.exam_id = ?", examID)
	if f.SessionID != "" {
		q = q.Where("sp.session_id = ?", f.SessionID)
	}
	if f.GroupID != "" {
		q = q.Where("EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = ? AND gm.user_id = sp.user_id)", f.GroupID)
	}
	out := []RecapAttempt{}
	err := q.Order("u.full_name, u.id, es.starts_at, a.attempt_no").Scan(&out).Error
	return out, wrap(err, "recap rows")
}

func (r *ReportRepo) AnalysisData(ctx context.Context, examID, sessionID string) ([]AnalysisAttempt, []AnalysisItem, error) {
	conn := dbpg.Conn(ctx, r.db)
	// each participant's first graded attempt: retakes are not mixed into the measurement
	attemptsSQL := `
		SELECT DISTINCT ON (a.user_id) a.id, a.user_id, a.raw_score, a.max_score, a.final_score, a.passed
		FROM attempts a
		WHERE a.exam_id = @exam AND a.status = 'graded' AND (@session = '' OR a.session_id::text = @session)
		ORDER BY a.user_id, a.attempt_no`
	args := map[string]any{"exam": examID, "session": sessionID}
	attempts := []AnalysisAttempt{}
	if err := conn.Raw(attemptsSQL, args).Scan(&attempts).Error; err != nil {
		return nil, nil, fmt.Errorf("analysis attempts: %w", err)
	}
	items := []AnalysisItem{}
	err := conn.Raw(`
		SELECT ai.attempt_id, ai.item_version_id, iv.item_id, i.code, iv.type, iv.search_text, iv.content,
		       coalesce(adj.answer_key, iv.answer_key) AS answer_key, ai.response, ai.score, ai.max_points
		FROM attempt_items ai
		JOIN item_versions iv ON iv.id = ai.item_version_id
		JOIN items i ON i.id = iv.item_id
		LEFT JOIN exam_item_adjustments adj ON adj.exam_id = @exam AND adj.item_version_id = ai.item_version_id
		     AND adj.revoked_at IS NULL AND adj.action = 'override_key'
		WHERE ai.attempt_id IN (SELECT id FROM (`+attemptsSQL+`) first_attempts)`, args).Scan(&items).Error
	return attempts, items, wrap(err, "analysis items")
}

func (r *ReportRepo) SaveStats(ctx context.Context, tenantID, examID, sessionID string, st StoredStats) error {
	conn := dbpg.Conn(ctx, r.db)
	var session any
	if sessionID != "" {
		session = sessionID
	}
	scope := "exam_id = ? AND session_id IS NOT DISTINCT FROM ?::uuid"
	if err := conn.Exec("DELETE FROM item_statistics WHERE "+scope, examID, session).Error; err != nil {
		return fmt.Errorf("clear item statistics: %w", err)
	}
	if err := conn.Exec("DELETE FROM exam_statistics WHERE "+scope, examID, session).Error; err != nil {
		return fmt.Errorf("clear exam statistics: %w", err)
	}
	e := st.Exam
	dist, _ := json.Marshal(e.Distribution)
	if err := conn.Exec(`INSERT INTO exam_statistics (tenant_id, exam_id, session_id, n_attempts, mean_score, sd_score, min_score,
		max_score, median_score, reliability, pass_rate, distribution, computed_at)
		VALUES (?, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?)`,
		tenantID, examID, session, e.NAttempts, e.Mean, e.SD, e.Min, e.Max, e.Median, e.Reliability, e.PassRate, dist, st.ComputedAt).Error; err != nil {
		return fmt.Errorf("insert exam statistics: %w", err)
	}
	for _, it := range st.Items {
		opts, _ := json.Marshal(it.Options)
		if err := conn.Exec(`INSERT INTO item_statistics (tenant_id, item_version_id, exam_id, session_id, n_responses, n_omitted,
			p_value, point_biserial, option_stats, computed_at) VALUES (?, ?, ?, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?)`,
			tenantID, it.ItemVersionID, examID, session, it.N, it.Omitted, it.PValue, it.Discrimination, opts, st.ComputedAt).Error; err != nil {
			return fmt.Errorf("insert item statistics: %w", err)
		}
	}
	return nil
}

func (r *ReportRepo) LoadStats(ctx context.Context, examID, sessionID string) (StoredStats, bool, error) {
	conn := dbpg.Conn(ctx, r.db)
	var session any
	if sessionID != "" {
		session = sessionID
	}
	var ex []struct {
		NAttempts    int
		MeanScore    *float64
		SdScore      *float64
		MinScore     *float64
		MaxScore     *float64
		MedianScore  *float64
		Reliability  *float64
		PassRate     *float64
		Distribution []byte
		ComputedAt   time.Time
	}
	if err := conn.Raw(`SELECT * FROM exam_statistics WHERE exam_id = ? AND session_id IS NOT DISTINCT FROM ?::uuid`, examID, session).
		Scan(&ex).Error; err != nil {
		return StoredStats{}, false, fmt.Errorf("load exam statistics: %w", err)
	}
	if len(ex) == 0 {
		return StoredStats{}, false, nil
	}
	st := StoredStats{ComputedAt: ex[0].ComputedAt.UTC(), Exam: modelReport.ExamStat{
		NAttempts: ex[0].NAttempts, Mean: ex[0].MeanScore, SD: ex[0].SdScore, Min: ex[0].MinScore, Max: ex[0].MaxScore,
		Median: ex[0].MedianScore, Reliability: ex[0].Reliability, PassRate: ex[0].PassRate,
	}}
	_ = json.Unmarshal(ex[0].Distribution, &st.Exam.Distribution)
	var rows []struct {
		ItemVersionID string
		ItemID        string
		Code          *string
		Type          string
		SearchText    *string
		NResponses    int
		NOmitted      int
		PValue        *float64
		PointBiserial *float64
		OptionStats   []byte
	}
	err := conn.Raw(`SELECT s.item_version_id, iv.item_id, i.code, iv.type, iv.search_text, s.n_responses, s.n_omitted,
			s.p_value, s.point_biserial, s.option_stats
		FROM item_statistics s JOIN item_versions iv ON iv.id = s.item_version_id JOIN items i ON i.id = iv.item_id
		WHERE s.exam_id = ? AND s.session_id IS NOT DISTINCT FROM ?::uuid
		ORDER BY s.point_biserial NULLS FIRST, s.item_version_id`, examID, session).Scan(&rows).Error
	for _, row := range rows {
		it := modelReport.ItemStat{ItemVersionID: row.ItemVersionID, ItemID: row.ItemID, Code: row.Code, Type: row.Type,
			StemPreview: preview(row.SearchText), N: row.NResponses, Omitted: row.NOmitted, PValue: row.PValue, Discrimination: row.PointBiserial}
		_ = json.Unmarshal(row.OptionStats, &it.Options)
		st.Items = append(st.Items, it)
	}
	return st, true, wrap(err, "load item statistics")
}

func (r *ReportRepo) FindReportAttempt(ctx context.Context, attemptID string) (ReportAttempt, error) {
	var rows []ReportAttempt
	err := dbpg.Conn(ctx, r.db).Table("attempts a").
		Select(`a.id, a.exam_id, a.user_id, u.username, u.full_name, u.external_id, es.name AS session_name, a.attempt_no, a.status,
			a.submit_reason, a.started_at, a.submitted_at, a.raw_score, a.max_score, a.final_score, a.passed,
			a.pending_manual_count, a.config_snapshot`).
		Joins("JOIN users u ON u.id = a.user_id").Joins("JOIN exam_sessions es ON es.id = a.session_id").
		Where("a.id = ?", attemptID).Limit(1).Scan(&rows).Error
	if err != nil {
		return ReportAttempt{}, fmt.Errorf("find attempt: %w", err)
	}
	if len(rows) == 0 {
		return ReportAttempt{}, errAttemptNotFound
	}
	return rows[0], nil
}

func (r *ReportRepo) ReportSections(ctx context.Context, attemptID string) ([]ReportSection, error) {
	out := []ReportSection{}
	err := dbpg.Conn(ctx, r.db).Table("attempt_sections").Select("id, section_id, raw_score, max_score, passed").
		Where("attempt_id = ?", attemptID).Order("sort_order").Scan(&out).Error
	return out, wrap(err, "report sections")
}

func (r *ReportRepo) ReportItems(ctx context.Context, attemptID string) ([]ReportItem, error) {
	out := []ReportItem{}
	err := dbpg.Conn(ctx, r.db).Table("attempt_items ai").
		Select(`ai.position, ai.attempt_section_id, iv.item_id, i.code, iv.type, iv.search_text, ai.response,
			coalesce(adj.answer_key, iv.answer_key) AS answer_key, ai.score, ai.max_points, ai.is_correct, ai.score_source,
			ai.time_spent_ms, ai.visit_count, ai.flagged, c.id AS competency_id, c.code AS competency_code, c.name AS competency_name`).
		Joins("JOIN attempts a ON a.id = ai.attempt_id").
		Joins("JOIN item_versions iv ON iv.id = ai.item_version_id").
		Joins("JOIN items i ON i.id = iv.item_id").
		Joins("LEFT JOIN competencies c ON c.id = iv.competency_id").
		Joins(`LEFT JOIN exam_item_adjustments adj ON adj.exam_id = a.exam_id AND adj.item_version_id = ai.item_version_id
			AND adj.revoked_at IS NULL AND adj.action = 'override_key'`).
		Where("ai.attempt_id = ?", attemptID).Order("ai.position").Scan(&out).Error
	return out, wrap(err, "report items")
}

func (r *ReportRepo) EventCounts(ctx context.Context, attemptID string, types []string) (map[string]int, error) {
	var rows []struct {
		Type string
		N    int
	}
	err := dbpg.Conn(ctx, r.db).Table("attempt_events").Select("type, count(*) AS n").
		Where("attempt_id = ? AND type IN ?", attemptID, types).Group("type").Scan(&rows).Error
	out := map[string]int{}
	for _, row := range rows {
		out[row.Type] = row.N
	}
	return out, wrap(err, "event counts")
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
