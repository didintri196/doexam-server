package health

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelHealth "doexam/models/health"
	"doexam/pkg/errr"
)

// checkTimeout bounds each dependency check so a hung database
// cannot hang the readiness probe.
const checkTimeout = 2 * time.Second

// HealthService reports liveness and readiness of the API.
type HealthService struct {
	db        Pinger
	version   string
	startedAt time.Time
	tracer    trace.Tracer
	logger    *slog.Logger
}

// NewHealthService returns a concrete HealthService.
func NewHealthService(db Pinger, version string, logger *slog.Logger) *HealthService {
	return &HealthService{
		db:        db,
		version:   version,
		startedAt: time.Now(),
		tracer:    otel.Tracer("doexam.service.health"),
		logger:    logger,
	}
}

// Liveness reports that the process is running. It never touches dependencies,
// so an orchestrator does not restart the API just because the database is down.
func (s *HealthService) Liveness(ctx context.Context) modelHealth.HealthDTO {
	return modelHealth.HealthDTO{
		Status:        modelHealth.StatusUp,
		Version:       s.version,
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
	}
}

// Readiness checks every dependency the API needs to serve traffic.
// It returns errr.ErrUnavailable when any check is down.
func (s *HealthService) Readiness(ctx context.Context) (modelHealth.HealthDTO, error) {
	ctx, span := s.tracer.Start(ctx, "HealthService.Readiness")
	defer span.End()

	dto := s.Liveness(ctx)
	dto.Checks = map[string]modelHealth.CheckDTO{
		"database": s.check(ctx, "database", s.db),
	}

	for _, c := range dto.Checks {
		if c.Status == modelHealth.StatusDown {
			dto.Status = modelHealth.StatusDown
			span.SetStatus(codes.Error, "dependency down")
			return dto, errr.ErrUnavailable
		}
	}
	return dto, nil
}

func (s *HealthService) check(ctx context.Context, name string, p Pinger) modelHealth.CheckDTO {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	err := p.Ping(ctx)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		s.logger.WarnContext(ctx, "health check failed",
			slog.String("check", name), slog.String("error", err.Error()),
		)
		return modelHealth.CheckDTO{Status: modelHealth.StatusDown, LatencyMs: latency}
	}
	return modelHealth.CheckDTO{Status: modelHealth.StatusUp, LatencyMs: latency}
}
