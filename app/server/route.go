package main

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"gorm.io/gorm"

	"doexam/app/server/handler"
	"doexam/business/auth"
	"doexam/business/group"
	"doexam/business/health"
	"doexam/business/importer"
	"doexam/business/tenant"
	"doexam/business/user"
	"doexam/config"
	modelImport "doexam/models/importer"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/middleware"
	"doexam/pkg/password"
	"doexam/pkg/storage"
	"doexam/pkg/ttlcache"
)

// credentialTTL is how long generated import passwords stay downloadable (memory only).
const credentialTTL = time.Hour

// PrepareRoute wires all dependencies and registers routes. The returned function
// waits for background work (running imports) during graceful shutdown.
func PrepareRoute(app *fiber.App, cfg config.DoExamConfig, db *gorm.DB, logger *slog.Logger) (func(context.Context) error, error) {

	// ── Repositories (Adapters) ────────────────────────────────
	dbPinger := health.NewDBPingRepo(db)
	txManager := dbpg.NewTxManager(db)
	tenantRepo := tenant.NewTenantRepo(db)
	authRepo := auth.NewAuthRepo(db)
	userRepo := user.NewUserRepo(db)
	groupRepo := group.NewGroupRepo(db)
	importRepo := importer.NewImportRepo(db)

	// ── External Providers ─────────────────────────────────────
	hasher := password.NewHasher(password.DefaultParams, 0)
	// Imports hash with half the CPUs and their own limit, so logins keep flowing.
	importHasher := password.NewHasher(password.DefaultParams, max(1, runtime.NumCPU()/2))
	fileStore, err := storage.NewLocalStore(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	credentialVault := ttlcache.New[[]modelImport.Credential](credentialTTL)

	// ── Services (Core) ────────────────────────────────────────
	healthSvc := health.NewHealthService(dbPinger, version, logger)
	tenantSvc := tenant.NewTenantService(tenantRepo, txManager, hasher, logger)
	authSvc := auth.NewAuthService(authRepo, txManager, hasher, cfg.SessionTTL, logger)
	userSvc := user.NewUserService(userRepo, txManager, hasher, logger)
	groupSvc := group.NewGroupService(groupRepo, txManager, logger)
	importSvc := importer.NewImportService(importRepo, txManager, fileStore, importHasher, credentialVault, logger)

	// ── Handlers ───────────────────────────────────────────────
	healthHandler := handler.NewHealthHandler(healthSvc)
	tenantHandler := handler.NewTenantHandler(tenantSvc)
	authHandler := handler.NewAuthHandler(authSvc)
	userHandler := handler.NewUserHandler(userSvc)
	groupHandler := handler.NewGroupHandler(groupSvc)
	importHandler := handler.NewImportHandler(importSvc)

	// ── Middleware ──────────────────────────────────────────────
	app.Use(recover.New())
	app.Use(middleware.Tracing(cfg.OTELService))
	app.Use(middleware.RequestLogger(logger))

	requireAuth := authHandler.RequireAuth
	// Per IP + username: a whole school lab often shares one public IP,
	// so limiting per IP alone would lock out every participant.
	loginLimiter := limiter.New(limiter.Config{
		Max:          10,
		Expiration:   time.Minute,
		KeyGenerator: loginLimiterKey,
		LimitReached: tooManyRequests,
	})
	setupLimiter := limiter.New(limiter.Config{Max: 5, Expiration: time.Minute, LimitReached: tooManyRequests})

	// ── Routes ─────────────────────────────────────────────────
	app.Get("/healthz", healthHandler.Healthz)
	app.Get("/readyz", healthHandler.Readyz)

	v1 := app.Group("/api/v1")
	v1.Post("/setup", setupLimiter, tenantHandler.Setup)

	authGroup := v1.Group("/auth")
	authGroup.Post("/login", loginLimiter, authHandler.Login)
	authGroup.Get("/me", requireAuth, authHandler.Me)
	authGroup.Post("/logout", requireAuth, authHandler.Logout)
	authGroup.Post("/change-password", requireAuth, authHandler.ChangePassword)

	v1.Get("/tenant", requireAuth, tenantHandler.Current)
	v1.Get("/roles", requireAuth, userHandler.ListRoles)

	users := v1.Group("/users", requireAuth)
	users.Get("/", userHandler.List)
	users.Post("/", userHandler.Create)
	users.Get("/:id", userHandler.Get)
	users.Patch("/:id", userHandler.Update)
	users.Delete("/:id", userHandler.Delete)
	users.Post("/:id/reset-password", userHandler.ResetPassword)

	groups := v1.Group("/groups", requireAuth)
	groups.Get("/", groupHandler.List)
	groups.Post("/", groupHandler.Create)
	groups.Get("/:id", groupHandler.Get)
	groups.Patch("/:id", groupHandler.Update)
	groups.Delete("/:id", groupHandler.Delete)
	groups.Get("/:id/members", groupHandler.ListMembers)
	groups.Post("/:id/members", groupHandler.AddMembers)
	groups.Delete("/:id/members/:user_id", groupHandler.RemoveMember)

	imports := v1.Group("/imports", requireAuth)
	imports.Get("/templates/:kind", importHandler.Template)
	imports.Get("/", importHandler.List)
	imports.Post("/", importHandler.Upload)
	imports.Get("/:id", importHandler.Get)
	imports.Post("/:id/commit", importHandler.Commit)
	imports.Get("/:id/credentials", importHandler.Credentials)

	return importSvc.Shutdown, nil
}

// loginLimiterKey is the rate-limit key for login: client IP + tenant + username.
func loginLimiterKey(c *fiber.Ctx) string {
	var body struct {
		Tenant   string `json:"tenant"`
		Username string `json:"username"`
	}
	_ = c.BodyParser(&body) // malformed body: fall back to IP only; the handler rejects it anyway
	return c.IP() + "|" + strings.ToLower(strings.TrimSpace(body.Tenant)) + "|" + strings.ToLower(strings.TrimSpace(body.Username))
}

func tooManyRequests(c *fiber.Ctx) error {
	return handler.ErrorResponse(c, fiber.StatusTooManyRequests, "too many requests", nil)
}
