// Package metrics sets up the OpenTelemetry meter provider and serves the metrics in
// Prometheus format on a separate listener (never on the public API port).
//
// Instruments are created anywhere with otel.Meter(...): until Setup runs, the global
// provider is a no-op, so tests and tools pay nothing.
package metrics

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// MeterName is the meter of instruments that belong to no domain (build info, dependencies).
const MeterName = "doexam"

// Config configures metrics.
type Config struct {
	// Addr is the listen address of /metrics ("127.0.0.1:9464"); empty disables the listener.
	Addr string
	// Token, when set, is required as "Authorization: Bearer <token>".
	Token       string
	ServiceName string
	Environment string
	Version     string
	// OTLPEndpoint, when set, also pushes metrics over OTLP/gRPC every PushInterval.
	OTLPEndpoint string
	Insecure     bool
	PushInterval time.Duration
}

// Metrics owns the meter provider and the /metrics listener.
type Metrics struct {
	registry *promclient.Registry
	provider *sdkmetric.MeterProvider
	server   *http.Server
	listener net.Listener
}

// Setup installs the global meter provider and, when cfg.Addr is set, starts serving
// /metrics. Go runtime and process metrics are added to the Prometheus output.
func Setup(ctx context.Context, cfg Config) (*Metrics, error) {
	reg := promclient.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsGC, collectors.MetricsScheduler)),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("deployment.environment", cfg.Environment),
	))
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}
	opts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithReader(exporter)}
	if cfg.OTLPEndpoint != "" {
		eopts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint)}
		if cfg.Insecure {
			eopts = append(eopts, otlpmetricgrpc.WithInsecure())
		}
		push, err := otlpmetricgrpc.New(ctx, eopts...)
		if err != nil {
			return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
		}
		interval := cfg.PushInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(push, sdkmetric.WithInterval(interval))))
	}
	provider := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)

	m := &Metrics{registry: reg, provider: provider}
	if err := registerBuildInfo(cfg.Version); err != nil {
		return nil, err
	}
	if cfg.Addr != "" {
		if err := m.listen(cfg.Addr, cfg.Token); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Addr is the address /metrics listens on ("" when disabled).
func (m *Metrics) Addr() string {
	if m == nil || m.listener == nil {
		return ""
	}
	return m.listener.Addr().String()
}

// RegisterDBStats adds the connection pool statistics of db (go_sql_* metrics).
// A nil Metrics (metrics disabled) ignores it.
func (m *Metrics) RegisterDBStats(db *sql.DB, name string) error {
	if m == nil {
		return nil
	}
	return m.registry.Register(collectors.NewDBStatsCollector(db, name))
}

// Shutdown stops the listener and flushes the provider.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var err error
	if m.server != nil {
		err = m.server.Shutdown(ctx)
	}
	return errors.Join(err, m.provider.Shutdown(ctx))
}

func (m *Metrics) listen(addr, token string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen metrics on %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", Protect(promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}), token))
	m.listener = ln
	m.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	go func() { _ = m.server.Serve(ln) }()
	return nil
}

// Protect requires "Authorization: Bearer <token>" when token is not empty.
func Protect(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func registerBuildInfo(version string) error {
	g, err := otel.Meter(MeterName).Int64ObservableGauge("doexam.build.info",
		metric.WithDescription("Build of the running binary (always 1)."))
	if err != nil {
		return err
	}
	attrs := metric.WithAttributes(attribute.String("version", version), attribute.String("go_version", runtime.Version()))
	_, err = otel.Meter(MeterName).RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(g, 1, attrs)
		return nil
	}, g)
	return err
}

// LatencyBuckets are the histogram boundaries (seconds) of request and query durations:
// dense below 500 ms, where the exam paths are expected to stay.
func LatencyBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// SizeBuckets are the histogram boundaries (bytes) of request and response bodies.
func SizeBuckets() []float64 {
	return []float64{256, 1 << 10, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20, 16 << 20}
}

// Report hands instrument creation errors (only possible for invalid names) to the
// OpenTelemetry error handler; instruments that failed are no-ops, never nil.
func Report(errs ...error) {
	if err := errors.Join(errs...); err != nil {
		otel.Handle(err)
	}
}
