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

// ImportJobEntity mirrors the import_jobs table (target is left to its DB default).
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
