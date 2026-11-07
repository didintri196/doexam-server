package dbpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"
	"gorm.io/gorm"
)

type txCtxKey struct{}

// ErrNestedTx is returned when WithTenant is called inside another tenant transaction.
var ErrNestedTx = errors.New("nested tenant transaction is not supported")

// TxManager runs work inside a transaction scoped to one tenant.
// Inside the transaction `app.tenant_id` is set (SET LOCAL semantics), so
// PostgreSQL RLS only exposes rows of that tenant.
type TxManager struct {
	db       *gorm.DB
	duration metric.Float64Histogram
}

// NewTxManager returns a concrete TxManager.
func NewTxManager(db *gorm.DB) *TxManager {
	return &TxManager{db: db, duration: newTxHistogram()}
}

// WithTenant opens a transaction, sets app.tenant_id for its duration and runs fn.
// Repositories called with the ctx passed to fn automatically use that transaction (see Conn).
// fn returning an error rolls the transaction back.
func (m *TxManager) WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txCtxKey{}).(*gorm.DB); ok {
		return ErrNestedTx
	}
	start := time.Now()
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// set_config(..., true) == SET LOCAL, but accepts a bind parameter.
		if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(context.WithValue(ctx, txCtxKey{}, tx))
	})
	if m.duration != nil {
		recordTx(ctx, m.duration, start, err)
	}
	return err
}

// Conn returns the tenant transaction carried by ctx, or db when there is none.
// Every repository method must obtain its connection through Conn.
func Conn(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txCtxKey{}).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}
