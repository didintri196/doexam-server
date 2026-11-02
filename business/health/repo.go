package health

import (
	"context"

	"gorm.io/gorm"
)

// Compile-time check: DBPingRepo must satisfy Pinger.
var _ Pinger = (*DBPingRepo)(nil)

// DBPingRepo is the GORM implementation of Pinger for PostgreSQL.
type DBPingRepo struct {
	db *gorm.DB
}

// NewDBPingRepo returns a concrete DBPingRepo (not interface).
func NewDBPingRepo(db *gorm.DB) *DBPingRepo {
	return &DBPingRepo{db: db}
}

func (r *DBPingRepo) Ping(ctx context.Context) error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
