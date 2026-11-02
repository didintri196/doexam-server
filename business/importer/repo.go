package importer

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	modelMedia "doexam/models/media"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: ImportRepo must satisfy ImportStorer.
var _ ImportStorer = (*ImportRepo)(nil)

// insertBatch is the number of rows per INSERT statement.
const insertBatch = 500

// ImportRepo is the GORM implementation of ImportStorer.
type ImportRepo struct {
	db *gorm.DB
}

// NewImportRepo returns a concrete ImportRepo.
func NewImportRepo(db *gorm.DB) *ImportRepo {
	return &ImportRepo{db: db}
}

func (r *ImportRepo) CreateMediaFile(ctx context.Context, f *modelMedia.MediaFileEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(f).Error; err != nil {
		return fmt.Errorf("create media file: %w", err)
	}
	return nil
}

func (r *ImportRepo) CreateJob(ctx context.Context, j *modelImport.ImportJobEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(j).Error; err != nil {
		return fmt.Errorf("create import job: %w", err)
	}
	return nil
}

func (r *ImportRepo) jobQuery(ctx context.Context) *gorm.DB {
	return dbpg.Conn(ctx, r.db).Table("import_jobs j").
		Select("j.*, m.original_name AS file_name, m.storage_key").
		Joins("JOIN media_files m ON m.id = j.source_file_id")
}

func (r *ImportRepo) FindJob(ctx context.Context, id string) (modelImport.JobRow, error) {
	var rows []modelImport.JobRow
	if err := r.jobQuery(ctx).Where("j.id = ?", id).Limit(1).Scan(&rows).Error; err != nil {
		return modelImport.JobRow{}, fmt.Errorf("find import job %s: %w", id, err)
	}
	if len(rows) == 0 {
		return modelImport.JobRow{}, errr.WithMessage(errr.ErrNotFound, "import not found")
	}
	return rows[0], nil
}

func (r *ImportRepo) ListJobs(ctx context.Context, kinds []modelImport.Kind, f modelImport.ListFilter) ([]modelImport.JobRow, int64, error) {
	var total int64
	if err := dbpg.Conn(ctx, r.db).Model(&modelImport.ImportJobEntity{}).Where("kind IN ?", kinds).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count import jobs: %w", err)
	}
	rows := []modelImport.JobRow{}
	err := r.jobQuery(ctx).Where("j.kind IN ?", kinds).Order("j.created_at DESC, j.id DESC").
		Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list import jobs: %w", err)
	}
	return rows, total, nil
}

func (r *ImportRepo) UpdateJob(ctx context.Context, j *modelImport.ImportJobEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(j).
		Select("status", "total_rows", "ok_rows", "error_rows", "errors", "finished_at").
		Updates(j).Error
	if err != nil {
		return fmt.Errorf("update import job %s: %w", j.ID, err)
	}
	return nil
}

func (r *ImportRepo) ClaimJob(ctx context.Context, id string) (bool, error) {
	res := dbpg.Conn(ctx, r.db).Model(&modelImport.ImportJobEntity{}).
		Where("id = ? AND status = ?", id, modelImport.StatusReady).
		Update("status", modelImport.StatusImporting)
	if res.Error != nil {
		return false, fmt.Errorf("claim import job %s: %w", id, res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *ImportRepo) existing(ctx context.Context, column string, values []string) ([]string, error) {
	found := []string{}
	if len(values) == 0 {
		return found, nil
	}
	// column is one of three constants below, never user input.
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{}).
		Where(column+" IN ?", values).Pluck(column, &found).Error
	if err != nil {
		return nil, fmt.Errorf("existing %s: %w", column, err)
	}
	return found, nil
}

func (r *ImportRepo) ExistingUsernames(ctx context.Context, values []string) ([]string, error) {
	return r.existing(ctx, "username", values)
}

func (r *ImportRepo) ExistingEmails(ctx context.Context, values []string) ([]string, error) {
	return r.existing(ctx, "email", values)
}

func (r *ImportRepo) ExistingExternalIDs(ctx context.Context, values []string) ([]string, error) {
	return r.existing(ctx, "external_id", values)
}

func (r *ImportRepo) RolesByCodes(ctx context.Context, codes []string) ([]modelAuth.RoleEntity, error) {
	roles := []modelAuth.RoleEntity{}
	if len(codes) == 0 {
		return roles, nil
	}
	if err := dbpg.Conn(ctx, r.db).Where("code IN ?", codes).Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("roles by codes: %w", err)
	}
	return roles, nil
}

func (r *ImportRepo) GroupsByCodes(ctx context.Context, codes []string) ([]modelGroup.GroupEntity, error) {
	groups := []modelGroup.GroupEntity{}
	if len(codes) == 0 {
		return groups, nil
	}
	if err := dbpg.Conn(ctx, r.db).Where("code IN ?", codes).Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("groups by codes: %w", err)
	}
	return groups, nil
}

func (r *ImportRepo) CreateUsers(ctx context.Context, users []modelAuth.UserEntity) error {
	return r.insert(ctx, &users, "create users")
}

func (r *ImportRepo) CreateUserRoles(ctx context.Context, links []modelAuth.UserRoleEntity) error {
	return r.insert(ctx, &links, "create user roles")
}

func (r *ImportRepo) CreateMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error {
	return r.insert(ctx, &members, "create group members")
}

func (r *ImportRepo) CreateGroups(ctx context.Context, groups []modelGroup.GroupEntity) error {
	return r.insert(ctx, &groups, "create groups")
}

// insert writes a non-empty slice in batches. A unique violation means the data
// changed after validation, which surfaces as errr.ErrConflict.
func (r *ImportRepo) insert(ctx context.Context, rows any, op string) error {
	err := dbpg.Conn(ctx, r.db).CreateInBatches(rows, insertBatch).Error
	if err == nil || errors.Is(err, gorm.ErrEmptySlice) {
		return nil
	}
	if dbpg.IsUniqueViolation(err) {
		return fmt.Errorf("%s: %w (constraint %s)", op, errr.ErrConflict, dbpg.ConstraintName(err))
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (r *ImportRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}
