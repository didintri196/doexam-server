package main

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"gorm.io/gorm"

	"doexam/app/server/handler"
	"doexam/business/attempt"
	"doexam/business/auth"
	"doexam/business/exam"
	"doexam/business/group"
	"doexam/business/health"
	"doexam/business/importer"
	"doexam/business/itembank"
	"doexam/business/media"
	"doexam/business/report"
	"doexam/business/session"
	"doexam/business/subject"
	"doexam/business/tenant"
	"doexam/business/user"
	"doexam/config"
	modelImport "doexam/models/importer"
	"doexam/pkg/bytecache"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/mediaurl"
	"doexam/pkg/metrics"
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
	attemptRepo := attempt.NewAttemptRepo(db)
	reportRepo := report.NewReportRepo(db)
	mediaRepo := media.NewMediaRepo(db)

	// ── External Providers ─────────────────────────────────────
	hasher := password.NewHasher(password.DefaultParams, 0).Named("login")
	// Imports hash with half the CPUs and their own limit, so logins keep flowing.
	importHasher := password.NewHasher(password.DefaultParams, max(1, runtime.NumCPU()/2)).Named("import")
	localStore, err := storage.NewLocalStore(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	fileStore := storage.Instrument(localStore)
	credentialVault := ttlcache.New[[]modelImport.Credential](credentialTTL)
	mediaKey, err := mediaSigningKey(context.Background(), cfg.MediaSigningKey, localStore)
	if err != nil {
		return nil, err
	}
	// Links live ≥12 h (a long exam day) and change once an hour, so caches can reuse them.
	mediaSigner := mediaurl.NewSigner(mediaKey, 12*time.Hour, time.Hour)
	contentCache, closeCache, pingCache := newContentCache(cfg, logger)
	deps := map[string]metrics.Check{"postgres": dbPinger.Ping}
	if pingCache != nil {
		deps["redis"] = pingCache
	}
	if err := errors.Join(metrics.RegisterDependencies(deps, 15*time.Second), metrics.RegisterDiskFree("files", cfg.StorageDir)); err != nil {
		return nil, err
	}

	// ── Services (Core) ────────────────────────────────────────
	healthSvc := health.NewHealthService(dbPinger, version, logger)
	mediaSvc := media.NewMediaService(mediaRepo, txManager, fileStore, mediaSigner, logger)
	tenantSvc := tenant.NewTenantService(tenantRepo, txManager, hasher, fileStore, mediaSigner, logger)
	authSvc := auth.NewAuthService(authRepo, txManager, hasher, cfg.SessionTTL, cfg.AuthCacheTTL, logger)
	userSvc := user.NewUserService(userRepo, txManager, hasher, authSvc, logger)
	groupSvc := group.NewGroupService(groupRepo, txManager, logger)
	importSvc := importer.NewImportService(importRepo, txManager, fileStore, importHasher, credentialVault, logger)
	subjectSvc := subject.NewSubjectService(subjectRepo, txManager, logger)
	itemBankSvc := itembank.NewItemBankService(itemBankRepo, txManager, mediaSvc, logger)
	examSvc := exam.NewExamService(examRepo, txManager, logger)
	announcementBoard := session.NewAnnouncementBoard(sessionRepo)
	attemptSvc := attempt.NewAttemptService(attemptRepo, txManager, mediaSvc, announcementBoard, contentCache, logger)
	sessionSvc := session.NewSessionService(sessionRepo, txManager, attemptSvc, importHasher, mediaSigner, announcementBoard, logger)
	reportSvc := report.NewReportService(reportRepo, txManager, logger)

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
	attemptHandler := handler.NewAttemptHandler(attemptSvc)
	gradingHandler := handler.NewGradingHandler(attemptSvc)
	reportHandler := handler.NewReportHandler(reportSvc)
	mediaHandler := handler.NewMediaHandler(mediaSvc)

	// ── Middleware ──────────────────────────────────────────────
	httpMetrics := middleware.NewHTTPMetrics()
	app.Use(httpMetrics.Handler(app, "/healthz", "/readyz")) // outermost: sees panics as 500
	app.Use(httpMetrics.Recover(logger))
	app.Use(middleware.Tracing(cfg.OTELService))
	app.Use(middleware.RequestLogger(logger))

	requireAuth := authHandler.RequireAuth
	// Per IP + username: a whole school lab often shares one public IP,
	// so limiting per IP alone would lock out every participant.
	loginLimiter := limiter.New(limiter.Config{
		Max:          10,
		Expiration:   time.Minute,
		KeyGenerator: loginLimiterKey,
		LimitReached: tooManyRequests(httpMetrics, "login"),
	})
	setupLimiter := limiter.New(limiter.Config{Max: 5, Expiration: time.Minute, LimitReached: tooManyRequests(httpMetrics, "setup")})

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

	v1.Get("/public/tenant", tenantHandler.Public)
	v1.Get("/public/templates/:name", handler.PublicTemplate)
	v1.Get("/public/tenant/logo", tenantHandler.Logo)
	// Signed links work without login (<img> cannot send the Authorization header).
	v1.Get("/media/:id", mediaHandler.Serve)
	v1.Post("/media", requireAuth, mediaHandler.Upload)
	v1.Post("/media/links", requireAuth, mediaHandler.Links)

	v1.Get("/tenant", requireAuth, tenantHandler.Current)
	v1.Patch("/tenant", requireAuth, tenantHandler.Update)
	v1.Put("/tenant/logo", requireAuth, tenantHandler.SetLogo)
	v1.Delete("/tenant/logo", requireAuth, tenantHandler.RemoveLogo)
	v1.Put("/tenant/card-signature", requireAuth, tenantHandler.SetCardSignature)
	v1.Delete("/tenant/card-signature", requireAuth, tenantHandler.RemoveCardSignature)
	v1.Get("/roles", requireAuth, userHandler.ListRoles)

	users := v1.Group("/users", requireAuth)
	users.Get("/", userHandler.List)
	users.Post("/", userHandler.Create)
	users.Post("/resolve", userHandler.Resolve)
	users.Post("/resolve-file", userHandler.ResolveFile)
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
	groups.Post("/:id/members/remove", groupHandler.RemoveMembers)
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
	banks.Get("/import-word/template", itemBankHandler.WordTemplate)
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
	banks.Post("/:id/import-word", itemBankHandler.ImportWord)

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
	sessions.Get("/:id/print", sessionHandler.Print)
	sessions.Post("/:id/card-passwords", sessionHandler.CardPasswords)
	sessions.Post("/:id/token/rotate", sessionHandler.RotateToken)
	sessions.Put("/:id/proctors", sessionHandler.SetProctors)
	sessions.Get("/:id/participants", sessionHandler.ListParticipants)
	sessions.Post("/:id/participants", sessionHandler.AddParticipants)
	sessions.Patch("/:id/participants/:user_id", sessionHandler.UpdateParticipant)
	sessions.Delete("/:id/participants/:user_id", sessionHandler.RemoveParticipant)
	v1.Get("/my/sessions", requireAuth, sessionHandler.MySessions)
	sessions.Post("/:id/announcements", sessionHandler.Announce)
	sessions.Get("/:id/announcements", sessionHandler.ListAnnouncements)
	sessions.Get("/:id/announcements/:aid/recipients", sessionHandler.AnnouncementRecipients)
	sessions.Post("/:id/announcements/:aid/retract", sessionHandler.RetractAnnouncement)
	v1.Post("/announcements", requireAuth, sessionHandler.Broadcast)
	v1.Get("/my/announcements", requireAuth, sessionHandler.MyAnnouncements)
	v1.Post("/my/announcements/:aid/read", requireAuth, sessionHandler.ReadAnnouncement)
	sessions.Get("/:id/monitor", attemptHandler.Monitor) // legacy: every participant; replaced by list/watch
	sessions.Get("/:id/monitor/list", attemptHandler.MonitorList)
	sessions.Post("/:id/monitor/watch", attemptHandler.MonitorWatch)

	attempts := v1.Group("/attempts", requireAuth)
	attempts.Post("/", attemptHandler.Start)
	attempts.Get("/:id", attemptHandler.Get)
	attempts.Post("/:id/events", attemptHandler.Sync)
	attempts.Post("/:id/sections/:section_id/finish", attemptHandler.FinishSection)
	attempts.Post("/:id/submit", attemptHandler.Submit)
	attempts.Get("/:id/result", attemptHandler.Result)
	attempts.Post("/:id/extend", attemptHandler.Extend)
	attempts.Post("/:id/lock", attemptHandler.Lock)
	attempts.Post("/:id/grant-plays", attemptHandler.GrantPlays)
	attempts.Post("/:id/unlock", attemptHandler.Unlock)
	attempts.Post("/:id/reset-device", attemptHandler.ResetDevice)
	attempts.Post("/:id/force-submit", attemptHandler.ForceSubmit)
	v1.Get("/my/attempts", requireAuth, attemptHandler.MyAttempts)

	exams.Get("/:id/grading/summary", gradingHandler.Summary)
	exams.Get("/:id/grading", gradingHandler.Queue)
	exams.Get("/:id/adjustments", gradingHandler.ListAdjustments)
	exams.Post("/:id/adjustments", gradingHandler.CreateAdjustment)
	v1.Get("/grading/items/:id", requireAuth, gradingHandler.Item)
	v1.Post("/grading/items/:id", requireAuth, gradingHandler.Grade)
	v1.Delete("/adjustments/:id", requireAuth, gradingHandler.RevokeAdjustment)

	exams.Get("/:id/recap", reportHandler.Recap)
	exams.Get("/:id/recap/export", reportHandler.RecapExport)
	exams.Get("/:id/analysis", reportHandler.Analysis)
	exams.Post("/:id/analysis", reportHandler.Recompute)
	attempts.Get("/:id/report", reportHandler.AttemptReport)

	shutdown := func(ctx context.Context) error {
		err := errors.Join(importSvc.Shutdown(ctx), attemptSvc.Shutdown(ctx))
		return errors.Join(err, closeCache())
	}
	return shutdown, nil
}

// contentRedisTTL bounds how long item content stays in Redis; versions never change,
// so this only keeps old exams from occupying memory (Redis also evicts by LRU).
const contentRedisTTL = 24 * time.Hour

// newContentCache returns the item content cache: Redis when REDIS_URL is set, an
// in-memory LRU otherwise. An unreachable Redis is only a warning: every read falls
// back to the database until it answers. The last value pings Redis (nil for memory).
func newContentCache(cfg config.DoExamConfig, logger *slog.Logger) (attempt.ContentCache, func() error, metrics.Check) {
	memory := func() (attempt.ContentCache, func() error, metrics.Check) {
		return bytecache.Instrument(bytecache.NewMemory(cfg.ContentCacheMB<<20), "memory"), func() error { return nil }, nil
	}
	if cfg.RedisURL == "" {
		logger.InfoContext(context.Background(), "item content cache: in memory", slog.Int("max_mb", cfg.ContentCacheMB))
		return memory()
	}
	client, err := bytecache.OpenRedis(context.Background(), cfg.RedisURL)
	if client == nil {
		logger.ErrorContext(context.Background(), "item content cache: invalid REDIS_URL, using memory", slog.String("error", err.Error()))
		return memory()
	}
	if err != nil {
		logger.WarnContext(context.Background(), "item content cache: redis not reachable yet", slog.String("error", err.Error()))
	} else {
		logger.InfoContext(context.Background(), "item content cache: redis")
	}
	ping := func(ctx context.Context) error { return client.Ping(ctx).Err() }
	return bytecache.Instrument(bytecache.NewRedis(client, "doexam:", contentRedisTTL), "redis"), client.Close, ping
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

// tooManyRequests answers 429 and counts the hit of the named limiter.
func tooManyRequests(m *middleware.HTTPMetrics, limiter string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		m.RateLimited(c.UserContext(), limiter)
		return handler.ErrorResponse(c, fiber.StatusTooManyRequests, "too many requests", nil)
	}
}
