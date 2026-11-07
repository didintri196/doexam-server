package dbpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"gorm.io/gorm"

	"doexam/pkg/metrics"
)

// pgStats is one reading of the PostgreSQL statistics views.
type pgStats struct {
	Connections []struct {
		State string
		Count int64
	}
	LockWaiting int64
	Database    struct {
		XactCommit   int64
		XactRollback int64
		Deadlocks    int64
		BlksHit      int64
		BlksRead     int64
		TempBytes    int64
		Size         int64
	}
	Tables []pgTableStats
}

type pgTableStats struct {
	Relname          string
	NLiveTup         int64
	NDeadTup         int64
	AutovacuumCount  int64
	AutoanalyzeCount int64
	LastAutovacuum   *float64 // unix seconds
	LastAutoanalyze  *float64
}

// RegisterPGStats exposes server-side PostgreSQL statistics (doexam_pg_*): connections
// of this database by state, sessions waiting on a lock, transactions, deadlocks, cache
// hits, database size, and dead tuples / autovacuum of the given tables. The views are
// read at most once per interval. They are readable by the (non-owner) app role.
func RegisterPGStats(db *gorm.DB, tables []string, interval time.Duration) error {
	cached := metrics.NewCached(interval, func(ctx context.Context) (pgStats, error) { return readPGStats(ctx, db, tables) })
	m := otel.Meter("doexam.db")
	var errs []error
	gauge := func(name, unit, desc string) metric.Int64ObservableGauge {
		g, err := m.Int64ObservableGauge(name, metric.WithUnit(unit), metric.WithDescription(desc))
		errs = append(errs, err)
		return g
	}
	counter := func(name, unit, desc string) metric.Int64ObservableCounter {
		c, err := m.Int64ObservableCounter(name, metric.WithUnit(unit), metric.WithDescription(desc))
		errs = append(errs, err)
		return c
	}
	conns := gauge("doexam.pg.connections", "{connection}", "Client connections to this database by state (pg_stat_activity).")
	lockWait := gauge("doexam.pg.lock_waiting", "{session}", "Sessions of this database waiting for a lock.")
	commits := counter("doexam.pg.xact.commit", "{transaction}", "Committed transactions (pg_stat_database).")
	rollbacks := counter("doexam.pg.xact.rollback", "{transaction}", "Rolled back transactions (pg_stat_database).")
	deadlocks := counter("doexam.pg.deadlocks", "{deadlock}", "Deadlocks detected (pg_stat_database).")
	blksHit := counter("doexam.pg.blocks.hit", "{block}", "Blocks found in shared buffers.")
	blksRead := counter("doexam.pg.blocks.read", "{block}", "Blocks read from disk (or the OS cache).")
	tempBytes := counter("doexam.pg.temp", "By", "Bytes written to temporary files by queries.")
	size := gauge("doexam.pg.database.size", "By", "Size of this database.")
	live := gauge("doexam.pg.table.live_tuples", "{tuple}", "Estimated live rows of a table.")
	dead := gauge("doexam.pg.table.dead_tuples", "{tuple}", "Estimated dead rows of a table, waiting for vacuum.")
	vacuums := counter("doexam.pg.table.autovacuum", "{run}", "Autovacuum runs on a table.")
	analyzes := counter("doexam.pg.table.autoanalyze", "{run}", "Autoanalyze runs on a table.")
	lastVacuum := gauge("doexam.pg.table.last_autovacuum", "s", "Unix time of the last autovacuum of a table.")
	lastAnalyze := gauge("doexam.pg.table.last_autoanalyze", "s", "Unix time of the last autoanalyze of a table.")
	if err := errors.Join(errs...); err != nil {
		return err
	}
	_, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		st, err := cached.Get(ctx)
		if err != nil && len(st.Connections) == 0 {
			return nil // nothing read yet; dependency_up reports the outage
		}
		for _, c := range st.Connections {
			o.ObserveInt64(conns, c.Count, metric.WithAttributes(attribute.String("state", c.State)))
		}
		o.ObserveInt64(lockWait, st.LockWaiting)
		d := st.Database
		o.ObserveInt64(commits, d.XactCommit)
		o.ObserveInt64(rollbacks, d.XactRollback)
		o.ObserveInt64(deadlocks, d.Deadlocks)
		o.ObserveInt64(blksHit, d.BlksHit)
		o.ObserveInt64(blksRead, d.BlksRead)
		o.ObserveInt64(tempBytes, d.TempBytes)
		o.ObserveInt64(size, d.Size)
		for _, t := range st.Tables {
			attrs := metric.WithAttributes(attribute.String("table", t.Relname))
			o.ObserveInt64(live, t.NLiveTup, attrs)
			o.ObserveInt64(dead, t.NDeadTup, attrs)
			o.ObserveInt64(vacuums, t.AutovacuumCount, attrs)
			o.ObserveInt64(analyzes, t.AutoanalyzeCount, attrs)
			if t.LastAutovacuum != nil {
				o.ObserveInt64(lastVacuum, int64(*t.LastAutovacuum), attrs)
			}
			if t.LastAutoanalyze != nil {
				o.ObserveInt64(lastAnalyze, int64(*t.LastAutoanalyze), attrs)
			}
		}
		return nil
	}, conns, lockWait, commits, rollbacks, deadlocks, blksHit, blksRead, tempBytes, size, live, dead, vacuums, analyzes, lastVacuum, lastAnalyze)
	return err
}

func readPGStats(ctx context.Context, db *gorm.DB, tables []string) (pgStats, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn := db.WithContext(ctx)
	var st pgStats
	if err := conn.Raw(`SELECT coalesce(state, 'unknown') AS state, count(*) AS count FROM pg_stat_activity
		WHERE datname = current_database() AND backend_type = 'client backend' GROUP BY 1`).Scan(&st.Connections).Error; err != nil {
		return pgStats{}, fmt.Errorf("read pg_stat_activity: %w", err)
	}
	if err := conn.Raw(`SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&st.LockWaiting).Error; err != nil {
		return pgStats{}, fmt.Errorf("read lock waits: %w", err)
	}
	if err := conn.Raw(`SELECT xact_commit, xact_rollback, deadlocks, blks_hit, blks_read, temp_bytes,
		pg_database_size(datname) AS size FROM pg_stat_database WHERE datname = current_database()`).Scan(&st.Database).Error; err != nil {
		return pgStats{}, fmt.Errorf("read pg_stat_database: %w", err)
	}
	if len(tables) > 0 {
		if err := conn.Raw(`SELECT relname, n_live_tup, n_dead_tup, autovacuum_count, autoanalyze_count,
			extract(epoch FROM last_autovacuum)::float8 AS last_autovacuum, extract(epoch FROM last_autoanalyze)::float8 AS last_autoanalyze
			FROM pg_stat_user_tables WHERE schemaname = current_schema() AND relname IN ?`, tables).Scan(&st.Tables).Error; err != nil {
			return pgStats{}, fmt.Errorf("read pg_stat_user_tables: %w", err)
		}
	}
	return st, nil
}
