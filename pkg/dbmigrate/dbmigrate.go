// Package dbmigrate applies SQL migrations from an fs.FS with golang-migrate (the same
// library as the migrate CLI, so the schema_migrations table stays compatible). Runs
// hold a PostgreSQL advisory lock: concurrent runs (a Helm hook and a starting pod)
// wait for each other instead of colliding.
package dbmigrate

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers pgx5://
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Status is the schema version and whether a failed run left it dirty.
type Status struct {
	Version uint
	Dirty   bool
	// None is true when no migration has been applied yet.
	None bool
}

// Migrator wraps a golang-migrate instance; Close it when done.
type Migrator struct {
	m *migrate.Migrate
}

// New opens the migrations in files (NNNNNN_name.up.sql / .down.sql) against databaseURL
// (postgres:// or postgresql://).
func New(files fs.FS, databaseURL string) (*Migrator, error) {
	src, err := iofs.New(files, ".")
	if err != nil {
		return nil, fmt.Errorf("open migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5URL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("connect for migrations: %w", err)
	}
	return &Migrator{m: m}, nil
}

// pgx5URL switches the scheme to the pgx v5 driver golang-migrate registers.
func pgx5URL(u string) string {
	for _, prefix := range []string{"postgresql://", "postgres://"} {
		if rest, ok := strings.CutPrefix(u, prefix); ok {
			return "pgx5://" + rest
		}
	}
	return u
}

// Up applies every pending migration. It reports whether anything changed.
func (x *Migrator) Up() (bool, error) {
	err := x.m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("migrate up: %w", err)
	}
	return true, nil
}

// Down rolls back the last n migrations (n >= 1), or all of them when fewer are
// applied. It returns how many were rolled back.
func (x *Migrator) Down(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("migrate down: n must be at least 1")
	}
	before, err := x.Status()
	if err != nil {
		return 0, err
	}
	err = x.m.Steps(-n)
	var short migrate.ErrShortLimit
	if err != nil && !errors.Is(err, migrate.ErrNoChange) && !errors.As(err, &short) {
		return 0, fmt.Errorf("migrate down: %w", err)
	}
	after, err := x.Status()
	if err != nil {
		return 0, err
	}
	return applied(before) - applied(after), nil
}

// applied counts migrations by position; versions are sequential (NNNNNN_ files).
func applied(s Status) int {
	if s.None {
		return 0
	}
	return int(s.Version)
}

// Status returns the current schema version.
func (x *Migrator) Status() (Status, error) {
	v, dirty, err := x.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return Status{None: true}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("migrate version: %w", err)
	}
	return Status{Version: v, Dirty: dirty}, nil
}

// Close releases the source and the database connection.
func (x *Migrator) Close() error {
	srcErr, dbErr := x.m.Close()
	return errors.Join(srcErr, dbErr)
}
