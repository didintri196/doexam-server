package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	modelHealth "doexam/models/health"
	"doexam/pkg/errr"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func newTestService(err error) *HealthService {
	return NewHealthService(fakePinger{err: err}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestLiveness(t *testing.T) {
	// Liveness must stay up even when the database is down.
	dto := newTestService(errors.New("db down")).Liveness(context.Background())

	if dto.Status != modelHealth.StatusUp {
		t.Fatalf("status = %q, want %q", dto.Status, modelHealth.StatusUp)
	}
	if dto.Version != "test" {
		t.Fatalf("version = %q, want %q", dto.Version, "test")
	}
	if dto.Checks != nil {
		t.Fatalf("liveness must not run dependency checks, got %v", dto.Checks)
	}
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus modelHealth.Status
		wantErr    error
	}{
		{name: "database up", pingErr: nil, wantStatus: modelHealth.StatusUp, wantErr: nil},
		{name: "database down", pingErr: errors.New("connection refused"), wantStatus: modelHealth.StatusDown, wantErr: errr.ErrUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dto, err := newTestService(tt.pingErr).Readiness(context.Background())

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if dto.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", dto.Status, tt.wantStatus)
			}
			if got := dto.Checks["database"].Status; got != tt.wantStatus {
				t.Fatalf("database check = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}
