package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	_ "golang.org/x/image/webp" // WebP dimensions

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	"doexam/pkg/errr"
)

var errMediaNotFound = errr.WithMessage(errr.ErrNotFound, "media not found")

// uploadPerms may upload media (item authors; admins hold item.write too).
var uploadPerms = []string{modelAuth.PermItemWrite}

// linkPerms may ask for links to media they found in content they can read.
var linkPerms = []string{
	modelAuth.PermItemRead, modelAuth.PermItemWrite, modelAuth.PermExamRead,
	modelAuth.PermGradeWrite, modelAuth.PermReportRead,
}

// MediaService stores uploaded media and hands out signed links to it.
type MediaService struct {
	storer MediaStorer
	tx     TxManager
	files  FileStore
	signer Signer
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewMediaService returns a concrete MediaService.
func NewMediaService(storer MediaStorer, tx TxManager, files FileStore, signer Signer, logger *slog.Logger) *MediaService {
	return &MediaService{
		storer: storer, tx: tx, files: files, signer: signer, now: time.Now,
		tracer: otel.Tracer("doexam.service.media"), logger: logger,
	}
}

// Upload stores an image (PNG, JPEG, GIF or WebP, at most MaxImageBytes). The same
// content uploaded again returns the existing file. data may be one byte over the limit
// so oversized uploads can be told apart.
func (s *MediaService) Upload(ctx context.Context, p modelAuth.Principal, filename string, data []byte, client modelAudit.Client) (modelMedia.MediaDTO, error) {
	ctx, span := s.tracer.Start(ctx, "MediaService.Upload")
	defer span.End()

	if !slices.ContainsFunc(uploadPerms, p.Has) {
		return modelMedia.MediaDTO{}, fail(span, errr.ErrForbidden)
	}
	mime, ext, w, h, err := checkImage(data)
	if err != nil {
		return modelMedia.MediaDTO{}, fail(span, err)
	}
	sum := sha256.Sum256(data)

	var out modelMedia.MediaFileEntity
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		existing, err := s.storer.FindBySHA(ctx, sum[:])
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, errr.ErrNotFound) {
			return err
		}
		f := modelMedia.MediaFileEntity{
			ID: newID(), TenantID: p.TenantID, OriginalName: truncateName(filename), MimeType: mime,
			SizeBytes: int64(len(data)), SHA256: sum[:], Width: w, Height: h, UploadedBy: &p.UserID,
		}
		f.StorageKey = fmt.Sprintf("%s/media/%s.%s", p.TenantID, f.ID, ext)
		// Written before the row: a failed insert leaves an unreferenced file (harmless);
		// the reverse order could reference a file that is not there.
		if err := s.files.Put(ctx, f.StorageKey, data); err != nil {
			return err
		}
		if err := s.storer.Create(ctx, &f); err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		return modelMedia.MediaDTO{}, fail(span, err)
	}
	span.SetAttributes(attribute.String("media.id", out.ID))
	return s.toDTO(out), nil
}

// Links returns signed links for media ids a staff member found in content
// (unknown ids are left out).
func (s *MediaService) Links(ctx context.Context, p modelAuth.Principal, in modelMedia.LinksInput) (map[string]modelMedia.Ref, error) {
	ctx, span := s.tracer.Start(ctx, "MediaService.Links", trace.WithAttributes(attribute.Int("ids", len(in.IDs))))
	defer span.End()

	if !slices.ContainsFunc(linkPerms, p.Has) {
		return nil, fail(span, errr.ErrForbidden)
	}
	ids := cleanIDs(in.IDs)
	v := errr.Validation{}
	v.Check(len(ids) <= modelMedia.MaxLinks, "ids", fmt.Sprintf("max %d ids", modelMedia.MaxLinks))
	if err := v.Err(); err != nil {
		return nil, fail(span, err)
	}
	var out map[string]modelMedia.Ref
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		out, err = s.Refs(ctx, p.TenantID, ids)
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// Refs returns signed links for ids of tenantID. It runs inside the caller's tenant
// transaction (used by other domains while building a view); unknown ids are left out.
func (s *MediaService) Refs(ctx context.Context, tenantID string, ids []string) (map[string]modelMedia.Ref, error) {
	out := map[string]modelMedia.Ref{}
	ids = cleanIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	files, err := s.storer.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, f := range files {
		out[f.ID] = modelMedia.Ref{
			URL: s.signer.Path(modelMedia.URLPrefix, tenantID, f.ID, now), Mime: f.MimeType, W: f.Width, H: f.Height,
		}
	}
	return out, nil
}

// Serve returns a file for a signed link (no login: the signature is the permission).
func (s *MediaService) Serve(ctx context.Context, id, tenantID, expires, sig string) (modelMedia.File, error) {
	ctx, span := s.tracer.Start(ctx, "MediaService.Serve", trace.WithAttributes(attribute.String("media.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil || uuid.Validate(tenantID) != nil {
		return modelMedia.File{}, fail(span, errMediaNotFound)
	}
	if err := s.signer.Verify(tenantID, id, expires, sig, s.now()); err != nil {
		// Same answer as a missing file: a bad link reveals nothing.
		return modelMedia.File{}, fail(span, errMediaNotFound)
	}
	var f modelMedia.MediaFileEntity
	err := s.tx.WithTenant(ctx, tenantID, func(ctx context.Context) error {
		var err error
		f, err = s.storer.FindByID(ctx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, errr.ErrNotFound) {
			err = errMediaNotFound
		}
		return modelMedia.File{}, fail(span, err)
	}
	data, err := s.files.Get(ctx, f.StorageKey)
	if err != nil {
		return modelMedia.File{}, fail(span, fmt.Errorf("read media %s: %w", f.ID, err))
	}
	return modelMedia.File{Data: data, MimeType: f.MimeType, SHA256: f.SHA256}, nil
}

func (s *MediaService) toDTO(f modelMedia.MediaFileEntity) modelMedia.MediaDTO {
	return modelMedia.MediaDTO{
		ID: f.ID, MimeType: f.MimeType, SizeBytes: f.SizeBytes, Width: f.Width, Height: f.Height,
		URL: s.signer.Path(modelMedia.URLPrefix, f.TenantID, f.ID, s.now()),
	}
}

// checkImage sniffs the type from the content (never the file name) and reads the size.
func checkImage(data []byte) (mime, ext string, w, h *int, err error) {
	if len(data) == 0 {
		return "", "", nil, nil, fileErr("required")
	}
	if len(data) > modelMedia.MaxImageBytes {
		return "", "", nil, nil, fileErr("max 5 MB")
	}
	mime = http.DetectContentType(data)
	ext, ok := modelMedia.ImageTypes[mime]
	if !ok {
		return "", "", nil, nil, fileErr("must be a PNG, JPEG, GIF or WebP image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", "", nil, nil, fileErr("cannot read image")
	}
	return mime, ext, &cfg.Width, &cfg.Height, nil
}

func cleanIDs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.ToLower(strings.TrimSpace(id))
		if uuid.Validate(id) == nil && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func fileErr(msg string) error { return &errr.ValidationError{Fields: map[string]string{"file": msg}} }

func truncateName(name string) string {
	if name == "" {
		return "media"
	}
	return modelAudit.Truncate(name, 255)
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }
