package itembank

import (
	"context"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	"time"

	modelAudit "doexam/models/audit"
	modelBank "doexam/models/itembank"
)

// BankRow is a bank with the caller's access and its item count.
type BankRow struct {
	modelBank.BankEntity
	MyAccess  modelBank.Access
	ItemCount int64
}

// StimulusRow is a stimulus joined with its current version.
type StimulusRow struct {
	modelBank.StimulusEntity
	VersionNo int
	Content   []byte
	LockedAt  *time.Time
}

// ItemBankStorer is the persistence port for banks, stimuli and items.
// Every method must run inside TxManager.WithTenant.
type ItemBankStorer interface {
	// ExistingMediaIDs returns which of ids are media files of the tenant.
	ExistingMediaIDs(ctx context.Context, ids []string) ([]string, error)
	// banks
	ListBanks(ctx context.Context, f modelBank.BankFilter, userID string, all bool) ([]BankRow, int64, error)
	FindBank(ctx context.Context, id string) (modelBank.BankEntity, error)
	MemberAccess(ctx context.Context, bankID, userID string) (modelBank.Access, error) // "" = none
	CreateBank(ctx context.Context, b *modelBank.BankEntity) error
	UpdateBank(ctx context.Context, b *modelBank.BankEntity) error
	SoftDeleteBank(ctx context.Context, id string) error
	CountItems(ctx context.Context, bankID string) (int64, error)
	SubjectExists(ctx context.Context, id string) (bool, error)
	CompetencyInSubject(ctx context.Context, competencyID, subjectID string) (bool, error)
	// CompetencyIDsByCode maps lower-cased competency codes of a subject to their ids.
	CompetencyIDsByCode(ctx context.Context, subjectID string) (map[string]string, error)

	// members
	// ListMembers returns one page of a bank's members (owners first) and the total.
	ListMembers(ctx context.Context, bankID string, f modelBank.MemberFilter) ([]modelBank.MemberDTO, int64, error)
	UpsertMember(ctx context.Context, m *modelBank.BankMemberEntity) error
	RemoveMember(ctx context.Context, bankID, userID string) (bool, error)
	CountOwners(ctx context.Context, bankID, excludeUserID string) (int64, error)
	UserExists(ctx context.Context, id string) (bool, error)

	// stimuli
	ListStimuli(ctx context.Context, bankID, query string, page, perPage int) ([]StimulusRow, int64, error)
	FindStimulus(ctx context.Context, id string) (StimulusRow, error)
	FindStimulusVersion(ctx context.Context, id string) (StimulusRow, error) // row of the version's stimulus, at that version
	CreateStimulus(ctx context.Context, st *modelBank.StimulusEntity, v *modelBank.StimulusVersionEntity) error
	UpdateStimulus(ctx context.Context, st *modelBank.StimulusEntity) error
	CreateStimulusVersion(ctx context.Context, v *modelBank.StimulusVersionEntity) error
	UpdateStimulusContent(ctx context.Context, versionID string, content []byte) error
	// RepointUnlockedItems moves unlocked item versions using any version of stimulusID to versionID.
	RepointUnlockedItems(ctx context.Context, stimulusID, versionID string) error
	CountItemsUsingStimulus(ctx context.Context, stimulusID string) (int64, error)
	SoftDeleteStimulus(ctx context.Context, id string) error

	// items
	ListItems(ctx context.Context, bankID string, f modelBank.ItemFilter) ([]modelBank.ItemListRow, int64, error)
	FindItem(ctx context.Context, id string, forUpdate bool) (modelBank.ItemEntity, error)
	FindVersion(ctx context.Context, id string) (modelBank.ItemVersionEntity, error)
	ListVersions(ctx context.Context, itemID string) ([]modelBank.ItemVersionEntity, error)
	MaxVersionNo(ctx context.Context, itemID string) (int, error)
	HasLockedVersion(ctx context.Context, itemID string) (bool, error)
	CreateItem(ctx context.Context, it *modelBank.ItemEntity, v *modelBank.ItemVersionEntity) error
	UpdateItem(ctx context.Context, it *modelBank.ItemEntity) error
	CreateVersion(ctx context.Context, v *modelBank.ItemVersionEntity) error
	UpdateVersion(ctx context.Context, v *modelBank.ItemVersionEntity) error
	SoftDeleteItem(ctx context.Context, id string) error

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// MediaStore stores pictures found in imported documents (business/media). It opens its
// own transaction, so it is called outside the item bank transaction.
type MediaStore interface {
	Upload(ctx context.Context, p modelAuth.Principal, filename string, data []byte, client modelAudit.Client) (modelMedia.MediaDTO, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
