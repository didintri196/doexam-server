package dbpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"doexam/pkg/metrics"
)

// SlowQueryThreshold is the duration above which a query is logged and counted as slow.
const SlowQueryThreshold = 200 * time.Millisecond

const startKey = "doexam:metrics:start"

// dbMetrics holds the query and transaction instruments.
type dbMetrics struct {
	query  metric.Float64Histogram
	slow   metric.Int64Counter
	errors metric.Int64Counter
}

func newDBMetrics() (*dbMetrics, error) {
	m := otel.Meter("doexam.db")
	d := &dbMetrics{}
	var errs [3]error
	d.query, errs[0] = m.Float64Histogram("doexam.db.query.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of SQL statements by operation and table."), metric.WithExplicitBucketBoundaries(metrics.LatencyBuckets()...))
	d.slow, errs[1] = m.Int64Counter("doexam.db.slow_queries", metric.WithUnit("{query}"),
		metric.WithDescription("SQL statements slower than 200 ms."))
	d.errors, errs[2] = m.Int64Counter("doexam.db.errors", metric.WithUnit("{error}"),
		metric.WithDescription("SQL statements that failed, by kind (SQLSTATE class)."))
	return d, errors.Join(errs[:]...)
}

// InstrumentDB records query durations, slow queries and errors of every GORM statement.
// Call it once, after metrics.Setup.
func InstrumentDB(db *gorm.DB) error {
	d, err := newDBMetrics()
	if err != nil {
		return err
	}
	cb := db.Callback()
	regs := []struct {
		op  string
		reg func(before, after func(*gorm.DB)) error
	}{
		{"select", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Query().Before("gorm:query").Register("metrics:before_query", b),
				cb.Query().After("gorm:query").Register("metrics:after_query", a))
		}},
		{"insert", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Create().Before("gorm:create").Register("metrics:before_create", b),
				cb.Create().After("gorm:create").Register("metrics:after_create", a))
		}},
		{"update", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Update().Before("gorm:update").Register("metrics:before_update", b),
				cb.Update().After("gorm:update").Register("metrics:after_update", a))
		}},
		{"delete", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Delete().Before("gorm:delete").Register("metrics:before_delete", b),
				cb.Delete().After("gorm:delete").Register("metrics:after_delete", a))
		}},
		{"row", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Row().Before("gorm:row").Register("metrics:before_row", b),
				cb.Row().After("gorm:row").Register("metrics:after_row", a))
		}},
		{"raw", func(b, a func(*gorm.DB)) error {
			return errors.Join(cb.Raw().Before("gorm:raw").Register("metrics:before_raw", b),
				cb.Raw().After("gorm:raw").Register("metrics:after_raw", a))
		}},
	}
	for _, r := range regs {
		if err := r.reg(before, d.after(r.op)); err != nil {
			return err
		}
	}
	return nil
}

func before(db *gorm.DB) { db.InstanceSet(startKey, time.Now()) }

func (d *dbMetrics) after(op string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		v, ok := db.InstanceGet(startKey)
		start, _ := v.(time.Time)
		if !ok || start.IsZero() {
			return
		}
		elapsed := time.Since(start)
		ctx := db.Statement.Context
		if ctx == nil {
			ctx = context.Background()
		}
		table := tableName(db.Statement.Table, op)
		attrs := metric.WithAttributes(attribute.String("db.operation", op), attribute.String("db.table", table))
		d.query.Record(ctx, elapsed.Seconds(), attrs)
		if elapsed > SlowQueryThreshold {
			d.slow.Add(ctx, 1, metric.WithAttributes(attribute.String("db.table", table)))
		}
		if err := db.Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			d.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", ErrorKind(err))))
		}
	}
}

// tableName keeps the label bounded: raw SQL has no table, aliases are dropped.
func tableName(table, op string) string {
	if table == "" {
		if op == "raw" || op == "row" {
			return "raw"
		}
		return "unknown"
	}
	if i := strings.IndexByte(table, ' '); i > 0 {
		table = table[:i]
	}
	return strings.Trim(table, `"`)
}

// ErrorKind classifies a database error for metrics.
func ErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "40P01":
			return "deadlock"
		case pgErr.Code == "40001":
			return "serialization"
		case pgErr.Code == "55P03":
			return "lock_timeout"
		case pgErr.Code == "57014":
			return "statement_timeout"
		case pgErr.Code == "23505":
			return "unique_violation"
		case pgErr.Code == "23503":
			return "foreign_key_violation"
		case strings.HasPrefix(pgErr.Code, "23"):
			return "constraint_violation"
		case strings.HasPrefix(pgErr.Code, "08"), strings.HasPrefix(pgErr.Code, "53"), strings.HasPrefix(pgErr.Code, "57P"):
			return "connection"
		case pgErr.Code == "42501":
			return "permission"
		}
		return "other"
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return "connection"
	}
	return "other"
}

// txName is the span name of the service method that opened the transaction
// ("AttemptService.Sync"): a fixed set, so it can be a label.
func txName(ctx context.Context) string {
	if s, ok := trace.SpanFromContext(ctx).(interface{ Name() string }); ok && s.Name() != "" {
		return s.Name()
	}
	return "unknown"
}

func newTxHistogram() metric.Float64Histogram {
	h, err := otel.Meter("doexam.db").Float64Histogram("doexam.db.tx.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of tenant transactions by the service method that opened them."),
		metric.WithExplicitBucketBoundaries(metrics.LatencyBuckets()...))
	metrics.Report(err)
	return h
}

func recordTx(ctx context.Context, h metric.Float64Histogram, start time.Time, err error) {
	outcome := "commit"
	if err != nil {
		outcome = "rollback"
	}
	h.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("name", txName(ctx)), attribute.String("outcome", outcome)))
}
