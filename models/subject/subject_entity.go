package subject

import (
	"time"

	"gorm.io/gorm"
)

// SubjectEntity mirrors the subjects table (mapel / test domain).
type SubjectEntity struct {
	ID        string `gorm:"type:uuid;primaryKey"`
	TenantID  string `gorm:"type:uuid;not null"`
	Code      string `gorm:"not null"`
	Name      string `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

func (SubjectEntity) TableName() string { return "subjects" }

// CompetencyEntity mirrors the competencies table (CP/TP/KD or job competency tree).
type CompetencyEntity struct {
	ID        string  `gorm:"type:uuid;primaryKey"`
	TenantID  string  `gorm:"type:uuid;not null"`
	SubjectID string  `gorm:"type:uuid;not null"`
	ParentID  *string `gorm:"type:uuid"`
	Code      string  `gorm:"not null"`
	Name      string  `gorm:"not null"`
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

func (CompetencyEntity) TableName() string { return "competencies" }
