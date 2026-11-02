package exam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAudit "doexam/models/audit"
	modelExam "doexam/models/exam"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: ExamRepo must satisfy ExamStorer.
var _ ExamStorer = (*ExamRepo)(nil)

// ExamRepo is the GORM implementation of ExamStorer.
type ExamRepo struct {
	db *gorm.DB
}

// NewExamRepo returns a concrete ExamRepo.
func NewExamRepo(db *gorm.DB) *ExamRepo {
	return &ExamRepo{db: db}
}

// ---- exams ----

func (r *ExamRepo) ListExams(ctx context.Context, f modelExam.ListFilter, userID string) ([]ExamRow, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("exams e").Where("e.deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("e.status = ?", f.Status)
	}
	if f.SubjectID != "" {
		q = q.Where("e.subject_id = ?", f.SubjectID)
	}
	if f.Query != "" {
		q = q.Where("e.title ILIKE ?", dbpg.ContainsPattern(f.Query))
	}
	if f.Mine {
		q = q.Where("e.created_by = ?", userID)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count exams: %w", err)
	}
	rows := []ExamRow{}
	err := q.Session(&gorm.Session{}).
		Select("e.*, (SELECT count(*) FROM exam_sections s WHERE s.exam_id = e.id) AS section_count").
		Order("e.updated_at DESC, e.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&rows).Error
	return rows, total, wrap(err, "list exams")
}

func (r *ExamRepo) FindExam(ctx context.Context, id string, forUpdate bool) (modelExam.ExamEntity, error) {
	var e modelExam.ExamEntity
	q := dbpg.Conn(ctx, r.db).Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&e).Error; err != nil {
		return e, notFound(err, "exam")
	}
	return e, nil
}

func (r *ExamRepo) CreateExam(ctx context.Context, e *modelExam.ExamEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(e).Error, "create exam")
}

func (r *ExamRepo) UpdateExam(ctx context.Context, e *modelExam.ExamEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(e).Select("subject_id", "title", "description", "instructions", "mode", "status",
		"duration_minutes", "navigation", "shuffle_items", "shuffle_options", "result_visibility", "show_explanation",
		"max_attempts", "attempt_scoring", "passing_score", "negative_marking", "security", "settings", "published_at").
		Updates(e).Error
	return wrap(err, "update exam")
}

func (r *ExamRepo) SoftDeleteExam(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelExam.ExamEntity{}).Error, "delete exam")
}

func (r *ExamRepo) exists(ctx context.Context, sql string, args ...any) (bool, error) {
	var ok bool
	err := dbpg.Conn(ctx, r.db).Raw("SELECT EXISTS ("+sql+")", args...).Scan(&ok).Error
	return ok, wrap(err, "exists")
}

func (r *ExamRepo) SubjectExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM subjects WHERE id = ? AND deleted_at IS NULL", id)
}

func (r *ExamRepo) CountLiveSessions(ctx context.Context, examID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("exam_sessions").Where("exam_id = ? AND status <> 'cancelled'", examID).Count(&n).Error
	return n, wrap(err, "count sessions")
}

// ---- sections ----

func (r *ExamRepo) ListSections(ctx context.Context, examID string) ([]modelExam.SectionEntity, error) {
	out := []modelExam.SectionEntity{}
	err := dbpg.Conn(ctx, r.db).Where("exam_id = ?", examID).Order("sort_order").Find(&out).Error
	return out, wrap(err, "list sections")
}

func (r *ExamRepo) FindSection(ctx context.Context, id string) (modelExam.SectionEntity, error) {
	var s modelExam.SectionEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&s).Error; err != nil {
		return s, notFound(err, "section")
	}
	return s, nil
}

func (r *ExamRepo) CreateSection(ctx context.Context, s *modelExam.SectionEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(s).Error, "create section")
}

func (r *ExamRepo) UpdateSection(ctx context.Context, s *modelExam.SectionEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(s).
		Select("title", "instructions", "duration_minutes", "selection", "allow_revisit", "passing_score").Updates(s).Error
	return wrap(err, "update section")
}

func (r *ExamRepo) DeleteSection(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelExam.SectionEntity{}).Error, "delete section")
}

func (r *ExamRepo) SetSectionOrder(ctx context.Context, id string, order int) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelExam.SectionEntity{}).Where("id = ?", id).Update("sort_order", order).Error
	return wrap(err, "order section") // unique (exam_id, sort_order) is deferred to commit
}

// ---- section items ----

func (r *ExamRepo) ListSectionItems(ctx context.Context, examID string) ([]SectionItemRow, error) {
	rows := []SectionItemRow{}
	err := dbpg.Conn(ctx, r.db).Table("exam_section_items esi").
		Select(`esi.*, i.bank_id, i.code, i.status AS item_status, iv.type, iv.id AS version_id, iv.version_no,
			iv.search_text, iv.default_points`).
		Joins("JOIN exam_sections s ON s.id = esi.section_id").
		Joins("JOIN items i ON i.id = esi.item_id").
		Joins("JOIN item_versions iv ON iv.id = coalesce(esi.item_version_id, i.current_version_id)").
		Where("s.exam_id = ?", examID).
		Order("s.sort_order, esi.sort_order").Scan(&rows).Error
	return rows, wrap(err, "list section items")
}

func (r *ExamRepo) FindSectionItem(ctx context.Context, id string) (modelExam.SectionItemEntity, error) {
	var it modelExam.SectionItemEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&it).Error; err != nil {
		return it, notFound(err, "section item")
	}
	return it, nil
}

func (r *ExamRepo) FindItems(ctx context.Context, ids []string) ([]ItemRef, error) {
	out := []ItemRef{}
	if len(ids) == 0 {
		return out, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("items").Select("id, bank_id, status").
		Where("id IN ? AND deleted_at IS NULL", ids).Scan(&out).Error
	return out, wrap(err, "find items")
}

func (r *ExamRepo) CreateSectionItems(ctx context.Context, items []modelExam.SectionItemEntity) error {
	err := dbpg.Conn(ctx, r.db).CreateInBatches(&items, 500).Error
	if dbpg.IsUniqueViolation(err) {
		return &errr.ValidationError{Fields: map[string]string{"items": "item already in this section"}}
	}
	return wrap(err, "create section items")
}

func (r *ExamRepo) UpdateSectionItem(ctx context.Context, it *modelExam.SectionItemEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Model(it).Select("points", "lock_options").Updates(it).Error, "update section item")
}

func (r *ExamRepo) DeleteSectionItem(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelExam.SectionItemEntity{}).Error, "delete section item")
}

func (r *ExamRepo) SetSectionItemOrder(ctx context.Context, id string, order int) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelExam.SectionItemEntity{}).Where("id = ?", id).Update("sort_order", order).Error
	return wrap(err, "order section item")
}

// ---- rules ----

func (r *ExamRepo) ListRules(ctx context.Context, examID string) ([]modelExam.RuleEntity, error) {
	out := []modelExam.RuleEntity{}
	err := dbpg.Conn(ctx, r.db).Table("exam_section_rules ru").Select("ru.*").
		Joins("JOIN exam_sections s ON s.id = ru.section_id").
		Where("s.exam_id = ?", examID).Order("s.sort_order, ru.sort_order, ru.id").Scan(&out).Error
	return out, wrap(err, "list rules")
}

func (r *ExamRepo) FindRule(ctx context.Context, id string) (modelExam.RuleEntity, error) {
	var ru modelExam.RuleEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&ru).Error; err != nil {
		return ru, notFound(err, "rule")
	}
	return ru, nil
}

func (r *ExamRepo) CreateRule(ctx context.Context, ru *modelExam.RuleEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(ru).Error, "create rule")
}

func (r *ExamRepo) UpdateRule(ctx context.Context, ru *modelExam.RuleEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(ru).
		Select("bank_id", "competency_id", "item_type", "difficulty", "tags_any", "item_count", "points").Updates(ru).Error
	return wrap(err, "update rule")
}

func (r *ExamRepo) DeleteRule(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelExam.RuleEntity{}).Error, "delete rule")
}

func (r *ExamRepo) RuleCandidates(ctx context.Context, ru modelExam.RuleEntity, excludeItemIDs []string) ([]Candidate, error) {
	q := dbpg.Conn(ctx, r.db).Table("items i").
		Select("i.id AS item_id, i.current_version_id AS version_id").
		Joins("JOIN item_versions iv ON iv.id = i.current_version_id").
		Where("i.bank_id = ? AND i.deleted_at IS NULL AND i.status = 'active'", ru.BankID)
	if ru.CompetencyID != nil {
		// the competency and every sub-competency
		q = q.Where(`iv.competency_id IN (
			WITH RECURSIVE sub AS (
				SELECT id FROM competencies WHERE id = ?
				UNION ALL
				SELECT c.id FROM competencies c JOIN sub ON c.parent_id = sub.id
			) SELECT id FROM sub)`, *ru.CompetencyID)
	}
	if ru.ItemType != nil {
		q = q.Where("iv.type = ?", *ru.ItemType)
	}
	if ru.Difficulty != nil {
		q = q.Where("iv.difficulty = ?", *ru.Difficulty)
	}
	if len(ru.TagsAny) > 0 {
		q = q.Where("iv.tags && ?::text[]", ru.TagsAny)
	}
	if len(excludeItemIDs) > 0 {
		q = q.Where("i.id NOT IN ?", excludeItemIDs)
	}
	out := []Candidate{}
	err := q.Order("i.id").Scan(&out).Error
	return out, wrap(err, "rule candidates")
}

func (r *ExamRepo) PoolSize(ctx context.Context, ruleID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Model(&modelExam.RulePoolEntity{}).Where("rule_id = ?", ruleID).Count(&n).Error
	return n, wrap(err, "pool size")
}

func (r *ExamRepo) BankSubject(ctx context.Context, bankID string) (string, error) {
	var ids []string
	err := dbpg.Conn(ctx, r.db).Table("item_banks").Where("id = ? AND deleted_at IS NULL", bankID).Pluck("subject_id", &ids).Error
	if err != nil || len(ids) == 0 {
		return "", wrap(err, "bank subject")
	}
	return ids[0], nil
}

func (r *ExamRepo) CompetencyInSubject(ctx context.Context, competencyID, subjectID string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM competencies WHERE id = ? AND subject_id = ? AND deleted_at IS NULL", competencyID, subjectID)
}

func (r *ExamRepo) BankMemberAccess(ctx context.Context, bankID, userID string) (string, error) {
	var access []string
	err := dbpg.Conn(ctx, r.db).Table("item_bank_members").Where("bank_id = ? AND user_id = ?", bankID, userID).
		Pluck("access", &access).Error
	if err != nil || len(access) == 0 {
		return "", wrap(err, "bank access")
	}
	return access[0], nil
}

// ---- publish ----

func (r *ExamRepo) PinSectionItems(ctx context.Context, examID string) error {
	err := dbpg.Conn(ctx, r.db).Exec(`
		UPDATE exam_section_items esi SET item_version_id = i.current_version_id
		FROM items i, exam_sections s
		WHERE i.id = esi.item_id AND s.id = esi.section_id AND s.exam_id = ?`, examID).Error
	return wrap(err, "pin section items")
}

func (r *ExamRepo) InsertPool(ctx context.Context, rows []modelExam.RulePoolEntity) error {
	if len(rows) == 0 {
		return nil
	}
	return wrap(dbpg.Conn(ctx, r.db).CreateInBatches(&rows, 1000).Error, "insert rule pool")
}

func (r *ExamRepo) LockVersions(ctx context.Context, examID string, at time.Time) error {
	conn := dbpg.Conn(ctx, r.db)
	const versions = `
		SELECT esi.item_version_id FROM exam_section_items esi JOIN exam_sections s ON s.id = esi.section_id
		WHERE s.exam_id = @exam AND esi.item_version_id IS NOT NULL
		UNION
		SELECT p.item_version_id FROM exam_rule_pool p
		JOIN exam_section_rules ru ON ru.id = p.rule_id JOIN exam_sections s ON s.id = ru.section_id
		WHERE s.exam_id = @exam`
	args := map[string]any{"exam": examID, "at": at}
	if err := conn.Exec(`UPDATE item_versions SET locked_at = @at WHERE locked_at IS NULL AND id IN (`+versions+`)`, args).Error; err != nil {
		return fmt.Errorf("lock item versions: %w", err)
	}
	err := conn.Exec(`UPDATE stimulus_versions SET locked_at = @at WHERE locked_at IS NULL AND id IN (
		SELECT stimulus_version_id FROM item_versions WHERE stimulus_version_id IS NOT NULL AND id IN (`+versions+`))`, args).Error
	return wrap(err, "lock stimulus versions")
}

func (r *ExamRepo) ClearPins(ctx context.Context, examID string) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Exec(`UPDATE exam_section_items SET item_version_id = NULL
		WHERE section_id IN (SELECT id FROM exam_sections WHERE exam_id = ?)`, examID).Error; err != nil {
		return fmt.Errorf("clear pins: %w", err)
	}
	err := conn.Exec(`DELETE FROM exam_rule_pool WHERE rule_id IN (
		SELECT ru.id FROM exam_section_rules ru JOIN exam_sections s ON s.id = ru.section_id WHERE s.exam_id = ?)`, examID).Error
	return wrap(err, "clear rule pool")
}

func (r *ExamRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error, "create audit log")
}

func notFound(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errr.WithMessage(errr.ErrNotFound, what+" not found")
	}
	return fmt.Errorf("find %s: %w", what, err)
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
