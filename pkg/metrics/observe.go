package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// checkTimeout bounds one dependency check, so a hung dependency cannot stall a scrape.
const checkTimeout = 2 * time.Second

// Check reports whether a dependency answers (nil = up).
type Check func(ctx context.Context) error

// RegisterDependencies exposes doexam_dependency_up{dependency="..."} (1 up, 0 down).
// Checks run at most once per minInterval; scrapes in between reuse the last result.
func RegisterDependencies(checks map[string]Check, minInterval time.Duration) error {
	meter := otel.Meter(MeterName)
	g, err := meter.Int64ObservableGauge("doexam.dependency.up",
		metric.WithDescription("Whether a dependency answers its health check (1 up, 0 down)."))
	if err != nil {
		return err
	}
	cached := NewCached(minInterval, func(ctx context.Context) (map[string]int64, error) {
		out := make(map[string]int64, len(checks))
		for name, check := range checks {
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			out[name] = 1
			if check(cctx) != nil {
				out[name] = 0
			}
			cancel()
		}
		return out, nil
	})
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		up, _ := cached.Get(ctx)
		for name, v := range up {
			o.ObserveInt64(g, v, metric.WithAttributes(attribute.String("dependency", name)))
		}
		return nil
	}, g)
	return err
}

// RegisterDiskFree exposes doexam_storage_free_bytes and doexam_storage_size_bytes for
// the file system holding path (local storage only; not reported where unsupported).
func RegisterDiskFree(name, path string) error {
	meter := otel.Meter(MeterName)
	free, err := meter.Int64ObservableGauge("doexam.storage.free", metric.WithUnit("By"),
		metric.WithDescription("Bytes available to the API on the file system of a storage directory."))
	if err != nil {
		return err
	}
	size, err := meter.Int64ObservableGauge("doexam.storage.size", metric.WithUnit("By"),
		metric.WithDescription("Total bytes of the file system of a storage directory."))
	if err != nil {
		return err
	}
	attrs := metric.WithAttributes(attribute.String("storage", name))
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		avail, total, ok := diskUsage(path)
		if ok {
			o.ObserveInt64(free, int64(avail), attrs)
			o.ObserveInt64(size, int64(total), attrs)
		}
		return nil
	}, free, size)
	return err
}

// Cached runs an expensive read (a database query, a ping) at most once per interval
// for observable instruments. Concurrent callers share the result; a failed read keeps
// the previous value.
type Cached[T any] struct {
	mu       sync.Mutex
	interval time.Duration
	load     func(ctx context.Context) (T, error)
	value    T
	at       time.Time
	err      error
	now      func() time.Time
}

// NewCached returns a Cached that calls load at most once per interval.
func NewCached[T any](interval time.Duration, load func(ctx context.Context) (T, error)) *Cached[T] {
	return &Cached[T]{interval: interval, load: load, now: time.Now}
}

// Get returns the cached value, reloading it when older than the interval.
func (c *Cached[T]) Get(ctx context.Context) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.at.IsZero() && now.Sub(c.at) < c.interval {
		return c.value, c.err
	}
	v, err := c.load(ctx)
	c.at, c.err = now, err
	if err == nil {
		c.value = v
	}
	return c.value, err
}
