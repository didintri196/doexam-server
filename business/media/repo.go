package media

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	modelMedia "doexam/models/media"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

var _ MediaStorer = (*MediaRepo)(nil)

// MediaRepo is the GORM implementation of MediaStorer.
type MediaRepo struct{ db *gorm.DB }

// NewMediaRepo returns a concrete MediaRepo.
func NewMediaRepo(db *gorm.DB) *MediaRepo { return &MediaRepo{db: db} }

func (r *MediaRepo) Create(ctx context.Context, f *modelMedia.MediaFileEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(f).Error; err != nil {
		return fmt.Errorf("create media file: %w", err)
	}
	return nil
}

func (r *MediaRepo) FindByID(ctx context.Context, id string) (modelMedia.MediaFileEntity, error) {
	var f modelMedia.MediaFileEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, errr.ErrNotFound
		}
		return f, fmt.Errorf("find media file %s: %w", id, err)
	}
	return f, nil
}

func (r *MediaRepo) FindBySHA(ctx context.Context, sha []byte) (modelMedia.MediaFileEntity, error) {
	var f modelMedia.MediaFileEntity
	err := dbpg.Conn(ctx, r.db).Where("sha256 = ? AND storage_key LIKE ?", sha, "%/media/%").
		Order("created_at").First(&f).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, errr.ErrNotFound
		}
		return f, fmt.Errorf("find media by sha: %w", err)
	}
	return f, nil
}

func (r *MediaRepo) FindByIDs(ctx context.Context, ids []string) ([]modelMedia.MediaFileEntity, error) {
	var fs []modelMedia.MediaFileEntity
	if len(ids) == 0 {
		return fs, nil
	}
	if err := dbpg.Conn(ctx, r.db).Where("id IN ?", ids).Find(&fs).Error; err != nil {
		return nil, fmt.Errorf("find media files: %w", err)
	}
	return fs, nil
}
