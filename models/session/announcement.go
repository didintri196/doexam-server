package session

import (
	"strings"
	"time"
)

// Announcement limits (DOCS/PLAN/12-PENGUMUMAN-PESERTA.md).
const (
	MaxAnnouncementLen        = 500
	MaxAnnouncementsPerMinute = 10 // per session, so participants are not flooded
	MaxAnnouncementSessions   = 100
)

// AnnouncementPriority is session_announcements.priority.
type AnnouncementPriority string

// Announcement priorities: important shows as a dialog the participant confirms.
const (
	PriorityInfo      AnnouncementPriority = "info"
	PriorityImportant AnnouncementPriority = "important"
)

// AnnouncementTarget is session_announcements.target.
type AnnouncementTarget string

// Announcement targets.
const (
	TargetAll      AnnouncementTarget = "all"
	TargetSelected AnnouncementTarget = "selected"
)

// AnnouncementEntity is a row of session_announcements.
type AnnouncementEntity struct {
	ID          string `gorm:"type:uuid;primaryKey"`
	TenantID    string `gorm:"type:uuid;not null"`
	SessionID   string `gorm:"type:uuid;not null"`
	SenderID    string `gorm:"type:uuid;not null"`
	Body        string
	Priority    AnnouncementPriority
	Target      AnnouncementTarget
	Recipients  int
	RetractedAt *time.Time
	CreatedAt   time.Time
}

// TableName maps AnnouncementEntity to session_announcements.
func (AnnouncementEntity) TableName() string { return "session_announcements" }

// AnnouncementInput is the body of POST /sessions/:id/announcements.
type AnnouncementInput struct {
	Body     string               `json:"body"`
	Priority AnnouncementPriority `json:"priority"`
	Target   AnnouncementTarget   `json:"target"`
	UserIDs  []string             `json:"user_ids"` // target "selected"
}

// Normalize trims the body and applies defaults.
func (in *AnnouncementInput) Normalize() {
	in.Body = strings.TrimSpace(in.Body)
	if in.Priority == "" {
		in.Priority = PriorityInfo
	}
	if in.Target == "" {
		in.Target = TargetAll
	}
	for i, id := range in.UserIDs {
		in.UserIDs[i] = strings.ToLower(strings.TrimSpace(id))
	}
}

// BroadcastInput is the body of POST /announcements: one announcement to every
// participant of several sessions (session.manage).
type BroadcastInput struct {
	SessionIDs []string             `json:"session_ids"`
	Running    bool                 `json:"running"` // every open or paused session instead of session_ids
	Body       string               `json:"body"`
	Priority   AnnouncementPriority `json:"priority"`
}

// SendResultDTO reports POST /sessions/:id/announcements.
type SendResultDTO struct {
	ID         string `json:"id"`
	Recipients int    `json:"recipients"`
	Skipped    int    `json:"skipped"` // requested user ids that are not participants of the session
}

// BroadcastResultDTO reports POST /announcements.
type BroadcastResultDTO struct {
	Sessions   int `json:"sessions"`
	Recipients int `json:"recipients"`
}

// AnnouncementDTO is an announcement as staff see it, with read counts.
type AnnouncementDTO struct {
	ID          string               `json:"id"`
	Body        string               `json:"body"`
	Priority    AnnouncementPriority `json:"priority"`
	Target      AnnouncementTarget   `json:"target"`
	Sender      PersonDTO            `json:"sender"`
	Recipients  int                  `json:"recipients"`
	Read        int                  `json:"read"`
	RetractedAt *time.Time           `json:"retracted_at"`
	CreatedAt   time.Time            `json:"created_at"`
}

// AnnouncementListDTO is GET /sessions/:id/announcements.
type AnnouncementListDTO struct {
	Announcements []AnnouncementDTO `json:"announcements"` // newest first
	// UnreadImportant lists participants who have not confirmed an important
	// announcement sent more than a minute ago, so a proctor can walk over.
	UnreadImportant []string `json:"unread_important"`
}

// RecipientDTO is one recipient of an announcement and whether they read it.
type RecipientDTO struct {
	UserID   string     `json:"user_id"`
	Username string     `json:"username"`
	FullName string     `json:"full_name"`
	Seat     *string    `json:"seat"`
	ReadAt   *time.Time `json:"read_at"`
}

// MyAnnouncementDTO is an announcement as its recipient sees it: no sender id, no
// other recipients. A retracted announcement keeps its id and loses its body.
type MyAnnouncementDTO struct {
	ID          string               `json:"id"`
	SessionID   string               `json:"session_id"`
	Body        string               `json:"body"`
	Priority    AnnouncementPriority `json:"priority"`
	From        string               `json:"from"` // "proctor" or "committee" (session.manage)
	ReadAt      *time.Time           `json:"read_at"`
	RetractedAt *time.Time           `json:"retracted_at"`
	CreatedAt   time.Time            `json:"created_at"`
}

// MyAnnouncementsDTO is GET /my/announcements.
type MyAnnouncementsDTO struct {
	Cursor        string              `json:"cursor"` // same value as SyncDTO.announcement_cursor
	Announcements []MyAnnouncementDTO `json:"announcements"`
}
