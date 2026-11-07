package dbpg

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorKind(t *testing.T) {
	pg := func(code string) error { return fmt.Errorf("query: %w", &pgconn.PgError{Code: code}) }
	tests := []struct {
		err  error
		want string
	}{
		{pg("40P01"), "deadlock"},
		{pg("40001"), "serialization"},
		{pg("55P03"), "lock_timeout"},
		{pg("57014"), "statement_timeout"},
		{pg("23505"), "unique_violation"},
		{pg("23503"), "foreign_key_violation"},
		{pg("23514"), "constraint_violation"},
		{pg("08006"), "connection"},
		{pg("53300"), "connection"},
		{pg("42501"), "permission"},
		{pg("42P01"), "other"},
		{fmt.Errorf("x: %w", context.Canceled), "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("boom"), "other"},
	}
	for _, tt := range tests {
		if got := ErrorKind(tt.err); got != tt.want {
			t.Errorf("ErrorKind(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestTableName(t *testing.T) {
	tests := []struct{ table, op, want string }{
		{"attempts", "select", "attempts"},
		{"attempts a", "select", "attempts"},
		{`"attempt_events"`, "insert", "attempt_events"},
		{"", "raw", "raw"},
		{"", "row", "raw"},
		{"", "update", "unknown"},
	}
	for _, tt := range tests {
		if got := tableName(tt.table, tt.op); got != tt.want {
			t.Errorf("tableName(%q, %q) = %q, want %q", tt.table, tt.op, got, tt.want)
		}
	}
}

func TestTxNameWithoutSpan(t *testing.T) {
	if got := txName(context.Background()); got != "unknown" {
		t.Fatalf("txName = %q, want unknown", got)
	}
}
