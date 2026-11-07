package attempt

import (
	"time"

	"doexam/models/pgtype"
)

// Status mirrors the CHECK constraint on attempts.status.
type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusLocked     Status = "locked" // by a proctor: viewable, no answers accepted
	StatusSubmitted  Status = "submitted"
	StatusGraded     Status = "graded"
	StatusVoid       Status = "void"
)

// SubmitReason mirrors the CHECK constraint on attempts.submit_reason.
type SubmitReason string

const (
	ReasonParticipant   SubmitReason = "participant"
	ReasonTimeout       SubmitReason = "timeout"
	ReasonProctor       SubmitReason = "proctor"
	ReasonSessionClosed SubmitReason = "session_closed"
)

// Score sources (attempt_items.score_source).
const (
	SourceAuto       = "auto"
	SourceManual     = "manual"
	SourceAdjustment = "adjustment"
)

// AttemptEntity mirrors the attempts table.
type AttemptEntity struct {
	ID                 string `gorm:"type:uuid;primaryKey"`
	TenantID           string `gorm:"type:uuid;not null"`
	SessionID          string `gorm:"type:uuid;not null"`
	ExamID             string `gorm:"type:uuid;not null"`
	UserID             string `gorm:"type:uuid;not null"`
	AttemptNo          int    `gorm:"not null"`
	Status             Status `gorm:"not null"`
	ConfigSnapshot     []byte `gorm:"type:jsonb;not null"`
	Seed               int64  `gorm:"not null"`
	StartedAt          time.Time
	DeadlineAt         time.Time `gorm:"not null"` // authoritative
	SubmittedAt        *time.Time
	SubmitReason       *SubmitReason
	ActiveDeviceID     *string
	LastSeenAt         *time.Time
	LastEventSeq       int
	RawScore           *float64 `gorm:"type:numeric(10,2)"`
	MaxScore           *float64 `gorm:"type:numeric(10,2)"`
	FinalScore         *float64 `gorm:"type:numeric(6,2)"`
	Passed             *bool
	PendingManualCount int
	GradedAt           *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (AttemptEntity) TableName() string { return "attempts" }

// AttemptSectionEntity mirrors the attempt_sections table.
type AttemptSectionEntity struct {
	ID         string `gorm:"type:uuid;primaryKey"`
	TenantID   string `gorm:"type:uuid;not null"`
	AttemptID  string `gorm:"type:uuid;not null"`
	SectionID  string `gorm:"type:uuid;not null"`
	SortOrder  int    `gorm:"not null"`
	StartedAt  *time.Time
	DeadlineAt *time.Time // nil = follows the attempt deadline
	ClosedAt   *time.Time
	RawScore   *float64 `gorm:"type:numeric(10,2)"`
	MaxScore   *float64 `gorm:"type:numeric(10,2)"`
	Passed     *bool
}

func (AttemptSectionEntity) TableName() string { return "attempt_sections" }

// AttemptItemEntity mirrors the attempt_items table.
type AttemptItemEntity struct {
	ID               string           `gorm:"type:uuid;primaryKey"`
	TenantID         string           `gorm:"type:uuid;not null"`
	AttemptID        string           `gorm:"type:uuid;not null"`
	AttemptSectionID string           `gorm:"type:uuid;not null"`
	Position         int              `gorm:"not null"`
	ItemVersionID    string           `gorm:"type:uuid;not null"`
	OptionOrder      pgtype.TextArray `gorm:"type:text[]"`
	MaxPoints        float64          `gorm:"type:numeric(8,2);not null"`
	Response         []byte           `gorm:"type:jsonb"`
	ResponseSeq      int
	AnsweredAt       *time.Time
	Flagged          bool
	TimeSpentMs      int
	VisitCount       int
	Score            *float64 `gorm:"type:numeric(8,2)"`
	IsCorrect        *bool
	ScoreSource      *string
	ScoredAt         *time.Time
}

func (AttemptItemEntity) TableName() string { return "attempt_items" }

// EventEntity mirrors the append-only attempt_events table.
type EventEntity struct {
	ID            int64   `gorm:"primaryKey;->"`
	TenantID      string  `gorm:"type:uuid;not null"`
	AttemptID     string  `gorm:"type:uuid;not null"`
	ClientEventID *string `gorm:"type:uuid"`
	ClientSeq     *int
	Type          string     `gorm:"not null"`
	AttemptItemID *string    `gorm:"type:uuid"`
	Payload       []byte     `gorm:"type:jsonb;not null"`
	ActorID       *string    `gorm:"type:uuid"`
	ClientTS      *time.Time `gorm:"column:client_ts"`
	ServerTS      time.Time  `gorm:"column:server_ts"`
	IP            *string    `gorm:"type:inet"`
}

func (EventEntity) TableName() string { return "attempt_events" }

// Event types. Client-sent types are listed in ClientEventTypes; the rest are server-only.
const (
	EventStarted        = "ATTEMPT_STARTED"
	EventResumed        = "ATTEMPT_RESUMED"
	EventItemViewed     = "ITEM_VIEWED"
	EventResponseSaved  = "RESPONSE_SAVED"
	EventFlagSet        = "FLAG_SET"
	EventFocusLost      = "FOCUS_LOST"
	EventFocusReturned  = "FOCUS_RETURNED"
	EventFullscreenExit = "FULLSCREEN_EXIT"
	EventDeviceChanged  = "DEVICE_CHANGED"
	EventDeviceReset    = "DEVICE_RESET"
	EventTimeExtended   = "TIME_EXTENDED"
	EventLocked         = "LOCKED"
	EventUnlocked       = "UNLOCKED"
	EventSectionStarted = "SECTION_STARTED"
	EventSectionClosed  = "SECTION_CLOSED"
	EventSubmitted      = "SUBMITTED"
	// EventMediaPlayed: the participant started an audio clip (payload {media_id, play_no}).
	EventMediaPlayed = "MEDIA_PLAYED"
	// EventAnnouncementRead {announcement_id}: the participant confirmed a session announcement.
	EventAnnouncementRead = "ANNOUNCEMENT_READ"
	// EventMediaPlaysGranted: a proctor allowed extra plays of every limited clip (payload {plays}).
	EventMediaPlaysGranted = "MEDIA_PLAYS_GRANTED"

	// Camera, screen sharing and extra displays (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md §4):
	// the *_STOPPED / DETECTED events are violations; the monitor shows the latest state.
	EventCameraStopped        = "CAMERA_STOPPED"
	EventCameraResumed        = "CAMERA_RESUMED"
	EventScreenShareStopped   = "SCREEN_SHARE_STOPPED"
	EventScreenShareResumed   = "SCREEN_SHARE_RESUMED"
	EventExtraDisplayDetected = "EXTRA_DISPLAY_DETECTED"
	EventExtraDisplayRemoved  = "EXTRA_DISPLAY_REMOVED"
)

// ClientEventTypes may be sent by the exam client.
var ClientEventTypes = []string{EventItemViewed, EventResponseSaved, EventFlagSet, EventFocusLost, EventFocusReturned, EventFullscreenExit, EventMediaPlayed, EventAnnouncementRead,
	EventCameraStopped, EventCameraResumed, EventScreenShareStopped, EventScreenShareResumed, EventExtraDisplayDetected, EventExtraDisplayRemoved}

// ViolationEventTypes are counted on the proctor dashboard.
var ViolationEventTypes = []string{EventFocusLost, EventFullscreenExit, EventDeviceChanged, EventCameraStopped, EventScreenShareStopped, EventExtraDisplayDetected}
