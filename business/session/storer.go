package session

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelSession "doexam/models/session"
	modelTenant "doexam/models/tenant"
)

// SessionStorer is the persistence port for exam sessions. Every method must run inside TxManager.WithTenant.
type SessionStorer interface {
	// ListSessions lists all sessions (all=true) or only those proctored by userID.
	ListSessions(ctx context.Context, f modelSession.ListFilter, userID string, all bool) ([]modelSession.SessionSummaryDTO, int64, error)
	FindSession(ctx context.Context, id string, forUpdate bool) (modelSession.SessionEntity, error)
	CreateSession(ctx context.Context, s *modelSession.SessionEntity) error
	UpdateSession(ctx context.Context, s *modelSession.SessionEntity) error
	DeleteSession(ctx context.Context, id string) error
	// ExamRef returns the exam with its total duration ("" ID = not found).
	ExamRef(ctx context.Context, examID string) (modelSession.ExamRefDTO, error)
	CountAttempts(ctx context.Context, sessionID, userID string) (int64, error) // userID "" = all users

	IsProctor(ctx context.Context, sessionID, userID string) (bool, error)
	ListProctors(ctx context.Context, sessionID string) ([]modelSession.PersonDTO, error)
	ReplaceProctors(ctx context.Context, tenantID, sessionID string, userIDs []string) error
	// UsersWithPermission returns the active users among ids that hold perm through a role.
	UsersWithPermission(ctx context.Context, ids []string, perm string) ([]string, error)

	ListSessionGroups(ctx context.Context, sessionID string) ([]modelSession.GroupRefDTO, error)
	AttendanceCounts(ctx context.Context, sessionID string) (map[string]int64, error)
	ActiveUserIDs(ctx context.Context, ids []string) ([]string, error)
	ExistingGroupIDs(ctx context.Context, ids []string) ([]string, error)
	// GroupMemberIDs returns active users who are plain members (not leaders) of the groups.
	GroupMemberIDs(ctx context.Context, groupIDs []string) ([]string, error)
	// AddParticipants inserts rows, skipping users already in the session; it returns how many were new.
	AddParticipants(ctx context.Context, rows []modelSession.ParticipantEntity) (int64, error)
	AddSessionGroups(ctx context.Context, rows []modelSession.SessionGroupEntity) error
	ListParticipants(ctx context.Context, sessionID string, f modelSession.ParticipantFilter) ([]modelSession.ParticipantDTO, int64, error)
	// PrintParticipants returns every participant with the status of their latest attempt.
	PrintParticipants(ctx context.Context, sessionID string) ([]modelSession.PrintParticipantDTO, error)
	// CardCandidates returns the participants' user ids with whether they ever signed in
	// and whether the account is active.
	CardCandidates(ctx context.Context, sessionID string) ([]CardCandidate, error)
	// SetCardPasswords stores password hashes (user id → hash) with must_change_password = false.
	SetCardPasswords(ctx context.Context, hashes map[string]string) error
	// FindTenant reads the tenant row (name and settings for printed documents).
	FindTenant(ctx context.Context, id string) (modelTenant.TenantEntity, error)
	FindParticipant(ctx context.Context, sessionID, userID string) (modelSession.ParticipantEntity, error)
	UpdateParticipant(ctx context.Context, p *modelSession.ParticipantEntity) error
	DeleteParticipant(ctx context.Context, sessionID, userID string) error

	// MySessions lists sessions where userID participates that end after since.
	MySessions(ctx context.Context, userID string, since time.Time) ([]modelSession.MySessionDTO, error)

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error

	// Announcements (DOCS/PLAN/12-PENGUMUMAN-PESERTA.md).
	BoardStorer
	CountRecentAnnouncements(ctx context.Context, sessionID string, since time.Time) (int64, error)
	CreateAnnouncement(ctx context.Context, a *modelSession.AnnouncementEntity) error
	// AddAnnouncementRecipients adds the session's participants (all, or only those in
	// userIDs when it is not nil) and returns how many were added.
	AddAnnouncementRecipients(ctx context.Context, tenantID, announcementID, sessionID string, userIDs []string) (int64, error)
	SetAnnouncementRecipients(ctx context.Context, announcementID string, n int) error
	ListAnnouncements(ctx context.Context, sessionID string) ([]modelSession.AnnouncementDTO, error)
	// UnreadImportant returns participants with an important, not retracted announcement
	// sent before `before` that they have not confirmed.
	UnreadImportant(ctx context.Context, sessionID string, before time.Time) ([]string, error)
	ListRecipients(ctx context.Context, announcementID string) ([]modelSession.RecipientDTO, error)
	FindAnnouncement(ctx context.Context, id string) (modelSession.AnnouncementEntity, error)
	RetractAnnouncement(ctx context.Context, id string, at time.Time) error
	MyAnnouncements(ctx context.Context, sessionID, userID string) ([]modelSession.MyAnnouncementDTO, error)
	IsAnnouncementRecipient(ctx context.Context, announcementID, userID string) (bool, error)
	// RunningSessionIDs lists open and paused sessions.
	RunningSessionIDs(ctx context.Context) ([]string, error)
}

// CardCandidate is a participant who may get a card password.
type CardCandidate struct {
	UserID   string
	SignedIn bool
	Active   bool
}

// PasswordHasher hashes passwords (pkg/password; it bounds parallel hashing itself).
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// LinkSigner makes signed, expiring media links (pkg/mediaurl), used for the card signature image.
type LinkSigner interface {
	Path(prefix, tenantID, fileID string, now time.Time) string
}

// AttemptFinalizer submits the running attempts of a session. It runs inside the caller's
// tenant transaction (implemented by the attempt domain).
type AttemptFinalizer interface {
	FinalizeSessionAttempts(ctx context.Context, sessionID, actorID string) (int, error)
}

// ExamWarmer preloads what an exam delivers (item content) before participants start,
// in the background (implemented by the attempt domain). Call it after the transaction.
type ExamWarmer interface {
	WarmExam(ctx context.Context, tenantID, examID string)
}

// Attempts is what the session domain needs from the attempt domain.
type Attempts interface {
	AttemptFinalizer
	ExamWarmer
}
