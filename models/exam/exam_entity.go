package exam

import (
	"time"

	"gorm.io/gorm"

	"doexam/models/pgtype"
)

// Enum values mirror the CHECK constraints of migrations/000005.
type (
	Mode             string
	Status           string
	Navigation       string
	ResultVisibility string
	AttemptScoring   string
	Selection        string
)

const (
	ModeLinear          Mode = "linear"
	ModeRandomBlueprint Mode = "random_blueprint"

	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"

	NavigationFree        Navigation = "free"
	NavigationForwardOnly Navigation = "forward_only"

	ResultImmediately  ResultVisibility = "immediately"
	ResultAfterGrading ResultVisibility = "after_grading"
	ResultAfterSession ResultVisibility = "after_session"
	ResultNever        ResultVisibility = "never"

	ScoringHighest AttemptScoring = "highest"
	ScoringLatest  AttemptScoring = "latest"
	ScoringAverage AttemptScoring = "average"

	SelectionFixed  Selection = "fixed"  // items picked by hand
	SelectionRandom Selection = "random" // drawn per participant from blueprint rules
)

// Security profiles (exams.security.profile).
const (
	SecurityLight    = "light"
	SecurityStandard = "standard"
)

// ExamEntity mirrors the exams table.
type ExamEntity struct {
	ID               string  `gorm:"type:uuid;primaryKey"`
	TenantID         string  `gorm:"type:uuid;not null"`
	SubjectID        *string `gorm:"type:uuid"`
	Title            string  `gorm:"not null"`
	Description      *string
	Instructions     []byte `gorm:"type:jsonb"` // {"body": "<sanitized html>"}
	Mode             Mode   `gorm:"not null"`
	Status           Status `gorm:"not null"`
	DurationMinutes  *int
	Navigation       Navigation `gorm:"not null"`
	ShuffleItems     bool
	ShuffleOptions   bool
	ResultVisibility ResultVisibility `gorm:"not null"`
	ShowExplanation  bool
	MaxAttempts      int            `gorm:"not null"`
	AttemptScoring   AttemptScoring `gorm:"not null"`
	PassingScore     *float64       `gorm:"type:numeric(6,2)"`
	NegativeMarking  float64        `gorm:"type:numeric(6,2);not null"`
	Security         []byte         `gorm:"type:jsonb;not null"`
	Settings         []byte         `gorm:"type:jsonb;not null"`
	PublishedAt      *time.Time
	CreatedBy        *string `gorm:"type:uuid"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt
}

func (ExamEntity) TableName() string { return "exams" }

// SectionEntity mirrors the exam_sections table.
type SectionEntity struct {
	ID              string    `gorm:"type:uuid;primaryKey"`
	TenantID        string    `gorm:"type:uuid;not null"`
	ExamID          string    `gorm:"type:uuid;not null"`
	SortOrder       int       `gorm:"not null"`
	Title           string    `gorm:"not null"`
	Instructions    []byte    `gorm:"type:jsonb"`
	DurationMinutes *int      // nil = shares the exam timer
	Selection       Selection `gorm:"not null"`
	AllowRevisit    bool
	PassingScore    *float64 `gorm:"type:numeric(6,2)"`
}

func (SectionEntity) TableName() string { return "exam_sections" }

// SectionItemEntity mirrors the exam_section_items table (hand-picked items).
type SectionItemEntity struct {
	ID            string   `gorm:"type:uuid;primaryKey"`
	TenantID      string   `gorm:"type:uuid;not null"`
	SectionID     string   `gorm:"type:uuid;not null"`
	ItemID        string   `gorm:"type:uuid;not null"`
	ItemVersionID *string  `gorm:"type:uuid"` // pinned when the exam is published
	SortOrder     int      `gorm:"not null"`
	Points        *float64 `gorm:"type:numeric(8,2)"` // nil = item default_points
	LockOptions   bool
}

func (SectionItemEntity) TableName() string { return "exam_section_items" }

// RuleEntity mirrors the exam_section_rules table (blueprint rule).
type RuleEntity struct {
	ID           string           `gorm:"type:uuid;primaryKey"`
	TenantID     string           `gorm:"type:uuid;not null"`
	SectionID    string           `gorm:"type:uuid;not null"`
	SortOrder    int              `gorm:"not null"`
	BankID       string           `gorm:"type:uuid;not null"`
	CompetencyID *string          `gorm:"type:uuid"` // includes sub-competencies
	ItemType     *string          //
	Difficulty   *string          //
	TagsAny      pgtype.TextArray `gorm:"type:text[];not null"`
	ItemCount    int              `gorm:"not null"`
	Points       *float64         `gorm:"type:numeric(8,2)"` // nil = item default_points
}

func (RuleEntity) TableName() string { return "exam_section_rules" }

// RulePoolEntity mirrors exam_rule_pool: candidate versions snapshotted at publish.
type RulePoolEntity struct {
	RuleID        string `gorm:"type:uuid;primaryKey"`
	ItemVersionID string `gorm:"type:uuid;primaryKey"`
	TenantID      string `gorm:"type:uuid;not null"`
}

func (RulePoolEntity) TableName() string { return "exam_rule_pool" }
