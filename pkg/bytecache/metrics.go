package bytecache

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"doexam/pkg/metrics"
)

// Instrumented wraps a Store with metrics: lookups by result (hit/miss) and by cache
// (the key prefix before the first ':', e.g. "item-content", "exam-plan"), operation
// durations and errors, labeled with backend ("memory", "redis").
type Instrumented struct {
	next     Store
	backend  attribute.KeyValue
	requests metric.Int64Counter
	duration metric.Float64Histogram
	errors   metric.Int64Counter
}

var _ Store = (*Instrumented)(nil)

// Instrument returns store wrapped with metrics. For a *Memory it also exposes the
// number of entries and their size.
func Instrument(store Store, backend string) *Instrumented {
	m := otel.Meter("doexam.cache")
	in := &Instrumented{next: store, backend: attribute.String("backend", backend)}
	var errs [3]error
	in.requests, errs[0] = m.Int64Counter("doexam.cache.requests", metric.WithUnit("{key}"),
		metric.WithDescription("Cache lookups per key, by cache and result (hit/miss)."))
	in.duration, errs[1] = m.Float64Histogram("doexam.cache.operation.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of cache operations."),
		metric.WithExplicitBucketBoundaries(0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1))
	in.errors, errs[2] = m.Int64Counter("doexam.cache.errors", metric.WithUnit("{error}"),
		metric.WithDescription("Failed cache operations (the caller falls back to the database)."))
	metrics.Report(errs[:]...)
	if mem, ok := store.(*Memory); ok {
		metrics.Report(registerMemory(m, mem))
	}
	return in
}

// GetMulti counts a hit or a miss for every key.
func (in *Instrumented) GetMulti(ctx context.Context, keys []string) (map[string][]byte, error) {
	start := time.Now()
	out, err := in.next.GetMulti(ctx, keys)
	in.done(ctx, "get", start, err)
	hits, misses := map[string]int64{}, map[string]int64{}
	for _, k := range keys {
		if _, ok := out[k]; ok {
			hits[cacheName(k)]++
		} else {
			misses[cacheName(k)]++
		}
	}
	for name, n := range hits {
		in.requests.Add(ctx, n, metric.WithAttributes(in.backend, attribute.String("cache", name), attribute.String("result", "hit")))
	}
	for name, n := range misses {
		in.requests.Add(ctx, n, metric.WithAttributes(in.backend, attribute.String("cache", name), attribute.String("result", "miss")))
	}
	return out, err
}

// SetMulti stores items.
func (in *Instrumented) SetMulti(ctx context.Context, items map[string][]byte) error {
	start := time.Now()
	err := in.next.SetMulti(ctx, items)
	in.done(ctx, "set", start, err)
	return err
}

func (in *Instrumented) done(ctx context.Context, op string, start time.Time, err error) {
	opAttr := attribute.String("op", op)
	in.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(in.backend, opAttr))
	if err != nil {
		in.errors.Add(ctx, 1, metric.WithAttributes(in.backend, opAttr))
	}
}

// cacheName is the key prefix before the first ':' (keys are "<cache>:<version>:...").
func cacheName(key string) string {
	if i := strings.IndexByte(key, ':'); i > 0 {
		return key[:i]
	}
	return "other"
}

func registerMemory(m metric.Meter, mem *Memory) error {
	entries, err1 := m.Int64ObservableGauge("doexam.cache.entries", metric.WithUnit("{entry}"),
		metric.WithDescription("Entries in the in-memory cache."))
	size, err2 := m.Int64ObservableGauge("doexam.cache.size", metric.WithUnit("By"),
		metric.WithDescription("Approximate bytes held by the in-memory cache."))
	limit, err3 := m.Int64ObservableGauge("doexam.cache.limit", metric.WithUnit("By"),
		metric.WithDescription("Byte limit of the in-memory cache (CONTENT_CACHE_MB)."))
	if err := errors.Join(err1, err2, err3); err != nil {
		return err
	}
	attrs := metric.WithAttributes(attribute.String("backend", "memory"))
	_, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		n, bytes := mem.stats()
		o.ObserveInt64(entries, int64(n), attrs)
		o.ObserveInt64(size, int64(bytes), attrs)
		o.ObserveInt64(limit, int64(mem.maxBytes), attrs)
		return nil
	}, entries, size, limit)
	return err
}

// stats returns the number of entries and their approximate size.
func (m *Memory) stats() (entries, size int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items), m.size
}
