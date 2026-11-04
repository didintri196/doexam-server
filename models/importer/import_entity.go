package importer

import "time"

// Kind mirrors the CHECK constraint on import_jobs.kind ("items" belongs to the item bank module).
type Kind string

const (
	KindUsers  Kind = "users"
	KindGroups Kind = "groups"
)

// Status mirrors the CHECK constraint on import_jobs.status.
//
//	upload → validating → ready  ──commit──► importing → done
//	                    ↘ failed                       ↘ failed
type Status string

const (
	StatusValidating Status = "validating"
	StatusReady      Status = "ready"
	StatusImporting  Status = "importing"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
)

// Audience narrows a user import (DOCS/PLAN/13-IMPOR-PER-MENU.md). Empty is the
// original import: any role, any group.
type Audience string

// Audiences of a user import.
const (
	AudienceStaff       Audience = "staff"       // staff roles only, no groups column
	AudienceParticipant Audience = "participant" // the participant role for every row
)

// Target is import_jobs.target: where the rows go.
type Target struct {
	Audience Audience `json:"audience,omitempty"`
	// GroupID imports a participant list into one group: existing accounts are added
	// to the group instead of being rejected.
	GroupID string `json:"group_id,omitempty"`
}

// Summary is import_jobs.summary: rows by outcome.
type Summary struct {
	Created       int `json:"created"`
	Added         int `json:"added,omitempty"`          // existing accounts added to the target group
	AlreadyMember int `json:"already_member,omitempty"` // already in the target group, skipped
}

// ImportJobEntity mirrors the import_jobs table.
type ImportJobEntity struct {
	ID           string `gorm:"type:uuid;primaryKey"`
	TenantID     string `gorm:"type:uuid;not null"`
	Kind         Kind   `gorm:"not null"`
	SourceFileID string `gorm:"type:uuid;not null"`
	Status       Status `gorm:"not null"`
	TotalRows    int
	OkRows       int
	ErrorRows    int
	Errors       []byte  `gorm:"type:jsonb;not null"` // JSON array of RowError
	Target       []byte  `gorm:"type:jsonb;not null"` // JSON Target
	Summary      []byte  `gorm:"type:jsonb;not null"` // JSON Summary
	CreatedBy    *string `gorm:"type:uuid"`
	CreatedAt    time.Time
	FinishedAt   *time.Time
}

func (ImportJobEntity) TableName() string { return "import_jobs" }

// JobRow is a job joined with its source file name.
type JobRow struct {
	ImportJobEntity
	FileName   string
	StorageKey string
}
