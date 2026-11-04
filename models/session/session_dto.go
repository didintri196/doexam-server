package session

import (
	"time"

	"doexam/models/opt"
)

// MaxParticipantsPerRequest bounds POST /sessions/:id/participants (user ids + group members).
const MaxParticipantsPerRequest = 5000

// ListFilter pages GET /api/v1/sessions.
type ListFilter struct {
	Page    int
	PerPage int
	ExamID  string
	Status  Status
	From    *time.Time // sessions ending after From
	To      *time.Time // sessions starting before To
}

// SessionInput is the body of POST /api/v1/sessions and PATCH /api/v1/sessions/:id.
// room "" clears; late_entry_minutes null = no limit.
type SessionInput struct {
	ExamID               *string           `json:"exam_id"`
	Name                 *string           `json:"name"`
	Room                 *string           `json:"room"`
	StartsAt             *time.Time        `json:"starts_at"`
	EndsAt               *time.Time        `json:"ends_at"`
	LateEntryMinutes     opt.Nullable[int] `json:"late_entry_minutes"`
	TokenRequired        *bool             `json:"token_required"`
	TokenRotationMinutes *int              `json:"token_rotation_minutes"`
	IsMakeup             *bool             `json:"is_makeup"`
}

// StatusInput is the body of POST /api/v1/sessions/:id/status.
type StatusInput struct {
	Status Status `json:"status"`
}

// ProctorsInput is the body of PUT /api/v1/sessions/:id/proctors (replaces the list).
type ProctorsInput struct {
	UserIDs []string `json:"user_ids"`
}

// AddParticipantsInput is the body of POST /api/v1/sessions/:id/participants.
// Members (not leaders) of the groups are added; users already listed are kept as they are.
type AddParticipantsInput struct {
	UserIDs          []string `json:"user_ids"`
	GroupIDs         []string `json:"group_ids"`
	ExtraTimeMinutes int      `json:"extra_time_minutes"`
}

// ParticipantUpdateInput is the body of PATCH /api/v1/sessions/:id/participants/:user_id.
// extra_time_minutes needs session.manage; seat/notes "" clears.
type ParticipantUpdateInput struct {
	ExtraTimeMinutes *int        `json:"extra_time_minutes"`
	Attendance       *Attendance `json:"attendance"`
	Seat             *string     `json:"seat"`
	Notes            *string     `json:"notes"`
}

// ParticipantFilter pages GET /api/v1/sessions/:id/participants.
type ParticipantFilter struct {
	Page       int
	PerPage    int
	Query      string
	Attendance Attendance
	UserID     string // internal: fetch one participant
}

// ExamRefDTO is the exam a session runs.
type ExamRefDTO struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	TotalMinutes *int   `json:"total_minutes"`
}

// PersonDTO is a proctor.
type PersonDTO struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	FullName string `json:"full_name"`
}

// GroupRefDTO is a group assigned to a session.
type GroupRefDTO struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Period *string `json:"period"`
}

// SessionDTO is a session for organizers and proctors. It never carries the token secret.
type SessionDTO struct {
	ID                   string           `json:"id"`
	Exam                 ExamRefDTO       `json:"exam"`
	Name                 string           `json:"name"`
	Room                 *string          `json:"room"`
	StartsAt             time.Time        `json:"starts_at"`
	EndsAt               time.Time        `json:"ends_at"`
	LateEntryMinutes     *int             `json:"late_entry_minutes"`
	TokenRequired        bool             `json:"token_required"`
	TokenRotationMinutes int              `json:"token_rotation_minutes"`
	Status               Status           `json:"status"`
	IsMakeup             bool             `json:"is_makeup"`
	ParticipantCount     int64            `json:"participant_count"`
	Attendance           map[string]int64 `json:"attendance"`
	Proctors             []PersonDTO      `json:"proctors"`
	Groups               []GroupRefDTO    `json:"groups"`
	MyRole               string           `json:"my_role"` // "manager" | "proctor"
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

// SessionSummaryDTO is a list row.
type SessionSummaryDTO struct {
	ID               string    `json:"id"`
	ExamID           string    `json:"exam_id"`
	ExamTitle        string    `json:"exam_title"`
	Name             string    `json:"name"`
	Room             *string   `json:"room"`
	StartsAt         time.Time `json:"starts_at"`
	EndsAt           time.Time `json:"ends_at"`
	Status           Status    `json:"status"`
	IsMakeup         bool      `json:"is_makeup"`
	ParticipantCount int64     `json:"participant_count"`
}

// TokenDTO is the current room token, for organizers and the session's proctors.
type TokenDTO struct {
	Code            string    `json:"code"`
	ExpiresAt       time.Time `json:"expires_at"`
	RotationMinutes int       `json:"rotation_minutes"`
}

// ParticipantDTO is one participant of a session.
type ParticipantDTO struct {
	UserID           string     `json:"user_id"`
	Username         string     `json:"username"`
	FullName         string     `json:"full_name"`
	ExternalID       *string    `json:"external_id"`
	ExtraTimeMinutes int        `json:"extra_time_minutes"`
	Attendance       Attendance `json:"attendance"`
	Seat             *string    `json:"seat"`
	Notes            *string    `json:"notes"`
	AddedAt          time.Time  `json:"added_at"`
}

// PrintParticipantDTO is one participant on the printed documents.
type PrintParticipantDTO struct {
	UserID        string     `json:"user_id"`
	Username      string     `json:"username"`
	FullName      string     `json:"full_name"`
	ExternalID    *string    `json:"external_id"`
	Seat          *string    `json:"seat"`
	Attendance    Attendance `json:"attendance"`
	AttemptStatus *string    `json:"attempt_status"` // latest attempt in this session, if any
}

// PrintStatsDTO sums up a session for the minutes (berita acara).
type PrintStatsDTO struct {
	Registered int `json:"registered"`
	Present    int `json:"present"` // attendance present, or started an attempt
	Absent     int `json:"absent"`
	Excused    int `json:"excused"`
	Started    int `json:"started"`   // has an attempt
	Submitted  int `json:"submitted"` // attempt submitted or graded
}

// InstitutionDTO is what the printed documents show of the institution.
type InstitutionDTO struct {
	Name       string  `json:"name"`
	Letterhead string  `json:"letterhead"`
	ExamRules  string  `json:"exam_rules"`
	LogoURL    *string `json:"logo_url"`
}

// PrintDTO is GET /api/v1/sessions/:id/print: everything the session documents need.
type PrintDTO struct {
	Session      SessionDTO            `json:"session"`
	Institution  InstitutionDTO        `json:"institution"`
	Participants []PrintParticipantDTO `json:"participants"` // by seat, then name
	Stats        PrintStatsDTO         `json:"stats"`
	GeneratedAt  time.Time             `json:"generated_at"`
}

// AddResultDTO reports POST /sessions/:id/participants.
type AddResultDTO struct {
	Added   int `json:"added"`   // new participants
	Skipped int `json:"skipped"` // already in the session
}

// MySessionDTO is a session as the participant sees it (no token, no other participants).
type MySessionDTO struct {
	SessionID        string    `json:"session_id"`
	ExamID           string    `json:"exam_id"`
	ExamTitle        string    `json:"exam_title"`
	Name             string    `json:"name"`
	Room             *string   `json:"room"`
	StartsAt         time.Time `json:"starts_at"`
	EndsAt           time.Time `json:"ends_at"`
	Status           Status    `json:"status"`
	TokenRequired    bool      `json:"token_required"`
	TotalMinutes     *int      `json:"total_minutes"`
	ExtraTimeMinutes int       `json:"extra_time_minutes"`
	Seat             *string   `json:"seat"`
}
