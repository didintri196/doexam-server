package session

import "time"

// Status mirrors the CHECK constraint on exam_sessions.status.
//
//	scheduled → open ⇄ paused → closed
//	scheduled → cancelled
type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusOpen      Status = "open"
	StatusPaused    Status = "paused"
	StatusClosed    Status = "closed"
	StatusCancelled Status = "cancelled"
)

// Attendance mirrors the CHECK constraint on exam_session_participants.attendance.
type Attendance string

const (
	AttendanceExpected Attendance = "expected"
	AttendancePresent  Attendance = "present"
	AttendanceAbsent   Attendance = "absent"
	AttendanceExcused  Attendance = "excused"
)

// SessionEntity mirrors the exam_sessions table.
type SessionEntity struct {
	ID                   string `gorm:"type:uuid;primaryKey"`
	TenantID             string `gorm:"type:uuid;not null"`
	ExamID               string `gorm:"type:uuid;not null"`
	Name                 string `gorm:"not null"`
	Room                 *string
	StartsAt             time.Time `gorm:"not null"`
	EndsAt               time.Time `gorm:"not null"`
	LateEntryMinutes     *int      // nil = may enter any time inside the window
	TokenRequired        bool
	TokenSecret          []byte `gorm:"type:bytea"` // never leaves the server
	TokenRotationMinutes int    `gorm:"not null"`
	Status               Status `gorm:"not null"`
	IsMakeup             bool
	// RequireCamera/RequireScreen: participants must share their camera / whole screen
	// before starting; proctors may then watch them live (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md).
	RequireCamera bool
	RequireScreen bool
	CreatedBy     *string `gorm:"type:uuid"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (SessionEntity) TableName() string { return "exam_sessions" }

// ProctorEntity mirrors the exam_session_proctors table.
type ProctorEntity struct {
	SessionID string `gorm:"type:uuid;primaryKey"`
	UserID    string `gorm:"type:uuid;primaryKey"`
	TenantID  string `gorm:"type:uuid;not null"`
}

func (ProctorEntity) TableName() string { return "exam_session_proctors" }

// SessionGroupEntity mirrors the exam_session_groups table (where participants came from).
type SessionGroupEntity struct {
	SessionID string `gorm:"type:uuid;primaryKey"`
	GroupID   string `gorm:"type:uuid;primaryKey"`
	TenantID  string `gorm:"type:uuid;not null"`
}

func (SessionGroupEntity) TableName() string { return "exam_session_groups" }

// ParticipantEntity mirrors the exam_session_participants table.
type ParticipantEntity struct {
	SessionID        string     `gorm:"type:uuid;primaryKey"`
	UserID           string     `gorm:"type:uuid;primaryKey"`
	TenantID         string     `gorm:"type:uuid;not null"`
	ExtraTimeMinutes int        `gorm:"not null"`
	Attendance       Attendance `gorm:"not null"`
	Seat             *string
	Notes            *string
	AddedAt          time.Time
}

func (ParticipantEntity) TableName() string { return "exam_session_participants" }
