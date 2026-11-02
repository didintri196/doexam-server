package tenant

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"
	"unicode/utf8"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
)

var (
	errSetupRequired  = errr.WithMessage(errr.ErrNotFound, "setup not completed")
	errTenantNotFound = errr.WithMessage(errr.ErrNotFound, "tenant not found")
	errNoLogo         = errr.WithMessage(errr.ErrNotFound, "logo not set")
)

// Update changes the institution name and settings (needs tenant.manage).
func (s *TenantService) Update(ctx context.Context, p modelAuth.Principal, in modelTenant.UpdateInput, client modelAudit.Client) (modelTenant.TenantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.Update")
	defer span.End()

	if !p.Has(modelAuth.PermTenantManage) {
		return modelTenant.TenantDTO{}, fail(span, errr.ErrForbidden)
	}
	in.Normalize()
	if err := validateUpdate(in); err != nil {
		return modelTenant.TenantDTO{}, fail(span, err)
	}

	var updated modelTenant.TenantEntity
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		t, err := s.storer.FindForUpdate(ctx, p.TenantID)
		if err != nil {
			return err
		}
		before := modelTenant.DecodeSettings(t.Settings)
		st := before
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		name := t.Name
		set(&name, in.Name)
		set(&st.Timezone, in.Timezone)
		set(&st.Locale, in.Locale)
		set(&st.PrimaryColor, in.PrimaryColor)
		set(&st.Letterhead, in.Letterhead)
		if name == t.Name && st == before {
			updated = t
			return nil // nothing changed: no write, no audit entry
		}
		if err := s.storer.UpdateTenant(ctx, t.ID, name, st.Encode()); err != nil {
			return err
		}
		updated = t
		updated.Name, updated.Settings = name, st.Encode()
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: t.ID, ActorID: p.UserID, Action: modelAudit.ActionTenantUpdate,
			EntityType: "tenant", EntityID: t.ID, Client: client,
			Before: map[string]any{"name": t.Name, "settings": before},
			After:  map[string]any{"name": name, "settings": st},
		}))
	})
	if err != nil {
		if errr.IsNotFound(err) {
			err = errTenantNotFound
		}
		return modelTenant.TenantDTO{}, fail(span, err)
	}
	return modelTenant.ToDTO(updated), nil
}

// SetLogo stores a new logo (PNG, JPEG or WebP, at most MaxLogoBytes) and points the tenant at it.
// data may be one byte longer than the limit so oversized uploads can be told apart.
func (s *TenantService) SetLogo(ctx context.Context, p modelAuth.Principal, filename string, data []byte, client modelAudit.Client) (modelTenant.TenantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.SetLogo")
	defer span.End()

	if !p.Has(modelAuth.PermTenantManage) {
		return modelTenant.TenantDTO{}, fail(span, errr.ErrForbidden)
	}
	mime, ext, err := checkLogo(data)
	if err != nil {
		return modelTenant.TenantDTO{}, fail(span, err)
	}

	sum := sha256.Sum256(data)
	file := modelMedia.MediaFileEntity{
		ID: newID(), TenantID: p.TenantID, OriginalName: truncateName(filename), MimeType: mime,
		SizeBytes: int64(len(data)), SHA256: sum[:], UploadedBy: &p.UserID,
	}
	file.StorageKey = fmt.Sprintf("%s/branding/logo-%s.%s", p.TenantID, file.ID, ext)
	// The file is written before the transaction: if the transaction fails, an unreferenced
	// file stays behind, which is harmless; the reverse order could point at a missing file.
	if err := s.files.Put(ctx, file.StorageKey, data); err != nil {
		return modelTenant.TenantDTO{}, fail(span, err)
	}

	dto, err := s.changeLogo(ctx, p, &file, client)
	if err != nil {
		return modelTenant.TenantDTO{}, fail(span, err)
	}
	s.logger.InfoContext(ctx, "tenant logo updated", slog.String("tenant_id", p.TenantID), slog.String("file_id", file.ID))
	return dto, nil
}

// RemoveLogo clears the logo; the media file itself is kept like every other upload.
func (s *TenantService) RemoveLogo(ctx context.Context, p modelAuth.Principal, client modelAudit.Client) (modelTenant.TenantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.RemoveLogo")
	defer span.End()

	if !p.Has(modelAuth.PermTenantManage) {
		return modelTenant.TenantDTO{}, fail(span, errr.ErrForbidden)
	}
	dto, err := s.changeLogo(ctx, p, nil, client)
	if err != nil {
		return modelTenant.TenantDTO{}, fail(span, err)
	}
	return dto, nil
}

// changeLogo records file (nil removes the logo) and points the tenant settings at it.
func (s *TenantService) changeLogo(ctx context.Context, p modelAuth.Principal, file *modelMedia.MediaFileEntity, client modelAudit.Client) (modelTenant.TenantDTO, error) {
	var updated modelTenant.TenantEntity
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		t, err := s.storer.FindForUpdate(ctx, p.TenantID)
		if err != nil {
			return err
		}
		st := modelTenant.DecodeSettings(t.Settings)
		before := st.LogoFileID
		st.LogoFileID = ""
		if file != nil {
			if err := s.storer.CreateMediaFile(ctx, file); err != nil {
				return err
			}
			st.LogoFileID = file.ID
		}
		updated = t
		if before == st.LogoFileID {
			return nil // removing a logo that is not set
		}
		if err := s.storer.UpdateTenant(ctx, t.ID, t.Name, st.Encode()); err != nil {
			return err
		}
		updated.Settings = st.Encode()
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: t.ID, ActorID: p.UserID, Action: modelAudit.ActionTenantLogo,
			EntityType: "tenant", EntityID: t.ID, Client: client,
			Before: map[string]any{"logo_file_id": before}, After: map[string]any{"logo_file_id": st.LogoFileID},
		}))
	})
	if err != nil {
		if errr.IsNotFound(err) {
			err = errTenantNotFound
		}
		return modelTenant.TenantDTO{}, err
	}
	return modelTenant.ToDTO(updated), nil
}

// Public returns what a login page may show before sign-in. slug is optional while
// the installation serves a single institution.
func (s *TenantService) Public(ctx context.Context, slug string) (modelTenant.PublicTenantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.Public")
	defer span.End()

	t, err := s.resolve(ctx, slug)
	if err != nil {
		return modelTenant.PublicTenantDTO{}, fail(span, err)
	}
	return modelTenant.ToPublicDTO(t), nil
}

// Logo returns the logo file of the tenant named by slug (optional, as in Public).
func (s *TenantService) Logo(ctx context.Context, slug string) (modelTenant.LogoFile, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.Logo")
	defer span.End()

	t, err := s.resolve(ctx, slug)
	if err != nil {
		return modelTenant.LogoFile{}, fail(span, err)
	}
	fileID := modelTenant.DecodeSettings(t.Settings).LogoFileID
	if fileID == "" {
		return modelTenant.LogoFile{}, fail(span, errNoLogo)
	}
	var file modelMedia.MediaFileEntity
	err = s.tx.WithTenant(ctx, t.ID, func(ctx context.Context) error {
		file, err = s.storer.FindMediaFile(ctx, fileID)
		return err
	})
	if err != nil {
		if errr.IsNotFound(err) {
			err = errNoLogo
		}
		return modelTenant.LogoFile{}, fail(span, err)
	}
	data, err := s.files.Get(ctx, file.StorageKey)
	if err != nil {
		return modelTenant.LogoFile{}, fail(span, fmt.Errorf("read logo %s: %w", file.ID, err))
	}
	return modelTenant.LogoFile{Data: data, MimeType: file.MimeType, SHA256: file.SHA256}, nil
}

// resolve finds an active tenant by slug, or the only active tenant when slug is empty.
func (s *TenantService) resolve(ctx context.Context, slug string) (modelTenant.TenantEntity, error) {
	if slug != "" {
		t, err := s.storer.FindBySlug(ctx, slug)
		if errr.IsNotFound(err) || (err == nil && !t.IsActive) {
			return modelTenant.TenantEntity{}, errTenantNotFound
		}
		return t, err
	}
	ts, err := s.storer.ListActive(ctx, 2)
	if err != nil {
		return modelTenant.TenantEntity{}, err
	}
	switch len(ts) {
	case 1:
		return ts[0], nil
	case 0:
		n, err := s.storer.CountTenants(ctx)
		if err != nil {
			return modelTenant.TenantEntity{}, err
		}
		if n == 0 {
			return modelTenant.TenantEntity{}, errSetupRequired
		}
		return modelTenant.TenantEntity{}, errTenantNotFound
	default:
		return modelTenant.TenantEntity{}, &errr.ValidationError{Fields: map[string]string{"tenant": "required"}}
	}
}

func validateUpdate(in modelTenant.UpdateInput) error {
	v := errr.Validation{}
	if in.Name != nil {
		v.Check(validName(*in.Name), "name", "required, max 200 chars")
	}
	if in.Timezone != nil && *in.Timezone != "" {
		v.Check(validTimezone(*in.Timezone), "timezone", "must be an IANA time zone, e.g. Asia/Jakarta")
	}
	if in.Locale != nil && *in.Locale != "" {
		v.Check(slices.Contains(modelTenant.Locales, *in.Locale), "locale", "must be id or en")
	}
	if in.PrimaryColor != nil && *in.PrimaryColor != "" {
		v.Check(modelTenant.ColorPattern.MatchString(*in.PrimaryColor), "primary_color", "must be a #RRGGBB color")
	}
	if in.Letterhead != nil {
		v.Check(utf8.RuneCountInString(*in.Letterhead) <= modelTenant.MaxLetterheadLen, "letterhead",
			fmt.Sprintf("max %d chars", modelTenant.MaxLetterheadLen))
	}
	return v.Err()
}

func validName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= modelTenant.MaxNameLen
}

func validTimezone(tz string) bool {
	if tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// checkLogo validates size and the sniffed content type (the client's claim is ignored).
func checkLogo(data []byte) (mime, ext string, err error) {
	if len(data) == 0 {
		return "", "", &errr.ValidationError{Fields: map[string]string{"file": "required"}}
	}
	if len(data) > modelTenant.MaxLogoBytes {
		return "", "", &errr.ValidationError{Fields: map[string]string{"file": "max 1 MiB"}}
	}
	mime = http.DetectContentType(data)
	ext, ok := modelTenant.LogoTypes[mime]
	if !ok {
		return "", "", &errr.ValidationError{Fields: map[string]string{"file": "must be a PNG, JPEG or WebP image"}}
	}
	return mime, ext, nil
}

func truncateName(name string) string {
	if name == "" {
		return "logo"
	}
	return modelAudit.Truncate(name, 255)
}
