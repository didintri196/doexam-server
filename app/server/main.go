package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // tenant time zones are validated even on hosts without zoneinfo

	"github.com/gofiber/fiber/v2"

	"doexam/config"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/mlog"
	"doexam/pkg/otelutil"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	// ── 1. Load config ──────────────────────────────────────────
	cfg := config.Load()

	if err := cfg.DoExam.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// ── 2. Setup structured logger ─────────────────────────────
	logger := mlog.New(mlog.LoggerConfig{
		Environment: cfg.DoExam.Environment,
		Level:       slog.LevelInfo,
	})
	slog.SetDefault(logger)

	logger.Info("starting DoExam API",
		slog.String("version", version),
		slog.String("environment", cfg.DoExam.Environment),
		slog.String("port", cfg.DoExam.ServerPort),
	)

	// ── 3. Setup OTEL (export disabled when endpoint is empty) ─
	ctx := context.Background()
	shutdownOTEL, err := otelutil.SetupOTEL(ctx, otelutil.OTELConfig{
		Endpoint:    cfg.DoExam.OTELEndpoint,
		ServiceName: cfg.DoExam.OTELService,
		Environment: cfg.DoExam.Environment,
		Insecure:    cfg.DoExam.Environment != "production",
	})
	if err != nil {
		logger.Error("failed to setup OTEL", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if cfg.DoExam.OTELEndpoint == "" {
		logger.Info("OTEL export disabled (OTEL_EXPORTER_OTLP_ENDPOINT is empty); trace IDs still generated")
	}

	// ── 4. Database ────────────────────────────────────────────
	db, err := dbpg.OpenDB(dbpg.Config{
		DSN:         cfg.DoExam.DatabaseURL,
		Environment: cfg.DoExam.Environment,
	})
	if err != nil {
		logger.Error("failed to connect to database", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── 5. Fiber app ───────────────────────────────────────────
	app := fiber.New(fiber.Config{
		AppName:      "DoExam API",
		BodyLimit:    8 << 20, // 8 MiB: import files are capped at 5 MiB by the service
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	})

	// ── 6. Wire dependencies and routes ────────────────────────
	shutdownWork, err := PrepareRoute(app, cfg.DoExam, db, logger)
	if err != nil {
		logger.Error("failed to prepare routes", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── 7. Graceful shutdown ───────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		addr := fmt.Sprintf(":%s", cfg.DoExam.ServerPort)
		logger.Info("server starting", slog.String("address", addr))
		if err := app.Listen(addr); err != nil {
			logger.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-quit
	logger.Info("received shutdown signal, shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		logger.Error("server forced to shutdown", slog.String("error", err.Error()))
	}
	// Running imports still need the database, so wait for them before closing it.
	if err := shutdownWork(shutdownCtx); err != nil {
		logger.Error("background work did not finish", slog.String("error", err.Error()))
	}
	if err := dbpg.Close(db); err != nil {
		logger.Error("database close error", slog.String("error", err.Error()))
	}
	if err := shutdownOTEL(shutdownCtx); err != nil {
		logger.Error("OTEL shutdown error", slog.String("error", err.Error()))
	}

	logger.Info("server stopped")
}
