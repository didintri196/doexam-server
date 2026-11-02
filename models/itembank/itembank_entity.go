package itembank

import (
	"time"

	"gorm.io/gorm"

	"doexam/models/pgtype"
)

// Access mirrors the CHECK constraint on item_bank_members.access.
type Access string

const (
	AccessViewer Access = "viewer" // read items
	AccessEditor Access = "editor" // + write items and stimuli
	AccessOwner  Access = "owner"  // + edit/delete the bank and manage members
)

// Rank orders access levels; 0 = no access.
func (a Access) Rank() int {
	switch a {
	case AccessViewer:
		return 1
	case AccessEditor:
		return 2
	case AccessOwner:
		return 3
	}
	return 0
}

// ItemType mirrors the CHECK constraint on item_versions.type.
type ItemType string

const (
	TypeMCQSingle   ItemType = "mcq_single"
	TypeMCQMulti    ItemType = "mcq_multi"
	TypeTrueFalse   ItemType = "true_false"
	TypeShortAnswer ItemType = "short_answer"
	TypeEssay       ItemType = "essay"
)

// ItemStatus mirrors the CHECK constraint on items.status.
type ItemStatus string

const (
	StatusDraft   ItemStatus = "draft"
	StatusActive  ItemStatus = "active" // usable in exams
	StatusRetired ItemStatus = "retired"
)

// BankEntity mirrors the item_banks table.
type BankEntity struct {
	ID          string `gorm:"type:uuid;primaryKey"`
	TenantID    string `gorm:"type:uuid;not null"`
	SubjectID   string `gorm:"type:uuid;not null"`
	Name        string `gorm:"not null"`
	Description *string
	CreatedBy   *string `gorm:"type:uuid"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt
}

func (BankEntity) TableName() string { return "item_banks" }

// BankMemberEntity mirrors the item_bank_members table.
type BankMemberEntity struct {
	BankID   string `gorm:"type:uuid;primaryKey"`
	UserID   string `gorm:"type:uuid;primaryKey"`
	TenantID string `gorm:"type:uuid;not null"`
	Access   Access `gorm:"not null"`
}

func (BankMemberEntity) TableName() string { return "item_bank_members" }

// StimulusEntity mirrors the stimuli table.
type StimulusEntity struct {
	ID               string  `gorm:"type:uuid;primaryKey"`
	TenantID         string  `gorm:"type:uuid;not null"`
	BankID           string  `gorm:"type:uuid;not null"`
	CurrentVersionID *string `gorm:"type:uuid"`
	Title            string  `gorm:"not null"`
	CreatedBy        *string `gorm:"type:uuid"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt
}

func (StimulusEntity) TableName() string { return "stimuli" }

// StimulusVersionEntity mirrors the stimulus_versions table. Content: {"body": "<sanitized html>"}.
type StimulusVersionEntity struct {
	ID         string     `gorm:"type:uuid;primaryKey"`
	TenantID   string     `gorm:"type:uuid;not null"`
	StimulusID string     `gorm:"type:uuid;not null"`
	VersionNo  int        `gorm:"not null"`
	Content    []byte     `gorm:"type:jsonb;not null"`
	LockedAt   *time.Time // set when first used by a published exam → immutable
	CreatedBy  *string    `gorm:"type:uuid"`
	CreatedAt  time.Time
}

func (StimulusVersionEntity) TableName() string { return "stimulus_versions" }

// ItemEntity mirrors the items table: the stable identity of a question.
type ItemEntity struct {
	ID               string `gorm:"type:uuid;primaryKey"`
	TenantID         string `gorm:"type:uuid;not null"`
	BankID           string `gorm:"type:uuid;not null"`
	Code             *string
	CurrentVersionID *string    `gorm:"type:uuid"`
	Status           ItemStatus `gorm:"not null"`
	CreatedBy        *string    `gorm:"type:uuid"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt
}

func (ItemEntity) TableName() string { return "items" }

// ItemVersionEntity mirrors the item_versions table: the content of a question.
// Content goes to participants; AnswerKey never does.
type ItemVersionEntity struct {
	ID                string   `gorm:"type:uuid;primaryKey"`
	TenantID          string   `gorm:"type:uuid;not null"`
	ItemID            string   `gorm:"type:uuid;not null"`
	VersionNo         int      `gorm:"not null"`
	Type              ItemType `gorm:"not null"`
	StimulusVersionID *string  `gorm:"type:uuid"`
	CompetencyID      *string  `gorm:"type:uuid"`
	Content           []byte   `gorm:"type:jsonb;not null"`
	AnswerKey         []byte   `gorm:"type:jsonb"`
	Scoring           []byte   `gorm:"type:jsonb;not null"`
	Explanation       []byte   `gorm:"type:jsonb"`
	DefaultPoints     float64  `gorm:"type:numeric(8,2);not null"`
	Difficulty        *string
	CognitiveLevel    *string
	Tags              pgtype.TextArray `gorm:"type:text[];not null"`
	SearchText        *string
	LockedAt          *time.Time // set when used by a published exam → content immutable
	CreatedBy         *string    `gorm:"type:uuid"`
	CreatedAt         time.Time
}

func (ItemVersionEntity) TableName() string { return "item_versions" }
