package media

import (
	"context"
	"time"

	modelMedia "doexam/models/media"
)

// MediaStorer is the persistence port for media files.
// Every method must run inside TxManager.WithTenant.
type MediaStorer interface {
	Create(ctx context.Context, f *modelMedia.MediaFileEntity) error
	FindByID(ctx context.Context, id string) (modelMedia.MediaFileEntity, error)
	// FindBySHA returns the first file of the tenant with this content, or ErrNotFound.
	FindBySHA(ctx context.Context, sha []byte) (modelMedia.MediaFileEntity, error)
	FindByIDs(ctx context.Context, ids []string) ([]modelMedia.MediaFileEntity, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// FileStore keeps file contents.
type FileStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// Signer signs and verifies media links (pkg/mediaurl).
type Signer interface {
	Path(prefix, tenantID, fileID string, now time.Time) string
	Verify(tenantID, fileID, expires, sig string, now time.Time) error
}
