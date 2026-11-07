package liveview

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelLive "doexam/models/liveview"
)

// SessionRow is what live views need from a session.
type SessionRow struct {
	ID            string
	Status        string
	RequireCamera bool
	RequireScreen bool
}

// AttemptRow is what live views need from an attempt.
type AttemptRow struct {
	ID        string
	SessionID string
	UserID    string
	Status    string
}

// LiveViewStorer is the persistence port. Every method must run inside TxManager.WithTenant.
type LiveViewStorer interface {
	FindSession(ctx context.Context, id string) (SessionRow, error)
	IsProctor(ctx context.Context, sessionID, userID string) (bool, error)
	FindAttempt(ctx context.Context, id string) (AttemptRow, error)

	// ActiveViews lists the views of an attempt that still run at now, and the number of
	// views a proctor has running.
	ActiveViews(ctx context.Context, attemptID string, now time.Time) ([]modelLive.ViewEntity, error)
	CountProctorViews(ctx context.Context, proctorID string, now time.Time) (int64, error)
	CreateView(ctx context.Context, v *modelLive.ViewEntity) error
	FindView(ctx context.Context, id string) (modelLive.ViewEntity, error)
	// EndView sets ended_at/end_reason unless already ended; it reports whether it did.
	EndView(ctx context.Context, id string, reason modelLive.EndReason, now time.Time) (bool, error)

	AddSignal(ctx context.Context, s *modelLive.SignalEntity) error
	// Notify wakes the long-polls waiting on key, on every replica, when the transaction commits.
	Notify(ctx context.Context, key string) error
	// ProctorSignals: messages to the proctor of a view after the cursor.
	ProctorSignals(ctx context.Context, viewID string, after int64, limit int) ([]modelLive.SignalEntity, error)
	// ParticipantSignals: messages to the participant of an attempt after the cursor, from
	// views still running at now or from messages newer than since (so "end" is delivered).
	ParticipantSignals(ctx context.Context, attemptID string, after int64, now, since time.Time, limit int) ([]modelLive.SignalEntity, error)
	// PurgeSignals drops the signaling of an attempt's views older than before.
	PurgeSignals(ctx context.Context, attemptID string, before time.Time) error

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a tenant transaction.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// Waker lets a long-poll wait for Notify(key) from any replica (pkg/pglisten).
type Waker interface {
	Subscribe(key string) (<-chan struct{}, func())
}
