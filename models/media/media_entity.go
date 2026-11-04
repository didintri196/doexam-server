package media

import "time"

// MediaFileEntity mirrors the media_files table. The content lives in file storage under StorageKey.
type MediaFileEntity struct {
	ID           string  `gorm:"type:uuid;primaryKey"`
	TenantID     string  `gorm:"type:uuid;not null"`
	StorageKey   string  `gorm:"not null"`
	OriginalName string  `gorm:"not null"`
	MimeType     string  `gorm:"not null"`
	SizeBytes    int64   `gorm:"not null"`
	SHA256       []byte  `gorm:"column:sha256;not null"`
	Width        *int    // images only
	Height       *int    // images only
	UploadedBy   *string `gorm:"type:uuid"`
	CreatedAt    time.Time
}

func (MediaFileEntity) TableName() string { return "media_files" }
