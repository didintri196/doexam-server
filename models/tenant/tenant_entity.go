package tenant

import "time"

// Preset mirrors the CHECK constraint on tenants.preset.
type Preset string

const (
	PresetSchool  Preset = "school"
	PresetCompany Preset = "company"
	PresetCustom  Preset = "custom"
)

// TenantEntity mirrors the tenants table (settings is left to its DB default).
type TenantEntity struct {
	ID        string `gorm:"type:uuid;primaryKey"`
	Slug      string `gorm:"not null"`
	Name      string `gorm:"not null"`
	Preset    Preset `gorm:"not null"`
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (TenantEntity) TableName() string { return "tenants" }
