package tenant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
)

// TenantService handles tenants: first-run setup, the institution profile and its logo.
type TenantService struct {
	storer TenantStorer
	tx     TxManager
	hasher PasswordHasher
	files  FileStore
	tracer trace.Tracer
	logger *slog.Logger
}

// NewTenantService returns a concrete TenantService.
func NewTenantService(storer TenantStorer, tx TxManager, hasher PasswordHasher, files FileStore, logger *slog.Logger) *TenantService {
	return &TenantService{
		storer: storer,
		tx:     tx,
		hasher: hasher,
		files:  files,
		tracer: otel.Tracer("doexam.service.tenant"),
		logger: logger,
	}
}

// Setup creates the first tenant, its system roles and an admin user, in one transaction.
// It only works while no tenant exists; afterwards it returns errr.ErrAlreadyExists.
func (s *TenantService) Setup(ctx context.Context, in modelTenant.SetupInput) (modelTenant.SetupDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.Setup")
	defer span.End()

	dto, err := s.setup(ctx, in)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return modelTenant.SetupDTO{}, err
	}
	s.logger.InfoContext(ctx, "tenant setup completed",
		slog.String("tenant_id", dto.Tenant.ID), slog.String("admin_id", dto.Admin.ID))
	return dto, nil
}

func (s *TenantService) setup(ctx context.Context, in modelTenant.SetupInput) (modelTenant.SetupDTO, error) {
	in.Normalize()
	if err := validateSetup(in); err != nil {
		return modelTenant.SetupDTO{}, err
	}

	// Hash before opening the transaction: Argon2 is slow and must not hold a connection.
	hash, err := s.hasher.Hash(ctx, in.AdminPassword)
	if err != nil {
		return modelTenant.SetupDTO{}, fmt.Errorf("hash password: %w", err)
	}

	tenant := modelTenant.TenantEntity{
		ID: newID(), Slug: in.TenantSlug, Name: in.TenantName, Preset: in.Preset,
		Settings: modelTenant.Settings{}.Encode(), IsActive: true,
	}
	admin := modelAuth.UserEntity{
		ID: newID(), TenantID: tenant.ID, Username: in.AdminUsername, Email: in.AdminEmail,
		PasswordHash: &hash, FullName: in.AdminFullName, Status: modelAuth.UserStatusActive,
	}
	var roles []modelAuth.RoleEntity
	var adminRoleID string
	for _, sr := range modelAuth.SystemRoles(string(in.Preset)) {
		r := modelAuth.RoleEntity{
			ID: newID(), TenantID: tenant.ID, Code: sr.Code, Name: sr.Name,
			Permissions: sr.Permissions, IsSystem: true,
		}
		if sr.Code == modelAuth.RoleAdmin {
			adminRoleID = r.ID
		}
		roles = append(roles, r)
	}

	err = s.tx.WithTenant(ctx, tenant.ID, func(ctx context.Context) error {
		if err := s.storer.LockSetup(ctx); err != nil {
			return err
		}
		n, err := s.storer.CountTenants(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrAlreadyExists, "setup already completed")
		}
		if err := s.storer.CreateTenant(ctx, &tenant); err != nil {
			return err
		}
		if err := s.storer.CreateRoles(ctx, roles); err != nil {
			return err
		}
		if err := s.storer.CreateUser(ctx, &admin); err != nil {
			return err
		}
		if err := s.storer.AssignRoles(ctx, []modelAuth.UserRoleEntity{
			{UserID: admin.ID, RoleID: adminRoleID, TenantID: tenant.ID},
		}); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: tenant.ID, ActorID: admin.ID, Action: modelAudit.ActionTenantSetup,
			EntityType: "tenant", EntityID: tenant.ID,
			After: map[string]any{"slug": tenant.Slug, "preset": tenant.Preset, "admin_id": admin.ID},
		}))
	})
	if err != nil {
		return modelTenant.SetupDTO{}, err
	}

	var dto modelTenant.SetupDTO
	dto.Tenant = modelTenant.ToDTO(tenant)
	dto.Admin.ID = admin.ID
	dto.Admin.Username = admin.Username
	return dto, nil
}

// Current returns the tenant of the authenticated principal.
func (s *TenantService) Current(ctx context.Context, p modelAuth.Principal) (modelTenant.TenantDTO, error) {
	ctx, span := s.tracer.Start(ctx, "TenantService.Current")
	defer span.End()

	t, err := s.storer.FindByID(ctx, p.TenantID)
	if err != nil {
		if errors.Is(err, errr.ErrNotFound) {
			err = errr.WithMessage(errr.ErrNotFound, "tenant not found")
		}
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return modelTenant.TenantDTO{}, err
	}
	return modelTenant.ToDTO(t), nil
}

func validateSetup(in modelTenant.SetupInput) error {
	v := errr.Validation{}
	v.Check(modelTenant.SlugPattern.MatchString(in.TenantSlug), "tenant_slug",
		"must be 3-63 chars of lowercase letters, digits or dashes")
	v.Check(validName(in.TenantName), "tenant_name", "required, max 200 chars")
	v.Check(in.Preset == modelTenant.PresetSchool || in.Preset == modelTenant.PresetCompany ||
		in.Preset == modelTenant.PresetCustom, "preset", "must be school, company or custom")
	v.Check(modelAuth.ValidUsername(in.AdminUsername), "admin_username", modelAuth.UsernameRule)
	v.Check(in.AdminFullName != "" && len(in.AdminFullName) <= 200, "admin_full_name", "required, max 200 chars")
	v.Check(modelAuth.ValidPassword(in.AdminPassword), "admin_password",
		fmt.Sprintf("must be %d-%d characters", modelAuth.PasswordMinLen, modelAuth.PasswordMaxBytes))
	return v.Err()
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}
