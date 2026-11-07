package importer

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"doexam/pkg/metrics"
)

// importMetrics holds the instruments of background imports.
type importMetrics struct {
	duration metric.Float64Histogram
	rows     metric.Int64Counter
	wait     metric.Float64Histogram
}

func newImportMetrics() *importMetrics {
	m := otel.Meter("doexam.service.importer")
	im := &importMetrics{}
	buckets := metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600)
	var errs [3]error
	im.duration, errs[0] = m.Float64Histogram("doexam.import.duration", metric.WithUnit("s"), buckets,
		metric.WithDescription("Duration of background imports by kind and result (done, invalid, failed)."))
	im.rows, errs[1] = m.Int64Counter("doexam.import.rows", metric.WithUnit("{row}"),
		metric.WithDescription("Rows written by successful imports."))
	im.wait, errs[2] = m.Float64Histogram("doexam.import.queue.duration", metric.WithUnit("s"), buckets,
		metric.WithDescription("Time an import waited for the single import slot of this node."))
	metrics.Report(errs[:]...)
	return im
}

func (m *importMetrics) queued(ctx context.Context, kind string, start time.Time) {
	if m != nil {
		m.wait.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("kind", kind)))
	}
}

func (m *importMetrics) done(ctx context.Context, kind, result string, start time.Time, rows int) {
	if m == nil {
		return
	}
	k := attribute.String("kind", kind)
	m.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(k, attribute.String("result", result)))
	if rows > 0 {
		m.rows.Add(ctx, int64(rows), metric.WithAttributes(k))
	}
}
