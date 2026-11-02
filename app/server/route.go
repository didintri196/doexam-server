package main

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"gorm.io/gorm"

	"doexam/app/server/handler"
	"doexam/business/health"
	"doexam/config"
	"doexam/pkg/middleware"
)

// PrepareRoute wires all dependencies and registers routes. The returned function
// waits for background work (running imports) during graceful shutdown.
func PrepareRoute(app *fiber.App, cfg config.DoExamConfig, db *gorm.DB, logger *slog.Logger) (func(context.Context) error, error) {

	// ── Repositories (Adapters) ────────────────────────────────
	dbPinger := health.NewDBPingRepo(db)

	// ── External Providers ─────────────────────────────────────

	// ── Services (Core) ────────────────────────────────────────
	healthSvc := health.NewHealthService(dbPinger, version, logger)

	// ── Handlers ───────────────────────────────────────────────
	healthHandler := handler.NewHealthHandler(healthSvc)

	// ── Middleware ──────────────────────────────────────────────
	app.Use(recover.New())
	app.Use(middleware.Tracing(cfg.OTELService))
	app.Use(middleware.RequestLogger(logger))

	// ── Routes ─────────────────────────────────────────────────
	app.Get("/healthz", healthHandler.Healthz)
	app.Get("/readyz", healthHandler.Readyz)

	return func(context.Context) error { return nil }, nil
}
