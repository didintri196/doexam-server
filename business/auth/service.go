package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
	"doexam/pkg/securetoken"
)

const (
	// tokenEntropyBytes is the random part of a session token.
	tokenEntropyBytes = 32
	// touchInterval throttles last_used_at writes to one per session per interval.
	touchInterval = time.Minute
)

var errInvalidCredentials = errr.WithMessage(errr.ErrUnauthorized, "invalid credentials")

// AuthService handles login, session validation, logout and password changes.
//
// Token format: "<tenant_id>.<random>". The tenant prefix lets the server open a
// tenant-scoped transaction (RLS) before looking up the session; only the
// SHA-256 of the whole token is stored, so a forged prefix finds nothing.
type AuthService struct {
	storer     AuthStorer
	tx         TxManager
	hasher     PasswordHasher
	sessionTTL time.Duration
	now        func() time.Time
	tracer     trace.Tracer
	logger     *slog.Logger
}

// NewAuthService returns a concrete AuthService.
func NewAuthService(storer AuthStorer, tx TxManager, hasher PasswordHasher, sessionTTL time.Duration, logger *slog.Logger) *AuthService {
	return &AuthService{
		storer:     storer,
		tx:         tx,
		hasher:     hasher,
		sessionTTL: sessionTTL,
		now:        time.Now,
		tracer:     otel.Tracer("doexam.service.auth"),
		logger:     logger,
	}
}

// Login checks credentials and opens a new session.
// Every failure returns the same "invalid credentials" error, so callers cannot
// learn whether the tenant or username exists.
func (s *AuthService) Login(ctx context.Context, in modelAuth.LoginInput, client modelAudit.Client) (modelAuth.LoginDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AuthService.Login")
	defer span.End()

	dto, err := s.login(ctx, in, client)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return modelAuth.LoginDTO{}, err
	}
	span.SetAttributes(attribute.String("tenant.id", dto.User.Tenant.ID), attribute.String("user.id", dto.User.ID))
	return dto, nil
}

func (s *AuthService) login(ctx context.Context, in modelAuth.LoginInput, client modelAudit.Client) (modelAuth.LoginDTO, error) {
	in.Normalize()
	v := errr.Validation{}
	v.Check(in.Tenant != "", "tenant", "required")
	v.Check(in.Username != "", "username", "required")
	v.Check(in.Password != "" && len(in.Password) <= modelAuth.PasswordMaxBytes, "password", "required")
	if err := v.Err(); err != nil {
		return modelAuth.LoginDTO{}, err
	}

	tenant, err := s.storer.FindTenantBySlug(ctx, in.Tenant)
	if errors.Is(err, errr.ErrNotFound) || (err == nil && !tenant.IsActive) {
		s.hasher.VerifyDummy(ctx, in.Password)
		return modelAuth.LoginDTO{}, errInvalidCredentials
	}
	if err != nil {
		return modelAuth.LoginDTO{}, err
	}

	// 1st transaction: read the user. Password verification runs outside any
	// transaction because Argon2 is slow and must not hold a DB connection.
	var user modelAuth.UserEntity
	err = s.tx.WithTenant(ctx, tenant.ID, func(ctx context.Context) error {
		user, err = s.storer.FindUserByUsername(ctx, in.Username)
		return err
	})
	if errors.Is(err, errr.ErrNotFound) || (err == nil && user.PasswordHash == nil) {
		s.hasher.VerifyDummy(ctx, in.Password)
		return modelAuth.LoginDTO{}, errInvalidCredentials
	}
	if err != nil {
		return modelAuth.LoginDTO{}, err
	}

	ok, err := s.hasher.Verify(ctx, *user.PasswordHash, in.Password)
	if err != nil {
		return modelAuth.LoginDTO{}, fmt.Errorf("verify password of user %s: %w", user.ID, err)
	}
	if !ok || user.Status != modelAuth.UserStatusActive {
		reason := "wrong_password"
		if ok {
			reason = "user_" + string(user.Status)
		}
		s.auditLoginFailed(ctx, tenant.ID, user.ID, reason, client)
		return modelAuth.LoginDTO{}, errInvalidCredentials
	}

	token, err := securetoken.Generate(tokenEntropyBytes)
	if err != nil {
		return modelAuth.LoginDTO{}, err
	}
	token = tenant.ID + "." + token
	now := s.now().UTC()
	session := modelAuth.AuthSessionEntity{
		ID: newID(), TenantID: tenant.ID, UserID: user.ID, TokenHash: securetoken.Hash(token),
		IP: nonEmpty(client.IP), UserAgent: nonEmpty(modelAudit.Truncate(client.UserAgent, 500)),
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(s.sessionTTL),
	}

	// 2nd transaction: open the session.
	var roles []modelAuth.RoleEntity
	err = s.tx.WithTenant(ctx, tenant.ID, func(ctx context.Context) error {
		if err := s.storer.CreateSession(ctx, &session); err != nil {
			return err
		}
		if err := s.storer.UpdateLastLogin(ctx, user.ID, now); err != nil {
			return err
		}
		if roles, err = s.storer.FindUserRoles(ctx, user.ID); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, auditEntry(tenant.ID, user.ID, modelAudit.ActionLogin, "auth_session", session.ID, nil, client))
	})
	if err != nil {
		return modelAuth.LoginDTO{}, err
	}

	s.logger.InfoContext(ctx, "user logged in",
		slog.String("tenant_id", tenant.ID), slog.String("user_id", user.ID), slog.String("session_id", session.ID))
	user.LastLoginAt = &now
	return modelAuth.LoginDTO{
		Token:     token,
		ExpiresAt: session.ExpiresAt,
		User:      modelAuth.ToMeDTO(user, tenantSummary(tenant), roles),
	}, nil
}

// auditLoginFailed records a failed login for a known user. It runs in its own
// transaction so the record survives the failed login; errors are only logged.
func (s *AuthService) auditLoginFailed(ctx context.Context, tenantID, userID, reason string, client modelAudit.Client) {
	err := s.tx.WithTenant(ctx, tenantID, func(ctx context.Context) error {
		return s.storer.CreateAuditLog(ctx,
			auditEntry(tenantID, userID, modelAudit.ActionLoginFailed, "user", userID, map[string]any{"reason": reason}, client))
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to write login audit log", slog.String("error", err.Error()))
	}
	s.logger.WarnContext(ctx, "login failed",
		slog.String("tenant_id", tenantID), slog.String("user_id", userID), slog.String("reason", reason))
}

// Authenticate resolves a bearer token to a Principal. It returns errr.ErrUnauthorized
// for malformed, unknown, revoked or expired tokens and for suspended users/tenants.
func (s *AuthService) Authenticate(ctx context.Context, token string) (modelAuth.Principal, error) {
	ctx, span := s.tracer.Start(ctx, "AuthService.Authenticate")
	defer span.End()

	p, err := s.authenticate(ctx, token)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return modelAuth.Principal{}, err
	}
	span.SetAttributes(attribute.String("tenant.id", p.TenantID), attribute.String("user.id", p.UserID))
	return p, nil
}

func (s *AuthService) authenticate(ctx context.Context, token string) (modelAuth.Principal, error) {
	tenantID, _, found := strings.Cut(token, ".")
	if !found || uuid.Validate(tenantID) != nil {
		return modelAuth.Principal{}, errr.ErrUnauthorized
	}

	tenant, err := s.storer.FindTenantByID(ctx, tenantID)
	if errors.Is(err, errr.ErrNotFound) || (err == nil && !tenant.IsActive) {
		return modelAuth.Principal{}, errr.ErrUnauthorized
	}
	if err != nil {
		return modelAuth.Principal{}, err
	}

	now := s.now().UTC()
	var p modelAuth.Principal
	err = s.tx.WithTenant(ctx, tenantID, func(ctx context.Context) error {
		session, err := s.storer.FindSessionByHash(ctx, securetoken.Hash(token))
		if err != nil {
			return err
		}
		if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			return errr.ErrUnauthorized
		}
		user, err := s.storer.FindUserByID(ctx, session.UserID)
		if err != nil {
			return err
		}
		if user.Status != modelAuth.UserStatusActive {
			return errr.ErrUnauthorized
		}
		roles, err := s.storer.FindUserRoles(ctx, user.ID)
		if err != nil {
			return err
		}
		if now.Sub(session.LastUsedAt) >= touchInterval {
			if err := s.storer.TouchSession(ctx, session.ID, now); err != nil {
				return err
			}
		}
		me := modelAuth.ToMeDTO(user, modelAuth.TenantSummaryDTO{}, roles)
		p = modelAuth.Principal{
			TenantID: tenantID, UserID: user.ID, SessionID: session.ID,
			Roles: me.Roles, Permissions: me.Permissions,
		}
		return nil
	})
	if errors.Is(err, errr.ErrNotFound) {
		return modelAuth.Principal{}, errr.ErrUnauthorized
	}
	if err != nil {
		return modelAuth.Principal{}, err
	}
	return p, nil
}

// Me returns the profile, tenant, roles and permissions of the principal.
func (s *AuthService) Me(ctx context.Context, p modelAuth.Principal) (modelAuth.MeDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AuthService.Me")
	defer span.End()

	dto, err := s.me(ctx, p)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return modelAuth.MeDTO{}, err
	}
	return dto, nil
}

func (s *AuthService) me(ctx context.Context, p modelAuth.Principal) (modelAuth.MeDTO, error) {
	tenant, err := s.storer.FindTenantByID(ctx, p.TenantID)
	if err != nil {
		return modelAuth.MeDTO{}, err
	}
	var dto modelAuth.MeDTO
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		user, err := s.storer.FindUserByID(ctx, p.UserID)
		if err != nil {
			return err
		}
		roles, err := s.storer.FindUserRoles(ctx, p.UserID)
		if err != nil {
			return err
		}
		dto = modelAuth.ToMeDTO(user, tenantSummary(tenant), roles)
		return nil
	})
	return dto, err
}

// Logout revokes the principal's current session.
func (s *AuthService) Logout(ctx context.Context, p modelAuth.Principal, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "AuthService.Logout")
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.storer.RevokeSession(ctx, p.SessionID, s.now().UTC()); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, auditEntry(p.TenantID, p.UserID, modelAudit.ActionLogout, "auth_session", p.SessionID, nil, client))
	})
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return err
	}
	return nil
}

// ChangePassword verifies the current password, stores the new one, clears
// must_change_password and revokes all other sessions of the user.
func (s *AuthService) ChangePassword(ctx context.Context, p modelAuth.Principal, in modelAuth.ChangePasswordInput, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "AuthService.ChangePassword")
	defer span.End()

	if err := s.changePassword(ctx, p, in, client); err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return err
	}
	s.logger.InfoContext(ctx, "password changed", slog.String("tenant_id", p.TenantID), slog.String("user_id", p.UserID))
	return nil
}

func (s *AuthService) changePassword(ctx context.Context, p modelAuth.Principal, in modelAuth.ChangePasswordInput, client modelAudit.Client) error {
	v := errr.Validation{}
	v.Check(in.CurrentPassword != "" && len(in.CurrentPassword) <= modelAuth.PasswordMaxBytes, "current_password", "required")
	v.Check(modelAuth.ValidPassword(in.NewPassword), "new_password",
		fmt.Sprintf("must be %d-%d characters", modelAuth.PasswordMinLen, modelAuth.PasswordMaxBytes))
	v.Check(in.NewPassword != in.CurrentPassword, "new_password", "must differ from the current password")
	if err := v.Err(); err != nil {
		return err
	}

	var user modelAuth.UserEntity
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		user, err = s.storer.FindUserByID(ctx, p.UserID)
		return err
	})
	if err != nil {
		return err
	}
	if user.PasswordHash == nil {
		return errr.WithMessage(errr.ErrForbidden, "account has no password")
	}
	ok, err := s.hasher.Verify(ctx, *user.PasswordHash, in.CurrentPassword)
	if err != nil {
		return fmt.Errorf("verify password of user %s: %w", user.ID, err)
	}
	if !ok {
		return &errr.ValidationError{Fields: map[string]string{"current_password": "incorrect"}}
	}
	hash, err := s.hasher.Hash(ctx, in.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.storer.UpdatePassword(ctx, user.ID, hash); err != nil {
			return err
		}
		if err := s.storer.RevokeUserSessions(ctx, user.ID, p.SessionID, s.now().UTC()); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, auditEntry(p.TenantID, user.ID, modelAudit.ActionPasswordChange, "user", user.ID, nil, client))
	})
}

func auditEntry(tenantID, actorID, action, entityType, entityID string, after map[string]any, client modelAudit.Client) *modelAudit.AuditLogEntity {
	e := modelAudit.Entry{
		TenantID: tenantID, ActorID: actorID, Action: action,
		EntityType: entityType, EntityID: entityID, Client: client,
	}
	if after != nil {
		e.After = after
	}
	return modelAudit.New(e)
}

func tenantSummary(t modelTenant.TenantEntity) modelAuth.TenantSummaryDTO {
	return modelAuth.TenantSummaryDTO{ID: t.ID, Slug: t.Slug, Name: t.Name, Preset: string(t.Preset)}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}
