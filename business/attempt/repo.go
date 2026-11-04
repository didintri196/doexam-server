package attempt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelExam "doexam/models/exam"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: AttemptRepo must satisfy AttemptStorer.
var _ AttemptStorer = (*AttemptRepo)(nil)

// AttemptRepo is the GORM implementation of AttemptStorer.
type AttemptRepo struct {
	db *gorm.DB
}

// NewAttemptRepo returns a concrete AttemptRepo.
func NewAttemptRepo(db *gorm.DB) *AttemptRepo {
	return &AttemptRepo{db: db}
}

func (r *AttemptRepo) FindSession(ctx context.Context, id string) (SessionInfo, error) {
	var rows []SessionInfo
	err := dbpg.Conn(ctx, r.db).Table("exam_sessions").
		Select("id, exam_id, status, starts_at, ends_at, late_entry_minutes, token_required, token_secret, token_rotation_minutes").
		Where("id = ?", id).Limit(1).Scan(&rows).Error
	if err != nil {
		return SessionInfo{}, fmt.Errorf("find session: %w", err)
	}
	if len(rows) == 0 {
		return SessionInfo{}, errSessionNotFound
	}
	return rows[0], nil
}

func (r *AttemptRepo) FindParticipant(ctx context.Context, sessionID, userID string) (ParticipantInfo, bool, error) {
	var rows []ParticipantInfo
	err := dbpg.Conn(ctx, r.db).Table("exam_session_participants").Select("extra_time_minutes, attendance, seat").
		Where("session_id = ? AND user_id = ?", sessionID, userID).Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return ParticipantInfo{}, false, wrap(err, "find participant")
	}
	return rows[0], true, nil
}

func (r *AttemptRepo) MarkPresent(ctx context.Context, sessionID, userID string) error {
	err := dbpg.Conn(ctx, r.db).Table("exam_session_participants").
		Where("session_id = ? AND user_id = ? AND attendance = 'expected'", sessionID, userID).
		Update("attendance", "present").Error
	return wrap(err, "mark present")
}

func (r *AttemptRepo) IsProctor(ctx context.Context, sessionID, userID string) (bool, error) {
	var ok bool
	err := dbpg.Conn(ctx, r.db).Raw("SELECT EXISTS (SELECT 1 FROM exam_session_proctors WHERE session_id = ? AND user_id = ?)",
		sessionID, userID).Scan(&ok).Error
	return ok, wrap(err, "is proctor")
}

func (r *AttemptRepo) FindExam(ctx context.Context, id string) (modelExam.ExamEntity, error) {
	var e modelExam.ExamEntity
	err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return e, errr.WithMessage(errr.ErrNotFound, "exam not found")
	}
	return e, wrap(err, "find exam")
}

func (r *AttemptRepo) ListSections(ctx context.Context, examID string) ([]modelExam.SectionEntity, error) {
	out := []modelExam.SectionEntity{}
	return out, wrap(dbpg.Conn(ctx, r.db).Where("exam_id = ?", examID).Order("sort_order").Find(&out).Error, "list sections")
}

func (r *AttemptRepo) ListRules(ctx context.Context, examID string) ([]modelExam.RuleEntity, error) {
	out := []modelExam.RuleEntity{}
	err := dbpg.Conn(ctx, r.db).Table("exam_section_rules ru").Select("ru.*").
		Joins("JOIN exam_sections s ON s.id = ru.section_id").Where("s.exam_id = ?", examID).
		Order("s.sort_order, ru.sort_order, ru.id").Scan(&out).Error
	return out, wrap(err, "list rules")
}

func (r *AttemptRepo) Candidates(ctx context.Context, examID string) ([]CandidateRow, error) {
	out := []CandidateRow{}
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT esi.section_id::text AS section_id, '' AS rule_id, esi.item_id, iv.id AS version_id, iv.type, iv.content,
		       iv.default_points, esi.points, esi.lock_options, esi.sort_order
		FROM exam_section_items esi
		JOIN exam_sections s ON s.id = esi.section_id
		JOIN item_versions iv ON iv.id = esi.item_version_id
		WHERE s.exam_id = @exam
		UNION ALL
		SELECT '' AS section_id, p.rule_id::text AS rule_id, iv.item_id, iv.id AS version_id, iv.type, iv.content,
		       iv.default_points, NULL AS points, false AS lock_options, 0 AS sort_order
		FROM exam_rule_pool p
		JOIN exam_section_rules ru ON ru.id = p.rule_id
		JOIN exam_sections s ON s.id = ru.section_id
		JOIN item_versions iv ON iv.id = p.item_version_id
		WHERE s.exam_id = @exam
		ORDER BY sort_order, version_id`, map[string]any{"exam": examID}).Scan(&out).Error
	return out, wrap(err, "exam candidates")
}

func (r *AttemptRepo) Adjustments(ctx context.Context, examID string) (map[string]adjustment, error) {
	var rows []struct {
		ItemVersionID string
		Action        string
		AnswerKey     []byte
	}
	err := dbpg.Conn(ctx, r.db).Table("exam_item_adjustments").Select("item_version_id, action, answer_key").
		Where("exam_id = ? AND revoked_at IS NULL", examID).Scan(&rows).Error
	out := map[string]adjustment{}
	for _, row := range rows {
		out[row.ItemVersionID] = adjustment{Action: row.Action, AnswerKey: row.AnswerKey}
	}
	return out, wrap(err, "adjustments")
}

func (r *AttemptRepo) ActiveAttempt(ctx context.Context, examID, userID string) (modelAttempt.AttemptEntity, bool, error) {
	var a []modelAttempt.AttemptEntity
	err := dbpg.Conn(ctx, r.db).Where("exam_id = ? AND user_id = ? AND status IN ?", examID, userID,
		[]modelAttempt.Status{modelAttempt.StatusInProgress, modelAttempt.StatusLocked}).
		Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Find(&a).Error
	if err != nil || len(a) == 0 {
		return modelAttempt.AttemptEntity{}, false, wrap(err, "active attempt")
	}
	return a[0], true, nil
}

func (r *AttemptRepo) CountAttempts(ctx context.Context, examID, userID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Model(&modelAttempt.AttemptEntity{}).
		Where("exam_id = ? AND user_id = ? AND status <> ?", examID, userID, modelAttempt.StatusVoid).Count(&n).Error
	return n, wrap(err, "count attempts")
}

func (r *AttemptRepo) FindAttempt(ctx context.Context, id string, forUpdate bool) (modelAttempt.AttemptEntity, error) {
	var a modelAttempt.AttemptEntity
	q := dbpg.Conn(ctx, r.db).Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return a, errAttemptNotFound
	}
	return a, wrap(err, "find attempt")
}

func (r *AttemptRepo) CreateAttempt(ctx context.Context, a *modelAttempt.AttemptEntity, sections []modelAttempt.AttemptSectionEntity, items []modelAttempt.AttemptItemEntity) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Create(a).Error; err != nil {
		if dbpg.IsUniqueViolation(err) {
			return errr.WithMessage(errr.ErrConflict, "an attempt was started at the same time, please retry")
		}
		return fmt.Errorf("create attempt: %w", err)
	}
	if err := conn.Create(&sections).Error; err != nil {
		return fmt.Errorf("create attempt sections: %w", err)
	}
	if len(items) == 0 {
		return nil
	}
	return wrap(conn.CreateInBatches(&items, 500).Error, "create attempt items")
}

func (r *AttemptRepo) UpdateAttempt(ctx context.Context, a *modelAttempt.AttemptEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(a).Select("status", "deadline_at", "submitted_at", "submit_reason", "active_device_id",
		"last_seen_at", "last_event_seq", "raw_score", "max_score", "final_score", "passed", "pending_manual_count", "graded_at").
		Updates(a).Error
	return wrap(err, "update attempt")
}

func (r *AttemptRepo) ListAttemptSections(ctx context.Context, attemptID string) ([]modelAttempt.AttemptSectionEntity, error) {
	out := []modelAttempt.AttemptSectionEntity{}
	return out, wrap(dbpg.Conn(ctx, r.db).Where("attempt_id = ?", attemptID).Order("sort_order").Find(&out).Error, "list attempt sections")
}

func (r *AttemptRepo) UpdateAttemptSection(ctx context.Context, s *modelAttempt.AttemptSectionEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(s).Select("started_at", "deadline_at", "closed_at", "raw_score", "max_score", "passed").Updates(s).Error
	return wrap(err, "update attempt section")
}

func (r *AttemptRepo) ListItems(ctx context.Context, attemptID string) ([]ItemRow, error) {
	out := []ItemRow{}
	err := dbpg.Conn(ctx, r.db).Table("attempt_items ai").
		Select(`ai.*, iv.type, iv.content, iv.answer_key, iv.scoring, iv.explanation,
			st.id AS stimulus_id, st.title AS stimulus_title, sv.content AS stimulus_content`).
		Joins("JOIN item_versions iv ON iv.id = ai.item_version_id").
		Joins("LEFT JOIN stimulus_versions sv ON sv.id = iv.stimulus_version_id").
		Joins("LEFT JOIN stimuli st ON st.id = sv.stimulus_id").
		Where("ai.attempt_id = ?", attemptID).Order("ai.position").Scan(&out).Error
	return out, wrap(err, "list attempt items")
}

func (r *AttemptRepo) SaveResponse(ctx context.Context, itemID string, response []byte, seq int, at time.Time) error {
	var resp any
	if response != nil {
		resp = response
	}
	// the seq guard makes an out-of-order older event a no-op
	err := dbpg.Conn(ctx, r.db).Exec(`UPDATE attempt_items SET response = ?::jsonb, response_seq = ?, answered_at = ?
		WHERE id = ? AND response_seq < ?`, resp, seq, at, itemID, seq).Error
	return wrap(err, "save response")
}

func (r *AttemptRepo) SetFlag(ctx context.Context, itemID string, flagged bool) error {
	return wrap(dbpg.Conn(ctx, r.db).Model(&modelAttempt.AttemptItemEntity{}).Where("id = ?", itemID).Update("flagged", flagged).Error, "set flag")
}

func (r *AttemptRepo) AddVisit(ctx context.Context, itemID string, spentMs int) error {
	err := dbpg.Conn(ctx, r.db).Exec(`UPDATE attempt_items SET visit_count = visit_count + 1,
		time_spent_ms = least(time_spent_ms + ?, 2000000000) WHERE id = ?`, spentMs, itemID).Error
	return wrap(err, "add visit")
}

func (r *AttemptRepo) SaveScores(ctx context.Context, items []modelAttempt.AttemptItemEntity) error {
	conn := dbpg.Conn(ctx, r.db)
	for i := range items {
		it := &items[i]
		err := conn.Model(it).Select("score", "is_correct", "score_source", "scored_at").Updates(it).Error
		if err != nil {
			return fmt.Errorf("save score of %s: %w", it.ID, err)
		}
	}
	return nil
}

func (r *AttemptRepo) LatestManualScores(ctx context.Context, itemIDs []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		AttemptItemID string
		Score         float64
	}
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT DISTINCT ON (attempt_item_id) attempt_item_id, score
		FROM manual_grades WHERE attempt_item_id IN ?
		ORDER BY attempt_item_id, created_at DESC, id DESC`, itemIDs).Scan(&rows).Error
	for _, row := range rows {
		out[row.AttemptItemID] = f(row.Score)
	}
	return out, wrap(err, "latest manual scores")
}

func (r *AttemptRepo) MediaPlays(ctx context.Context, attemptID string) (map[string]int, int, error) {
	var rows []struct {
		MediaID string
		N       int
	}
	err := dbpg.Conn(ctx, r.db).Table("attempt_events").
		Select("payload->>'media_id' AS media_id, count(*) AS n").
		Where("attempt_id = ? AND type = ?", attemptID, modelAttempt.EventMediaPlayed).
		Group("payload->>'media_id'").Scan(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("media plays of attempt %s: %w", attemptID, err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.MediaID] = r.N
	}
	var extra int
	err = dbpg.Conn(ctx, r.db).Table("attempt_events").
		Select("COALESCE(SUM((payload->>'plays')::int), 0)").
		Where("attempt_id = ? AND type = ?", attemptID, modelAttempt.EventMediaPlaysGranted).
		Scan(&extra).Error
	if err != nil {
		return nil, 0, fmt.Errorf("granted plays of attempt %s: %w", attemptID, err)
	}
	return out, extra, nil
}

func (r *AttemptRepo) InsertEvent(ctx context.Context, e *modelAttempt.EventEntity) (bool, error) {
	if len(e.Payload) == 0 {
		e.Payload = []byte("{}")
	}
	res := dbpg.Conn(ctx, r.db).Exec(`
		INSERT INTO attempt_events (tenant_id, attempt_id, client_event_id, client_seq, type, attempt_item_id, payload, actor_id, client_ts, server_ts, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?)
		ON CONFLICT (attempt_id, client_event_id) WHERE client_event_id IS NOT NULL DO NOTHING`,
		e.TenantID, e.AttemptID, e.ClientEventID, e.ClientSeq, e.Type, e.AttemptItemID, e.Payload, e.ActorID, e.ClientTS, e.ServerTS, e.IP)
	if res.Error != nil {
		return false, fmt.Errorf("insert event: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *AttemptRepo) GradingSummary(ctx context.Context, examID string) ([]modelAttempt.GradingSummaryRow, error) {
	out := []modelAttempt.GradingSummaryRow{}
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT iv.id AS item_version_id, i.id AS item_id, i.code, left(coalesce(iv.search_text, ''), 160) AS stem_preview,
		       count(*) FILTER (WHERE ai.score IS NULL) AS pending,
		       count(*) FILTER (WHERE ai.score_source = 'manual') AS graded
		FROM attempt_items ai
		JOIN attempts a ON a.id = ai.attempt_id AND a.status IN ('submitted', 'graded')
		JOIN item_versions iv ON iv.id = ai.item_version_id AND iv.type = 'essay'
		JOIN items i ON i.id = iv.item_id
		WHERE a.exam_id = ? AND ai.response IS NOT NULL
		GROUP BY iv.id, i.id, i.code, iv.search_text
		ORDER BY min(ai.position), iv.id`, examID).Scan(&out).Error
	return out, wrap(err, "grading summary")
}

func (r *AttemptRepo) GradingQueue(ctx context.Context, examID string, f modelAttempt.GradingFilter) ([]modelAttempt.GradingQueueRow, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("attempt_items ai").
		Joins("JOIN attempts a ON a.id = ai.attempt_id AND a.status IN ('submitted', 'graded')").
		Joins("JOIN item_versions iv ON iv.id = ai.item_version_id AND iv.type = 'essay'").
		Joins("JOIN users u ON u.id = a.user_id").
		Where("a.exam_id = ? AND ai.response IS NOT NULL", examID)
	if f.ItemVersionID != "" {
		q = q.Where("ai.item_version_id = ?", f.ItemVersionID)
	}
	switch f.Status {
	case "graded":
		q = q.Where("ai.score_source = 'manual'")
	case "all":
	default:
		q = q.Where("ai.score IS NULL")
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count grading queue: %w", err)
	}
	out := []modelAttempt.GradingQueueRow{}
	// ordered by attempt id (UUIDv7, i.e. start time), not by name: graders see a neutral order
	err := q.Session(&gorm.Session{}).
		Select(`ai.id AS attempt_item_id, ai.attempt_id, ai.item_version_id, a.user_id, u.username, u.full_name,
			ai.response, ai.max_points, ai.score, ai.score_source, ai.answered_at`).
		Order("ai.item_version_id, a.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&out).Error
	return out, total, wrap(err, "grading queue")
}

func (r *AttemptRepo) FindItem(ctx context.Context, attemptItemID string) (ItemRow, error) {
	var rows []ItemRow
	err := dbpg.Conn(ctx, r.db).Table("attempt_items ai").
		Select("ai.*, iv.type, iv.content, iv.answer_key, iv.scoring, iv.explanation").
		Joins("JOIN item_versions iv ON iv.id = ai.item_version_id").
		Where("ai.id = ?", attemptItemID).Limit(1).Scan(&rows).Error
	if err != nil {
		return ItemRow{}, fmt.Errorf("find attempt item: %w", err)
	}
	if len(rows) == 0 {
		return ItemRow{}, errr.WithMessage(errr.ErrNotFound, "answer not found")
	}
	return rows[0], nil
}

func (r *AttemptRepo) GradeHistory(ctx context.Context, attemptItemID string) ([]modelAttempt.GradeHistoryRow, error) {
	out := []modelAttempt.GradeHistoryRow{}
	err := dbpg.Conn(ctx, r.db).Table("manual_grades mg").
		Select("mg.grader_id, u.full_name AS grader_name, mg.score, mg.rubric_scores, mg.comment, mg.created_at").
		Joins("JOIN users u ON u.id = mg.grader_id").
		Where("mg.attempt_item_id = ?", attemptItemID).Order("mg.created_at DESC").Scan(&out).Error
	return out, wrap(err, "grade history")
}

func (r *AttemptRepo) InsertManualGrade(ctx context.Context, g *modelAttempt.ManualGradeEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(g).Error, "insert manual grade")
}

func (r *AttemptRepo) ListAdjustments(ctx context.Context, examID string) ([]modelAttempt.AdjustmentEntity, error) {
	out := []modelAttempt.AdjustmentEntity{}
	err := dbpg.Conn(ctx, r.db).Where("exam_id = ?", examID).Order("created_at DESC").Find(&out).Error
	return out, wrap(err, "list adjustments")
}

func (r *AttemptRepo) FindAdjustment(ctx context.Context, id string) (modelAttempt.AdjustmentEntity, error) {
	var a modelAttempt.AdjustmentEntity
	err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return a, errr.WithMessage(errr.ErrNotFound, "adjustment not found")
	}
	return a, wrap(err, "find adjustment")
}

func (r *AttemptRepo) CreateAdjustment(ctx context.Context, a *modelAttempt.AdjustmentEntity) error {
	err := dbpg.Conn(ctx, r.db).Create(a).Error
	if dbpg.IsUniqueViolation(err) {
		return errr.WithMessage(errr.ErrConflict, "this item already has an active adjustment; revoke it first")
	}
	return wrap(err, "create adjustment")
}

func (r *AttemptRepo) RevokeAdjustment(ctx context.Context, id string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAttempt.AdjustmentEntity{}).Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at).Error
	return wrap(err, "revoke adjustment")
}

func (r *AttemptRepo) VersionInExam(ctx context.Context, examID, versionID string) (string, []byte, bool, error) {
	var rows []struct {
		Type    string
		Content []byte
	}
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT iv.type, iv.content FROM item_versions iv
		WHERE iv.id = @v AND (
			EXISTS (SELECT 1 FROM exam_section_items esi JOIN exam_sections s ON s.id = esi.section_id
			        WHERE s.exam_id = @e AND esi.item_version_id = iv.id)
			OR EXISTS (SELECT 1 FROM exam_rule_pool p JOIN exam_section_rules ru ON ru.id = p.rule_id
			           JOIN exam_sections s ON s.id = ru.section_id WHERE s.exam_id = @e AND p.item_version_id = iv.id)
			OR EXISTS (SELECT 1 FROM attempt_items ai JOIN attempts a ON a.id = ai.attempt_id
			           WHERE a.exam_id = @e AND ai.item_version_id = iv.id))`,
		map[string]any{"v": versionID, "e": examID}).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return "", nil, false, wrap(err, "version in exam")
	}
	return rows[0].Type, rows[0].Content, true, nil
}

func (r *AttemptRepo) AttemptsWithVersion(ctx context.Context, examID, versionID string) ([]modelAttempt.AttemptEntity, error) {
	out := []modelAttempt.AttemptEntity{}
	err := dbpg.Conn(ctx, r.db).
		Where("exam_id = ? AND status IN ? AND EXISTS (SELECT 1 FROM attempt_items ai WHERE ai.attempt_id = attempts.id AND ai.item_version_id = ?)",
			examID, []modelAttempt.Status{modelAttempt.StatusSubmitted, modelAttempt.StatusGraded}, versionID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&out).Error
	return out, wrap(err, "attempts with version")
}

func (r *AttemptRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error, "create audit log")
}

func (r *AttemptRepo) RunningAttempts(ctx context.Context, sessionID string) ([]modelAttempt.AttemptEntity, error) {
	out := []modelAttempt.AttemptEntity{}
	err := dbpg.Conn(ctx, r.db).Where("session_id = ? AND status IN ?", sessionID,
		[]modelAttempt.Status{modelAttempt.StatusInProgress, modelAttempt.StatusLocked}).
		Clauses(clause.Locking{Strength: "UPDATE"}).Find(&out).Error
	return out, wrap(err, "running attempts")
}

func (r *AttemptRepo) Monitor(ctx context.Context, sessionID string) ([]MonitorAttempt, error) {
	out := []MonitorAttempt{}
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT sp.user_id, u.username, u.full_name, sp.seat, sp.attendance,
		       a.id AS attempt_id, a.status, a.deadline_at, a.last_seen_at, a.active_device_id AS device_id, a.final_score,
		       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = a.id AND ai.response IS NOT NULL), 0) AS answered,
		       coalesce((SELECT count(*) FROM attempt_items ai WHERE ai.attempt_id = a.id), 0) AS item_count,
		       coalesce((SELECT count(*) FROM attempt_events ev WHERE ev.attempt_id = a.id AND ev.type IN ?), 0) AS violations
		FROM exam_session_participants sp
		JOIN users u ON u.id = sp.user_id
		LEFT JOIN LATERAL (
			SELECT * FROM attempts x WHERE x.session_id = sp.session_id AND x.user_id = sp.user_id
			ORDER BY x.attempt_no DESC LIMIT 1
		) a ON true
		WHERE sp.session_id = ?
		ORDER BY u.full_name, u.id`, modelAttempt.ViolationEventTypes, sessionID).Scan(&out).Error
	return out, wrap(err, "monitor")
}

func (r *AttemptRepo) MyAttempts(ctx context.Context, userID string) ([]modelAttempt.MyAttemptDTO, error) {
	out := []modelAttempt.MyAttemptDTO{}
	err := dbpg.Conn(ctx, r.db).Table("attempts a").
		Select("a.id, a.exam_id, e.title AS exam_title, a.session_id, a.attempt_no, a.status, a.started_at, a.deadline_at, a.submitted_at").
		Joins("JOIN exams e ON e.id = a.exam_id").
		Where("a.user_id = ? AND a.status <> 'void'", userID).Order("a.started_at DESC").Limit(100).Scan(&out).Error
	return out, wrap(err, "my attempts")
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
