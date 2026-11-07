package storage

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"doexam/pkg/metrics"
)

// Store is what Instrumented wraps (LocalStore today, S3 later).
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// Instrumented wraps a Store with operation durations, transferred bytes and errors.
// A missing key (ErrNotFound) is not an error.
type Instrumented struct {
	next     Store
	duration metric.Float64Histogram
	bytes    metric.Int64Counter
	errors   metric.Int64Counter
}

var _ Store = (*Instrumented)(nil)

// Instrument returns store wrapped with metrics.
func Instrument(store Store) *Instrumented {
	m := otel.Meter("doexam.storage")
	in := &Instrumented{next: store}
	var errs [3]error
	in.duration, errs[0] = m.Float64Histogram("doexam.storage.operation.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of file storage operations."), metric.WithExplicitBucketBoundaries(metrics.LatencyBuckets()...))
	in.bytes, errs[1] = m.Int64Counter("doexam.storage.transferred", metric.WithUnit("By"),
		metric.WithDescription("Bytes written to or read from file storage by successful operations."))
	in.errors, errs[2] = m.Int64Counter("doexam.storage.errors", metric.WithUnit("{error}"),
		metric.WithDescription("Failed file storage operations."))
	metrics.Report(errs[:]...)
	return in
}

// Put writes data under key.
func (in *Instrumented) Put(ctx context.Context, key string, data []byte) error {
	start := time.Now()
	err := in.next.Put(ctx, key, data)
	in.done(ctx, "put", start, len(data), err)
	return err
}

// Get reads the data of key.
func (in *Instrumented) Get(ctx context.Context, key string) ([]byte, error) {
	start := time.Now()
	data, err := in.next.Get(ctx, key)
	in.done(ctx, "get", start, len(data), err)
	return data, err
}

func (in *Instrumented) done(ctx context.Context, op string, start time.Time, n int, err error) {
	attrs := metric.WithAttributes(attribute.String("op", op))
	in.duration.Record(ctx, time.Since(start).Seconds(), attrs)
	if n > 0 && err == nil {
		in.bytes.Add(ctx, int64(n), attrs)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		in.errors.Add(ctx, 1, attrs)
	}
}
