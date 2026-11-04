package itembank

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAudit "doexam/models/audit"
	modelBank "doexam/models/itembank"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: ItemBankRepo must satisfy ItemBankStorer.
var _ ItemBankStorer = (*ItemBankRepo)(nil)

// ItemBankRepo is the GORM implementation of ItemBankStorer.
type ItemBankRepo struct {
	db *gorm.DB
}

// NewItemBankRepo returns a concrete ItemBankRepo.
func NewItemBankRepo(db *gorm.DB) *ItemBankRepo {
	return &ItemBankRepo{db: db}
}

// ---- banks ----

func (r *ItemBankRepo) ListBanks(ctx context.Context, f modelBank.BankFilter, userID string, all bool) ([]BankRow, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("item_banks b").Where("b.deleted_at IS NULL")
	access := "'owner'"
	if !all {
		q = q.Joins("JOIN item_bank_members m ON m.bank_id = b.id AND m.user_id = ?", userID)
		access = "m.access"
	}
	if f.SubjectID != "" {
		q = q.Where("b.subject_id = ?", f.SubjectID)
	}
	if f.Query != "" {
		q = q.Where("b.name ILIKE ?", dbpg.ContainsPattern(f.Query))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count banks: %w", err)
	}
	rows := []BankRow{}
	// Select only here: Count() would otherwise wrap the select list in count(...)
	sel := "b.*, " + access + ` AS my_access,
		(SELECT count(*) FROM items i WHERE i.bank_id = b.id AND i.deleted_at IS NULL) AS item_count`
	if err := q.Session(&gorm.Session{}).Select(sel).Order("b.name, b.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list banks: %w", err)
	}
	return rows, total, nil
}

func (r *ItemBankRepo) ExistingMediaIDs(ctx context.Context, ids []string) ([]string, error) {
	var found []string
	if len(ids) == 0 {
		return found, nil
	}
	if err := dbpg.Conn(ctx, r.db).Table("media_files").Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		return nil, fmt.Errorf("existing media: %w", err)
	}
	return found, nil
}

func (r *ItemBankRepo) FindBank(ctx context.Context, id string) (modelBank.BankEntity, error) {
	var b modelBank.BankEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&b).Error; err != nil {
		return b, notFound(err, "item bank")
	}
	return b, nil
}

func (r *ItemBankRepo) MemberAccess(ctx context.Context, bankID, userID string) (modelBank.Access, error) {
	var access []string
	err := dbpg.Conn(ctx, r.db).Model(&modelBank.BankMemberEntity{}).
		Where("bank_id = ? AND user_id = ?", bankID, userID).Pluck("access", &access).Error
	if err != nil {
		return "", fmt.Errorf("member access: %w", err)
	}
	if len(access) == 0 {
		return "", nil
	}
	return modelBank.Access(access[0]), nil
}

func (r *ItemBankRepo) CreateBank(ctx context.Context, b *modelBank.BankEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(b).Error, "create bank")
}

func (r *ItemBankRepo) UpdateBank(ctx context.Context, b *modelBank.BankEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Model(b).Select("name", "description").Updates(b).Error, "update bank")
}

func (r *ItemBankRepo) SoftDeleteBank(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelBank.BankEntity{}).Error, "delete bank")
}

func (r *ItemBankRepo) CountItems(ctx context.Context, bankID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Model(&modelBank.ItemEntity{}).Where("bank_id = ?", bankID).Count(&n).Error
	return n, wrap(err, "count items")
}

func (r *ItemBankRepo) exists(ctx context.Context, sql string, args ...any) (bool, error) {
	var ok bool
	err := dbpg.Conn(ctx, r.db).Raw("SELECT EXISTS ("+sql+")", args...).Scan(&ok).Error
	return ok, wrap(err, "exists")
}

func (r *ItemBankRepo) SubjectExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM subjects WHERE id = ? AND deleted_at IS NULL", id)
}

func (r *ItemBankRepo) CompetencyInSubject(ctx context.Context, competencyID, subjectID string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM competencies WHERE id = ? AND subject_id = ? AND deleted_at IS NULL", competencyID, subjectID)
}

func (r *ItemBankRepo) CompetencyIDsByCode(ctx context.Context, subjectID string) (map[string]string, error) {
	var rows []struct {
		ID   string
		Code string
	}
	err := dbpg.Conn(ctx, r.db).Table("competencies").Select("id, code").
		Where("subject_id = ? AND deleted_at IS NULL AND code IS NOT NULL", subjectID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("competencies of subject %s: %w", subjectID, err)
	}
	out := make(map[string]string, len(rows))
	for _, c := range rows {
		out[strings.ToLower(strings.TrimSpace(c.Code))] = c.ID
	}
	return out, nil
}

func (r *ItemBankRepo) UserExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM users WHERE id = ? AND deleted_at IS NULL", id)
}

// ---- members ----

func (r *ItemBankRepo) ListMembers(ctx context.Context, bankID string) ([]modelBank.MemberDTO, error) {
	out := []modelBank.MemberDTO{}
	err := dbpg.Conn(ctx, r.db).Table("item_bank_members m").
		Select("m.user_id, u.username, u.full_name, m.access").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.bank_id = ?", bankID).
		Order("CASE m.access WHEN 'owner' THEN 0 WHEN 'editor' THEN 1 ELSE 2 END, u.full_name").
		Scan(&out).Error
	return out, wrap(err, "list bank members")
}

func (r *ItemBankRepo) UpsertMember(ctx context.Context, m *modelBank.BankMemberEntity) error {
	err := dbpg.Conn(ctx, r.db).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "bank_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"access"}),
	}).Create(m).Error
	return wrap(err, "upsert bank member")
}

func (r *ItemBankRepo) RemoveMember(ctx context.Context, bankID, userID string) (bool, error) {
	res := dbpg.Conn(ctx, r.db).Where("bank_id = ? AND user_id = ?", bankID, userID).Delete(&modelBank.BankMemberEntity{})
	return res.RowsAffected > 0, wrap(res.Error, "remove bank member")
}

func (r *ItemBankRepo) CountOwners(ctx context.Context, bankID, excludeUserID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("item_bank_members m").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL AND u.status = 'active'").
		Where("m.bank_id = ? AND m.access = ? AND m.user_id <> ?", bankID, modelBank.AccessOwner, excludeUserID).
		Count(&n).Error
	return n, wrap(err, "count bank owners")
}

// ---- stimuli ----

const stimulusSelect = "s.*, v.version_no, v.content, v.locked_at"

func (r *ItemBankRepo) ListStimuli(ctx context.Context, bankID, query string, page, perPage int) ([]StimulusRow, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("stimuli s").
		Joins("JOIN stimulus_versions v ON v.id = s.current_version_id").
		Where("s.bank_id = ? AND s.deleted_at IS NULL", bankID)
	if query != "" {
		q = q.Where("s.title ILIKE ?", dbpg.ContainsPattern(query))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count stimuli: %w", err)
	}
	rows := []StimulusRow{}
	err := q.Session(&gorm.Session{}).Select(stimulusSelect).Order("s.title, s.id").
		Limit(perPage).Offset((page - 1) * perPage).Scan(&rows).Error
	return rows, total, wrap(err, "list stimuli")
}

func (r *ItemBankRepo) FindStimulus(ctx context.Context, id string) (StimulusRow, error) {
	return r.oneStimulus(ctx, "s.id = ?", id)
}

func (r *ItemBankRepo) FindStimulusVersion(ctx context.Context, versionID string) (StimulusRow, error) {
	var rows []StimulusRow
	err := dbpg.Conn(ctx, r.db).Table("stimulus_versions v").
		Joins("JOIN stimuli s ON s.id = v.stimulus_id").
		Select(stimulusSelect).Where("v.id = ?", versionID).Limit(1).Scan(&rows).Error
	if err != nil {
		return StimulusRow{}, fmt.Errorf("find stimulus version: %w", err)
	}
	if len(rows) == 0 {
		return StimulusRow{}, errr.WithMessage(errr.ErrNotFound, "stimulus not found")
	}
	rows[0].CurrentVersionID = &versionID
	return rows[0], nil
}

func (r *ItemBankRepo) oneStimulus(ctx context.Context, where string, args ...any) (StimulusRow, error) {
	var rows []StimulusRow
	err := dbpg.Conn(ctx, r.db).Table("stimuli s").
		Joins("JOIN stimulus_versions v ON v.id = s.current_version_id").
		Select(stimulusSelect).Where("s.deleted_at IS NULL").Where(where, args...).Limit(1).Scan(&rows).Error
	if err != nil {
		return StimulusRow{}, fmt.Errorf("find stimulus: %w", err)
	}
	if len(rows) == 0 {
		return StimulusRow{}, errr.WithMessage(errr.ErrNotFound, "stimulus not found")
	}
	return rows[0], nil
}

func (r *ItemBankRepo) CreateStimulus(ctx context.Context, st *modelBank.StimulusEntity, v *modelBank.StimulusVersionEntity) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Create(st).Error; err != nil { // current_version_id FK is deferred
		return wrap(err, "create stimulus")
	}
	return wrap(conn.Create(v).Error, "create stimulus version")
}

func (r *ItemBankRepo) UpdateStimulus(ctx context.Context, st *modelBank.StimulusEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Model(st).Select("title", "current_version_id").Updates(st).Error, "update stimulus")
}

func (r *ItemBankRepo) CreateStimulusVersion(ctx context.Context, v *modelBank.StimulusVersionEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(v).Error, "create stimulus version")
}

func (r *ItemBankRepo) UpdateStimulusContent(ctx context.Context, versionID string, content []byte) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelBank.StimulusVersionEntity{}).
		Where("id = ? AND locked_at IS NULL", versionID).Update("content", content).Error
	return wrap(err, "update stimulus content")
}

func (r *ItemBankRepo) RepointUnlockedItems(ctx context.Context, stimulusID, versionID string) error {
	err := dbpg.Conn(ctx, r.db).Exec(`
		UPDATE item_versions SET stimulus_version_id = ?
		WHERE locked_at IS NULL
		  AND stimulus_version_id IN (SELECT id FROM stimulus_versions WHERE stimulus_id = ?)`,
		versionID, stimulusID).Error
	return wrap(err, "repoint items to stimulus version")
}

func (r *ItemBankRepo) CountItemsUsingStimulus(ctx context.Context, stimulusID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("items i").
		Joins("JOIN item_versions iv ON iv.id = i.current_version_id").
		Joins("JOIN stimulus_versions sv ON sv.id = iv.stimulus_version_id").
		Where("i.deleted_at IS NULL AND sv.stimulus_id = ?", stimulusID).Count(&n).Error
	return n, wrap(err, "count items using stimulus")
}

func (r *ItemBankRepo) SoftDeleteStimulus(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelBank.StimulusEntity{}).Error, "delete stimulus")
}

// ---- items ----

func (r *ItemBankRepo) ListItems(ctx context.Context, bankID string, f modelBank.ItemFilter) ([]modelBank.ItemListRow, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("items i").
		Joins("JOIN item_versions iv ON iv.id = i.current_version_id").
		Where("i.bank_id = ? AND i.deleted_at IS NULL", bankID)
	if f.Status != "" {
		q = q.Where("i.status = ?", f.Status)
	}
	if f.Type != "" {
		q = q.Where("iv.type = ?", f.Type)
	}
	if f.CompetencyID != "" {
		q = q.Where("iv.competency_id = ?", f.CompetencyID)
	}
	if f.Difficulty != "" {
		q = q.Where("iv.difficulty = ?", f.Difficulty)
	}
	if f.Tag != "" {
		q = q.Where("? = ANY(iv.tags)", f.Tag)
	}
	if ts := dbpg.PrefixTSQuery(f.Query); ts != "" {
		// uses item_versions_search_idx; codes are matched too
		q = q.Where("(to_tsvector('simple', coalesce(iv.search_text, '')) @@ to_tsquery('simple', ?) OR i.code ILIKE ?)",
			ts, dbpg.ContainsPattern(f.Query))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count items: %w", err)
	}
	rows := []modelBank.ItemListRow{}
	err := q.Session(&gorm.Session{}).
		Select("i.id, i.code, i.status, iv.type, iv.version_no, iv.search_text, iv.difficulty, iv.tags, iv.competency_id, iv.locked_at, i.updated_at").
		Order("i.updated_at DESC, i.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).
		Scan(&rows).Error
	return rows, total, wrap(err, "list items")
}

func (r *ItemBankRepo) FindItem(ctx context.Context, id string, forUpdate bool) (modelBank.ItemEntity, error) {
	var it modelBank.ItemEntity
	q := dbpg.Conn(ctx, r.db).Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&it).Error; err != nil {
		return it, notFound(err, "item")
	}
	return it, nil
}

func (r *ItemBankRepo) FindVersion(ctx context.Context, id string) (modelBank.ItemVersionEntity, error) {
	var v modelBank.ItemVersionEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&v).Error; err != nil {
		return v, notFound(err, "item version")
	}
	return v, nil
}

func (r *ItemBankRepo) ListVersions(ctx context.Context, itemID string) ([]modelBank.ItemVersionEntity, error) {
	out := []modelBank.ItemVersionEntity{}
	err := dbpg.Conn(ctx, r.db).Select("id", "item_id", "version_no", "type", "locked_at", "created_at").
		Where("item_id = ?", itemID).Order("version_no DESC").Find(&out).Error
	return out, wrap(err, "list item versions")
}

func (r *ItemBankRepo) MaxVersionNo(ctx context.Context, itemID string) (int, error) {
	var n int
	err := dbpg.Conn(ctx, r.db).Model(&modelBank.ItemVersionEntity{}).Where("item_id = ?", itemID).
		Select("coalesce(max(version_no), 0)").Scan(&n).Error
	return n, wrap(err, "max version no")
}

func (r *ItemBankRepo) HasLockedVersion(ctx context.Context, itemID string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM item_versions WHERE item_id = ? AND locked_at IS NOT NULL", itemID)
}

func (r *ItemBankRepo) CreateItem(ctx context.Context, it *modelBank.ItemEntity, v *modelBank.ItemVersionEntity) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Create(it).Error; err != nil { // current_version_id FK is deferred
		return translateItemErr(err, "create item")
	}
	return wrap(conn.Create(v).Error, "create item version")
}

func (r *ItemBankRepo) UpdateItem(ctx context.Context, it *modelBank.ItemEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(it).Select("code", "status", "current_version_id").Updates(it).Error
	return translateItemErr(err, "update item")
}

func (r *ItemBankRepo) CreateVersion(ctx context.Context, v *modelBank.ItemVersionEntity) error {
	err := dbpg.Conn(ctx, r.db).Create(v).Error
	if dbpg.IsUniqueViolation(err) {
		return errr.WithMessage(errr.ErrConflict, "item was changed at the same time, please retry")
	}
	return wrap(err, "create item version")
}

func (r *ItemBankRepo) UpdateVersion(ctx context.Context, v *modelBank.ItemVersionEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(v).Select("type", "stimulus_version_id", "competency_id", "content", "answer_key",
		"scoring", "explanation", "default_points", "difficulty", "cognitive_level", "tags", "search_text").Updates(v).Error
	return wrap(err, "update item version")
}

func (r *ItemBankRepo) SoftDeleteItem(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelBank.ItemEntity{}).Error, "delete item")
}

func (r *ItemBankRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error, "create audit log")
}

func notFound(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errr.WithMessage(errr.ErrNotFound, what+" not found")
	}
	return fmt.Errorf("find %s: %w", what, err)
}

func translateItemErr(err error, op string) error {
	if dbpg.IsUniqueViolation(err) && dbpg.ConstraintName(err) == "items_bank_code_uq" {
		return &errr.ValidationError{Fields: map[string]string{"code": "already in use in this bank"}}
	}
	return wrap(err, op)
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
