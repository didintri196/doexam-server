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
	"gorm.io/gorm"

	"doexam/config"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/metrics"
	"doexam/pkg/mlog"
	"doexam/pkg/otelutil"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	// ── 1. Load config ──────────────────────────────────────────
	cfg := config.Load()
	exitIfCommand(cfg.DoExam) // migrate / version / help; returns for serve

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

	// ── 4. Metrics (before anything creates instruments) ───────
	var mtr *metrics.Metrics
	if cfg.DoExam.MetricsEnabled() {
		otlp := ""
		if cfg.DoExam.OTELMetricsExporter == "otlp" {
			otlp = cfg.DoExam.OTELEndpoint
		}
		mtr, err = metrics.Setup(ctx, metrics.Config{
			Addr:         cfg.DoExam.MetricsAddr,
			Token:        cfg.DoExam.MetricsToken,
			ServiceName:  cfg.DoExam.OTELService,
			Environment:  cfg.DoExam.Environment,
			Version:      version,
			OTLPEndpoint: otlp,
			Insecure:     cfg.DoExam.Environment != "production",
		})
		if err != nil {
			logger.Error("failed to setup metrics", slog.String("error", err.Error()))
			os.Exit(1)
		}
		if addr := mtr.Addr(); addr != "" {
			logger.Info("metrics listening", slog.String("address", addr), slog.Bool("token", cfg.DoExam.MetricsToken != ""))
		}
	} else {
		logger.Info("metrics disabled (METRICS_ADDR=off, OTEL_METRICS_EXPORTER=none)")
	}

	// ── 5. Database ────────────────────────────────────────────
	db, err := dbpg.OpenDB(dbpg.Config{
		DSN:         cfg.DoExam.DatabaseURL,
		Environment: cfg.DoExam.Environment,
	})
	if err != nil {
		logger.Error("failed to connect to database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if mtr != nil {
		if err := instrumentDB(db, mtr); err != nil {
			logger.Error("failed to instrument database", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	// ── 6. Fiber app ───────────────────────────────────────────
	app := fiber.New(fiber.Config{
		AppName:      "DoExam API",
		BodyLimit:    24 << 20, // 24 MiB: audio clips are capped at 20 MiB, imports at 5 MiB, by the services
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	})

	// ── 7. Wire dependencies and routes ────────────────────────
	shutdownWork, err := PrepareRoute(app, cfg.DoExam, db, logger)
	if err != nil {
		logger.Error("failed to prepare routes", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── 8. Graceful shutdown ───────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		addr := fmt.Sprintf(":%s", cfg.DoExam.ServerPort)
		logger.Info("server starting", slog.String("address", addr), slog.Bool("tls", cfg.DoExam.TLSEnabled()))
		var err error
		if cfg.DoExam.TLSEnabled() {
			err = app.ListenTLS(addr, cfg.DoExam.TLSCertFile, cfg.DoExam.TLSKeyFile)
		} else {
			err = app.Listen(addr)
		}
		if err != nil {
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
	if err := mtr.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics shutdown error", slog.String("error", err.Error()))
	}
	if err := shutdownOTEL(shutdownCtx); err != nil {
		logger.Error("OTEL shutdown error", slog.String("error", err.Error()))
	}

	logger.Info("server stopped")
}

// instrumentDB adds query metrics, pool statistics and PostgreSQL statistics.
func instrumentDB(db *gorm.DB, mtr *metrics.Metrics) error {
	if err := dbpg.InstrumentDB(db); err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if err := mtr.RegisterDBStats(sqlDB, "doexam"); err != nil {
		return err
	}
	// Dead tuples and autovacuum runs of the tables written on every sync during an
	// exam (DOCS/PLAN/14-UJI-BEBAN.md).
	tables := []string{"attempts", "attempt_items", "attempt_sections", "attempt_events", "auth_sessions", "audit_logs"}
	return dbpg.RegisterPGStats(db, tables, 30*time.Second)
}
