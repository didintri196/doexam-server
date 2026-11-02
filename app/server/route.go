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
	"doexam/business/exam"
	"doexam/business/group"
	"doexam/business/health"
	"doexam/business/importer"
	"doexam/business/itembank"
	"doexam/business/session"
	"doexam/business/subject"
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
	subjectRepo := subject.NewSubjectRepo(db)
	itemBankRepo := itembank.NewItemBankRepo(db)
	examRepo := exam.NewExamRepo(db)
	sessionRepo := session.NewSessionRepo(db)

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
	subjectSvc := subject.NewSubjectService(subjectRepo, txManager, logger)
	itemBankSvc := itembank.NewItemBankService(itemBankRepo, txManager, logger)
	examSvc := exam.NewExamService(examRepo, txManager, logger)
	sessionSvc := session.NewSessionService(sessionRepo, txManager, noAttempts{}, logger)

	// ── Handlers ───────────────────────────────────────────────
	healthHandler := handler.NewHealthHandler(healthSvc)
	tenantHandler := handler.NewTenantHandler(tenantSvc)
	authHandler := handler.NewAuthHandler(authSvc)
	userHandler := handler.NewUserHandler(userSvc)
	groupHandler := handler.NewGroupHandler(groupSvc)
	importHandler := handler.NewImportHandler(importSvc)
	subjectHandler := handler.NewSubjectHandler(subjectSvc)
	itemBankHandler := handler.NewItemBankHandler(itemBankSvc)
	examHandler := handler.NewExamHandler(examSvc)
	sessionHandler := handler.NewSessionHandler(sessionSvc)

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

	subjects := v1.Group("/subjects", requireAuth)
	subjects.Get("/", subjectHandler.List)
	subjects.Post("/", subjectHandler.Create)
	subjects.Get("/:id", subjectHandler.Get)
	subjects.Patch("/:id", subjectHandler.Update)
	subjects.Delete("/:id", subjectHandler.Delete)
	subjects.Get("/:id/competencies", subjectHandler.ListCompetencies)
	subjects.Post("/:id/competencies", subjectHandler.CreateCompetency)
	v1.Patch("/competencies/:id", requireAuth, subjectHandler.UpdateCompetency)
	v1.Delete("/competencies/:id", requireAuth, subjectHandler.DeleteCompetency)

	banks := v1.Group("/item-banks", requireAuth)
	banks.Get("/", itemBankHandler.ListBanks)
	banks.Post("/", itemBankHandler.CreateBank)
	banks.Get("/:id", itemBankHandler.GetBank)
	banks.Patch("/:id", itemBankHandler.UpdateBank)
	banks.Delete("/:id", itemBankHandler.DeleteBank)
	banks.Get("/:id/members", itemBankHandler.ListMembers)
	banks.Post("/:id/members", itemBankHandler.SetMember)
	banks.Delete("/:id/members/:user_id", itemBankHandler.RemoveMember)
	banks.Get("/:id/stimuli", itemBankHandler.ListStimuli)
	banks.Post("/:id/stimuli", itemBankHandler.CreateStimulus)
	banks.Get("/:id/items", itemBankHandler.ListItems)
	banks.Post("/:id/items", itemBankHandler.CreateItem)

	stimuli := v1.Group("/stimuli", requireAuth)
	stimuli.Get("/:id", itemBankHandler.GetStimulus)
	stimuli.Patch("/:id", itemBankHandler.UpdateStimulus)
	stimuli.Delete("/:id", itemBankHandler.DeleteStimulus)

	items := v1.Group("/items", requireAuth)
	items.Get("/:id", itemBankHandler.GetItem)
	items.Patch("/:id", itemBankHandler.UpdateItem)
	items.Delete("/:id", itemBankHandler.DeleteItem)
	items.Get("/:id/versions", itemBankHandler.ListVersions)

	exams := v1.Group("/exams", requireAuth)
	exams.Get("/", examHandler.List)
	exams.Post("/", examHandler.Create)
	exams.Get("/:id", examHandler.Get)
	exams.Patch("/:id", examHandler.Update)
	exams.Delete("/:id", examHandler.Delete)
	exams.Get("/:id/check", examHandler.Check)
	exams.Post("/:id/publish", examHandler.Publish)
	exams.Post("/:id/unpublish", examHandler.Unpublish)
	exams.Post("/:id/archive", examHandler.Archive)
	exams.Post("/:id/sections", examHandler.CreateSection)
	exams.Post("/:id/sections/order", examHandler.OrderSections)

	sections := v1.Group("/sections", requireAuth)
	sections.Patch("/:id", examHandler.UpdateSection)
	sections.Delete("/:id", examHandler.DeleteSection)
	sections.Post("/:id/items", examHandler.AddItems)
	sections.Post("/:id/items/order", examHandler.OrderSectionItems)
	sections.Post("/:id/rules", examHandler.CreateRule)
	v1.Patch("/section-items/:id", requireAuth, examHandler.UpdateSectionItem)
	v1.Delete("/section-items/:id", requireAuth, examHandler.RemoveSectionItem)
	v1.Patch("/section-rules/:id", requireAuth, examHandler.UpdateRule)
	v1.Delete("/section-rules/:id", requireAuth, examHandler.DeleteRule)

	sessions := v1.Group("/sessions", requireAuth)
	sessions.Get("/", sessionHandler.List)
	sessions.Post("/", sessionHandler.Create)
	sessions.Get("/:id", sessionHandler.Get)
	sessions.Patch("/:id", sessionHandler.Update)
	sessions.Delete("/:id", sessionHandler.Delete)
	sessions.Post("/:id/status", sessionHandler.SetStatus)
	sessions.Get("/:id/token", sessionHandler.Token)
	sessions.Post("/:id/token/rotate", sessionHandler.RotateToken)
	sessions.Put("/:id/proctors", sessionHandler.SetProctors)
	sessions.Get("/:id/participants", sessionHandler.ListParticipants)
	sessions.Post("/:id/participants", sessionHandler.AddParticipants)
	sessions.Patch("/:id/participants/:user_id", sessionHandler.UpdateParticipant)
	sessions.Delete("/:id/participants/:user_id", sessionHandler.RemoveParticipant)
	v1.Get("/my/sessions", requireAuth, sessionHandler.MySessions)

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

// noAttempts stands in for the attempt engine until it exists: closing a session has no attempts to submit.
type noAttempts struct{}

func (noAttempts) FinalizeSessionAttempts(context.Context, string, string) (int, error) {
	return 0, nil
}
