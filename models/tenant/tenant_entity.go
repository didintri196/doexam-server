package tenant

import "time"

// Preset mirrors the CHECK constraint on tenants.preset. It is kept for compatibility and
// future presets; it no longer changes role names or any wording (see SystemRoles).
type Preset string

const (
	PresetSchool  Preset = "school"
	PresetCompany Preset = "company"
	PresetCustom  Preset = "custom"
)

// TenantEntity mirrors the tenants table. Settings holds the JSON of Settings.
type TenantEntity struct {
	ID        string `gorm:"type:uuid;primaryKey"`
	Slug      string `gorm:"not null"`
	Name      string `gorm:"not null"`
	Preset    Preset `gorm:"not null"`
	Settings  []byte `gorm:"type:jsonb;not null"`
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (TenantEntity) TableName() string { return "tenants" }
