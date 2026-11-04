package user

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	"doexam/pkg/errr"
	"doexam/pkg/securetoken"
)

// temporaryPasswordLen is the length of generated passwords (≈ 57 bits of entropy).
const temporaryPasswordLen = 10

var (
	errUserNotFound = errr.WithMessage(errr.ErrNotFound, "user not found")
	errSelf         = errr.WithMessage(errr.ErrForbidden, "cannot suspend, delete or reset yourself")
	errLastAdmin    = errr.WithMessage(errr.ErrForbidden, "tenant must keep at least one active admin")
)

// UserService manages the users of a tenant. Every method requires user.manage, except
// Resolve/ResolveFile (also group.manage or session.manage).
type UserService struct {
	storer UserStorer
	tx     TxManager
	hasher PasswordHasher
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewUserService returns a concrete UserService.
func NewUserService(storer UserStorer, tx TxManager, hasher PasswordHasher, logger *slog.Logger) *UserService {
	return &UserService{
		storer: storer,
		tx:     tx,
		hasher: hasher,
		now:    time.Now,
		tracer: otel.Tracer("doexam.service.user"),
		logger: logger,
	}
}

// ListRoles returns the tenant's roles (for the role picker).
func (s *UserService) ListRoles(ctx context.Context, p modelAuth.Principal) ([]modelUser.RoleDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.ListRoles")
	defer span.End()

	var out []modelUser.RoleDTO
	err := s.run(ctx, span, p, func(ctx context.Context) error {
		roles, err := s.storer.ListRoles(ctx)
		if err != nil {
			return err
		}
		out = make([]modelUser.RoleDTO, 0, len(roles))
		for _, r := range roles {
			out = append(out, modelUser.ToRoleDTO(r))
		}
		return nil
	})
	return out, err
}

// List returns one page of users matching f.
func (s *UserService) List(ctx context.Context, p modelAuth.Principal, f modelUser.ListFilter) ([]modelUser.UserDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.List")
	defer span.End()

	v := errr.Validation{}
	v.Check(f.Status == "" || f.Status == modelAuth.UserStatusActive || f.Status == modelAuth.UserStatusSuspended,
		"status", "must be active or suspended")
	v.Check(f.GroupID == "" || uuid.Validate(f.GroupID) == nil, "group_id", "must be a UUID")
	v.Check(f.Kind == "" || f.Kind == modelUser.KindStaff || f.Kind == modelUser.KindParticipant, "kind", "must be staff or participant")
	if err := v.Err(); err != nil {
		return nil, 0, err
	}

	var (
		out   []modelUser.UserDTO
		total int64
	)
	err := s.run(ctx, span, p, func(ctx context.Context) error {
		users, n, err := s.storer.List(ctx, f)
		if err != nil {
			return err
		}
		ids := make([]string, len(users))
		for i, u := range users {
			ids[i] = u.ID
		}
		roles, err := s.storer.RolesOfUsers(ctx, ids)
		if err != nil {
			return err
		}
		out = make([]modelUser.UserDTO, 0, len(users))
		for _, u := range users {
			out = append(out, modelUser.ToDTO(u, roles[u.ID]))
		}
		total = n
		return nil
	})
	return out, total, err
}

// Get returns a user with roles and group memberships.
func (s *UserService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelUser.UserDetailDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Get", trace.WithAttributes(attribute.String("user.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelUser.UserDetailDTO{}, errUserNotFound
	}
	var out modelUser.UserDetailDTO
	err := s.run(ctx, span, p, func(ctx context.Context) error {
		u, roles, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		groups, err := s.storer.GroupsOfUser(ctx, id)
		if err != nil {
			return err
		}
		out = modelUser.UserDetailDTO{UserDTO: modelUser.ToDTO(u, roles), Groups: groups}
		return nil
	})
	return out, err
}

// Create adds a user. When in.Password is empty a temporary password is generated
// and returned once; the user must change it at first login (unless told otherwise).
func (s *UserService) Create(ctx context.Context, p modelAuth.Principal, in modelUser.CreateInput, client modelAudit.Client) (modelUser.CreatedDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Create")
	defer span.End()

	if !p.Has(modelAuth.PermUserManage) {
		return modelUser.CreatedDTO{}, fail(span, errr.ErrForbidden)
	}
	in.Normalize()
	if len(in.RoleCodes) == 0 {
		in.RoleCodes = []string{modelAuth.RoleParticipant}
	}
	v := errr.Validation{}
	v.Check(modelAuth.ValidUsername(in.Username), "username", modelAuth.UsernameRule)
	v.Check(in.FullName != "" && len(in.FullName) <= 200, "full_name", "required, max 200 chars")
	v.Check(in.Email == nil || modelUser.ValidEmail(*in.Email), "email", "must be a valid e-mail address")
	v.Check(in.ExternalID == nil || len(*in.ExternalID) <= 100, "external_id", "max 100 chars")
	v.Check(in.Password == "" || modelAuth.ValidPassword(in.Password), "password",
		fmt.Sprintf("must be %d-%d characters", modelAuth.PasswordMinLen, modelAuth.PasswordMaxBytes))
	if err := v.Err(); err != nil {
		return modelUser.CreatedDTO{}, fail(span, err)
	}

	pw, temporary := in.Password, ""
	if pw == "" {
		var err error
		if pw, err = securetoken.ReadablePassword(temporaryPasswordLen); err != nil {
			return modelUser.CreatedDTO{}, fail(span, err)
		}
		temporary = pw
	}
	mustChange := in.MustChangePassword == nil || *in.MustChangePassword
	// Hash outside the transaction: Argon2 is slow and must not hold a DB connection.
	hash, err := s.hasher.Hash(ctx, pw)
	if err != nil {
		return modelUser.CreatedDTO{}, fail(span, fmt.Errorf("hash password: %w", err))
	}

	u := modelAuth.UserEntity{
		ID: newID(), TenantID: p.TenantID, Username: in.Username, Email: in.Email, ExternalID: in.ExternalID,
		PasswordHash: &hash, FullName: in.FullName, Status: modelAuth.UserStatusActive,
		MustChangePassword: mustChange, CreatedAt: s.now().UTC(),
	}
	var dto modelUser.UserDTO
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		roles, err := s.resolveRoles(ctx, in.RoleCodes)
		if err != nil {
			return err
		}
		if err := s.storer.Create(ctx, &u); err != nil {
			return err
		}
		if err := s.storer.ReplaceRoles(ctx, p.TenantID, u.ID, roleIDs(roles)); err != nil {
			return err
		}
		dto = modelUser.ToDTO(u, roles)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionUserCreate,
			EntityType: "user", EntityID: u.ID, After: dto, Client: client,
		}))
	})
	if err != nil {
		return modelUser.CreatedDTO{}, fail(span, err)
	}
	s.logger.InfoContext(ctx, "user created", slog.String("tenant_id", p.TenantID), slog.String("user_id", u.ID))
	return modelUser.CreatedDTO{User: dto, TemporaryPassword: temporary}, nil
}

// Update changes profile fields, status and/or roles of a user.
// Suspending a user revokes all of their sessions.
func (s *UserService) Update(ctx context.Context, p modelAuth.Principal, id string, in modelUser.UpdateInput, client modelAudit.Client) (modelUser.UserDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Update", trace.WithAttributes(attribute.String("user.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelUser.UserDTO{}, errUserNotFound
	}
	in.Normalize()
	v := errr.Validation{}
	v.Check(in.FullName == nil || (*in.FullName != "" && len(*in.FullName) <= 200), "full_name", "required, max 200 chars")
	v.Check(in.Email == nil || *in.Email == "" || modelUser.ValidEmail(*in.Email), "email", "must be a valid e-mail address")
	v.Check(in.ExternalID == nil || len(*in.ExternalID) <= 100, "external_id", "max 100 chars")
	v.Check(in.Status == nil || *in.Status == modelAuth.UserStatusActive || *in.Status == modelAuth.UserStatusSuspended,
		"status", "must be active or suspended")
	v.Check(in.RoleCodes == nil || len(*in.RoleCodes) > 0, "role_codes", "at least one role")
	if err := v.Err(); err != nil {
		return modelUser.UserDTO{}, fail(span, err)
	}

	var dto modelUser.UserDTO
	err := s.run(ctx, span, p, func(ctx context.Context) error {
		u, roles, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		before := modelUser.ToDTO(u, roles)

		newRoles := roles
		if in.RoleCodes != nil {
			if newRoles, err = s.resolveRoles(ctx, *in.RoleCodes); err != nil {
				return err
			}
		}
		suspending := in.Status != nil && *in.Status == modelAuth.UserStatusSuspended && u.Status != modelAuth.UserStatusSuspended
		if suspending && id == p.UserID {
			return errSelf
		}
		if hasAdmin(roles) && (!hasAdmin(newRoles) || suspending) {
			if err := s.ensureAnotherAdmin(ctx, id); err != nil {
				return err
			}
		}

		if in.FullName != nil {
			u.FullName = *in.FullName
		}
		if in.Email != nil {
			u.Email = optional(*in.Email)
		}
		if in.ExternalID != nil {
			u.ExternalID = optional(*in.ExternalID)
		}
		if in.Status != nil {
			u.Status = *in.Status
		}
		if err := s.storer.Update(ctx, &u); err != nil {
			return err
		}
		if in.RoleCodes != nil {
			if err := s.storer.ReplaceRoles(ctx, p.TenantID, id, roleIDs(newRoles)); err != nil {
				return err
			}
		}
		if suspending {
			if err := s.storer.RevokeSessions(ctx, id, s.now().UTC()); err != nil {
				return err
			}
		}
		dto = modelUser.ToDTO(u, newRoles)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionUserUpdate,
			EntityType: "user", EntityID: id, Before: before, After: dto, Client: client,
		}))
	})
	return dto, err
}

// Delete soft-deletes a user and revokes their sessions. The username becomes reusable.
func (s *UserService) Delete(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "UserService.Delete", trace.WithAttributes(attribute.String("user.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return errUserNotFound
	}
	if id == p.UserID {
		return fail(span, errSelf)
	}
	return s.run(ctx, span, p, func(ctx context.Context) error {
		u, roles, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		if hasAdmin(roles) {
			if err := s.ensureAnotherAdmin(ctx, id); err != nil {
				return err
			}
		}
		if err := s.storer.SoftDelete(ctx, id); err != nil {
			return err
		}
		if err := s.storer.RevokeSessions(ctx, id, s.now().UTC()); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionUserDelete,
			EntityType: "user", EntityID: id, Before: modelUser.ToDTO(u, roles), Client: client,
		}))
	})
}

// ResetPassword sets a new temporary password (returned once), forces a change at
// next login and revokes all sessions. Use change-password for your own account.
func (s *UserService) ResetPassword(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelUser.PasswordResetDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.ResetPassword", trace.WithAttributes(attribute.String("user.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelUser.PasswordResetDTO{}, errUserNotFound
	}
	if !p.Has(modelAuth.PermUserManage) {
		return modelUser.PasswordResetDTO{}, fail(span, errr.ErrForbidden)
	}
	if id == p.UserID {
		return modelUser.PasswordResetDTO{}, fail(span, errSelf)
	}
	pw, err := securetoken.ReadablePassword(temporaryPasswordLen)
	if err != nil {
		return modelUser.PasswordResetDTO{}, fail(span, err)
	}
	hash, err := s.hasher.Hash(ctx, pw)
	if err != nil {
		return modelUser.PasswordResetDTO{}, fail(span, fmt.Errorf("hash password: %w", err))
	}

	err = s.run(ctx, span, p, func(ctx context.Context) error {
		if _, err := s.storer.FindByID(ctx, id); err != nil {
			return err
		}
		if err := s.storer.SetPassword(ctx, id, hash, true); err != nil {
			return err
		}
		if err := s.storer.RevokeSessions(ctx, id, s.now().UTC()); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionUserPasswordReset,
			EntityType: "user", EntityID: id, Client: client,
		}))
	})
	if err != nil {
		return modelUser.PasswordResetDTO{}, err
	}
	return modelUser.PasswordResetDTO{TemporaryPassword: pw}, nil
}

// run checks user.manage and executes fn in the principal's tenant transaction,
// recording any error on span.
func (s *UserService) run(ctx context.Context, span trace.Span, p modelAuth.Principal, fn func(ctx context.Context) error) error {
	if !p.Has(modelAuth.PermUserManage) {
		return fail(span, errr.ErrForbidden)
	}
	if err := s.tx.WithTenant(ctx, p.TenantID, fn); err != nil {
		return fail(span, err)
	}
	return nil
}

func (s *UserService) load(ctx context.Context, id string) (modelAuth.UserEntity, []modelAuth.RoleEntity, error) {
	u, err := s.storer.FindByID(ctx, id)
	if err != nil {
		return modelAuth.UserEntity{}, nil, err
	}
	roles, err := s.storer.RolesOfUsers(ctx, []string{id})
	if err != nil {
		return modelAuth.UserEntity{}, nil, err
	}
	return u, roles[id], nil
}

// resolveRoles maps role codes to roles of the current tenant; unknown codes are a validation error.
func (s *UserService) resolveRoles(ctx context.Context, codes []string) ([]modelAuth.RoleEntity, error) {
	roles, err := s.storer.FindRolesByCodes(ctx, codes)
	if err != nil {
		return nil, err
	}
	for _, c := range codes {
		if !slices.ContainsFunc(roles, func(r modelAuth.RoleEntity) bool { return r.Code == c }) {
			return nil, &errr.ValidationError{Fields: map[string]string{"role_codes": "unknown role: " + c}}
		}
	}
	return roles, nil
}

// ensureAnotherAdmin fails when userID is the last active admin.
func (s *UserService) ensureAnotherAdmin(ctx context.Context, userID string) error {
	if err := s.storer.LockAdminRole(ctx); err != nil {
		return err
	}
	n, err := s.storer.CountActiveAdmins(ctx, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		return errLastAdmin
	}
	return nil
}

func hasAdmin(roles []modelAuth.RoleEntity) bool {
	return slices.ContainsFunc(roles, func(r modelAuth.RoleEntity) bool { return r.Code == modelAuth.RoleAdmin })
}

func roleIDs(roles []modelAuth.RoleEntity) []string {
	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = r.ID
	}
	return ids
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
