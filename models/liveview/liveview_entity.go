// Package liveview holds the live camera/screen views a proctor opens on a participant
// (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md). Media flows peer-to-peer; only signaling is stored.
package liveview

import (
	"encoding/json"
	"time"
)

// Recipient mirrors the CHECK constraint on live_view_signals.recipient.
type Recipient string

const (
	RecipientParticipant Recipient = "participant"
	RecipientProctor     Recipient = "proctor"
)

// Kind mirrors the CHECK constraint on live_view_signals.kind.
type Kind string

const (
	KindViewRequest Kind = "view_request" // server → participant: start sending to this view
	KindOffer       Kind = "offer"        // participant → proctor (SDP)
	KindAnswer      Kind = "answer"       // proctor → participant (SDP)
	KindICE         Kind = "ice"          // both ways (ICE candidate)
	KindEnd         Kind = "end"          // the view is over
)

// EndReason mirrors the CHECK constraint on live_views.end_reason.
type EndReason string

const (
	EndProctor     EndReason = "proctor"
	EndParticipant EndReason = "participant"
	EndExpired     EndReason = "expired"
	EndReplaced    EndReason = "replaced" // the same proctor opened a new view of the attempt
)

// ViewEntity mirrors the live_views table.
type ViewEntity struct {
	ID        string    `gorm:"type:uuid;primaryKey"`
	TenantID  string    `gorm:"type:uuid;not null"`
	SessionID string    `gorm:"type:uuid;not null"`
	AttemptID string    `gorm:"type:uuid;not null"`
	ProctorID string    `gorm:"type:uuid;not null"`
	StartedAt time.Time `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	EndedAt   *time.Time
	EndReason *EndReason
}

func (ViewEntity) TableName() string { return "live_views" }

// Active reports whether the view still runs at now.
func (v ViewEntity) Active(now time.Time) bool { return v.EndedAt == nil && now.Before(v.ExpiresAt) }

// SignalEntity mirrors the live_view_signals table.
type SignalEntity struct {
	ID        int64           `gorm:"primaryKey;autoIncrement"`
	TenantID  string          `gorm:"type:uuid;not null"`
	ViewID    string          `gorm:"type:uuid;not null"`
	Recipient Recipient       `gorm:"not null"`
	Kind      Kind            `gorm:"not null"`
	Payload   json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedAt time.Time       `gorm:"not null"`
}

func (SignalEntity) TableName() string { return "live_view_signals" }
