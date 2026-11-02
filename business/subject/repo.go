package subject

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelSubject "doexam/models/subject"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: SubjectRepo must satisfy SubjectStorer.
var _ SubjectStorer = (*SubjectRepo)(nil)

// SubjectRepo is the GORM implementation of SubjectStorer.
type SubjectRepo struct {
	db *gorm.DB
}

// NewSubjectRepo returns a concrete SubjectRepo.
func NewSubjectRepo(db *gorm.DB) *SubjectRepo {
	return &SubjectRepo{db: db}
}

func (r *SubjectRepo) ListSubjects(ctx context.Context, f modelSubject.ListFilter) ([]modelSubject.SubjectEntity, int64, error) {
	q := dbpg.Conn(ctx, r.db).Model(&modelSubject.SubjectEntity{})
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		q = q.Where("(code ILIKE ? OR name ILIKE ?)", p, p)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count subjects: %w", err)
	}
	var out []modelSubject.SubjectEntity
	if err := q.Session(&gorm.Session{}).Order("name, id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list subjects: %w", err)
	}
	return out, total, nil
}

func (r *SubjectRepo) FindSubject(ctx context.Context, id string) (modelSubject.SubjectEntity, error) {
	var s modelSubject.SubjectEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&s).Error; err != nil {
		return s, notFound(err, "subject")
	}
	return s, nil
}

func (r *SubjectRepo) CreateSubject(ctx context.Context, s *modelSubject.SubjectEntity) error {
	return translate(dbpg.Conn(ctx, r.db).Create(s).Error, "create subject")
}

func (r *SubjectRepo) UpdateSubject(ctx context.Context, s *modelSubject.SubjectEntity) error {
	return translate(dbpg.Conn(ctx, r.db).Model(s).Select("code", "name").Updates(s).Error, "update subject")
}

func (r *SubjectRepo) SoftDeleteSubject(ctx context.Context, id string) error {
	return translate(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelSubject.SubjectEntity{}).Error, "delete subject")
}

func (r *SubjectRepo) CountBanks(ctx context.Context, subjectID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("item_banks").Where("subject_id = ? AND deleted_at IS NULL", subjectID).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count banks of subject %s: %w", subjectID, err)
	}
	return n, nil
}

func (r *SubjectRepo) ListCompetencies(ctx context.Context, subjectID string) ([]modelSubject.CompetencyEntity, error) {
	out := []modelSubject.CompetencyEntity{}
	err := dbpg.Conn(ctx, r.db).Where("subject_id = ?", subjectID).Order("sort_order, code, id").Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list competencies of subject %s: %w", subjectID, err)
	}
	return out, nil
}

func (r *SubjectRepo) FindCompetency(ctx context.Context, id string) (modelSubject.CompetencyEntity, error) {
	var c modelSubject.CompetencyEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&c).Error; err != nil {
		return c, notFound(err, "competency")
	}
	return c, nil
}

func (r *SubjectRepo) CreateCompetency(ctx context.Context, c *modelSubject.CompetencyEntity) error {
	return translate(dbpg.Conn(ctx, r.db).Create(c).Error, "create competency")
}

func (r *SubjectRepo) UpdateCompetency(ctx context.Context, c *modelSubject.CompetencyEntity) error {
	return translate(dbpg.Conn(ctx, r.db).Model(c).Select("parent_id", "code", "name", "sort_order").Updates(c).Error, "update competency")
}

func (r *SubjectRepo) SoftDeleteCompetency(ctx context.Context, id string) error {
	return translate(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelSubject.CompetencyEntity{}).Error, "delete competency")
}

func (r *SubjectRepo) CountChildCompetencies(ctx context.Context, id string) (int64, error) {
	var n int64
	if err := dbpg.Conn(ctx, r.db).Model(&modelSubject.CompetencyEntity{}).Where("parent_id = ?", id).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count child competencies of %s: %w", id, err)
	}
	return n, nil
}

func (r *SubjectRepo) CountItemVersionsUsing(ctx context.Context, competencyID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("item_versions iv").
		Joins("JOIN items i ON i.id = iv.item_id AND i.deleted_at IS NULL").
		Where("iv.competency_id = ?", competencyID).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count item versions using competency %s: %w", competencyID, err)
	}
	return n, nil
}

func (r *SubjectRepo) CompetencyAncestorIDs(ctx context.Context, id string) ([]string, error) {
	var ids []string
	err := dbpg.Conn(ctx, r.db).Raw(`
		WITH RECURSIVE up AS (
			SELECT parent_id, 1 AS depth FROM competencies WHERE id = ?
			UNION ALL
			SELECT c.parent_id, up.depth + 1 FROM competencies c JOIN up ON c.id = up.parent_id
			WHERE up.depth < 100
		)
		SELECT parent_id FROM up WHERE parent_id IS NOT NULL`, id).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("ancestors of competency %s: %w", id, err)
	}
	return ids, nil
}

func (r *SubjectRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}

func notFound(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errr.WithMessage(errr.ErrNotFound, what+" not found")
	}
	return fmt.Errorf("find %s: %w", what, err)
}

// translate maps unique violations of code columns to a field validation error.
func translate(err error, op string) error {
	if err == nil {
		return nil
	}
	if dbpg.IsUniqueViolation(err) {
		return &errr.ValidationError{Fields: map[string]string{"code": "already in use"}}
	}
	return fmt.Errorf("%s: %w", op, err)
}
