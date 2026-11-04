package attempt

import (
	"context"
	modelMedia "doexam/models/media"
	"time"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelExam "doexam/models/exam"
)

// SessionInfo is what starting an attempt needs from the session.
type SessionInfo struct {
	ID                   string
	ExamID               string
	Status               string
	StartsAt             time.Time
	EndsAt               time.Time
	LateEntryMinutes     *int
	TokenRequired        bool
	TokenSecret          []byte
	TokenRotationMinutes int
}

// ParticipantInfo is a participant row.
type ParticipantInfo struct {
	ExtraTimeMinutes int
	Attendance       string
	Seat             *string
}

// CandidateRow is a hand-picked item (SectionID set) or a pool entry (RuleID set) with its version.
type CandidateRow struct {
	SectionID     string
	RuleID        string
	ItemID        string
	VersionID     string
	Type          string
	Content       []byte
	DefaultPoints float64
	Points        *float64
	LockOptions   bool
	SortOrder     int
}

// ItemRow is an attempt item joined with its item version and stimulus.
type ItemRow struct {
	modelAttempt.AttemptItemEntity
	Type            string
	Content         []byte
	AnswerKey       []byte
	Scoring         []byte
	Explanation     []byte
	StimulusID      *string
	StimulusTitle   *string
	StimulusContent []byte
}

// MonitorAttempt is the latest attempt of each participant of a session, for the dashboard.
type MonitorAttempt struct {
	UserID     string
	Username   string
	FullName   string
	Seat       *string
	Attendance string
	AttemptID  *string
	Status     *modelAttempt.Status
	Answered   int
	ItemCount  int
	DeadlineAt *time.Time
	LastSeenAt *time.Time
	Violations int
	DeviceID   *string
	FinalScore *float64
}

// AttemptStorer is the persistence port for attempts. Every method must run inside TxManager.WithTenant.
type AttemptStorer interface {
	FindSession(ctx context.Context, id string) (SessionInfo, error)
	FindParticipant(ctx context.Context, sessionID, userID string) (ParticipantInfo, bool, error)
	MarkPresent(ctx context.Context, sessionID, userID string) error
	IsProctor(ctx context.Context, sessionID, userID string) (bool, error)
	FindExam(ctx context.Context, id string) (modelExam.ExamEntity, error)
	ListSections(ctx context.Context, examID string) ([]modelExam.SectionEntity, error)
	ListRules(ctx context.Context, examID string) ([]modelExam.RuleEntity, error)
	// Candidates returns hand-picked items (pinned versions) and rule pools of a published exam.
	Candidates(ctx context.Context, examID string) ([]CandidateRow, error)
	// Adjustments returns active regrade adjustments by item version id.
	Adjustments(ctx context.Context, examID string) (map[string]adjustment, error)

	ActiveAttempt(ctx context.Context, examID, userID string) (modelAttempt.AttemptEntity, bool, error)
	CountAttempts(ctx context.Context, examID, userID string) (int64, error) // excluding void
	FindAttempt(ctx context.Context, id string, forUpdate bool) (modelAttempt.AttemptEntity, error)
	CreateAttempt(ctx context.Context, a *modelAttempt.AttemptEntity, sections []modelAttempt.AttemptSectionEntity, items []modelAttempt.AttemptItemEntity) error
	UpdateAttempt(ctx context.Context, a *modelAttempt.AttemptEntity) error
	ListAttemptSections(ctx context.Context, attemptID string) ([]modelAttempt.AttemptSectionEntity, error)
	UpdateAttemptSection(ctx context.Context, s *modelAttempt.AttemptSectionEntity) error
	ListItems(ctx context.Context, attemptID string) ([]ItemRow, error)
	SaveResponse(ctx context.Context, itemID string, response []byte, seq int, at time.Time) error
	SetFlag(ctx context.Context, itemID string, flagged bool) error
	AddVisit(ctx context.Context, itemID string, spentMs int) error
	SaveScores(ctx context.Context, items []modelAttempt.AttemptItemEntity) error
	// LatestManualScores returns the newest manual grade per attempt item (only items that have one).
	LatestManualScores(ctx context.Context, itemIDs []string) (map[string]*float64, error)
	// MediaPlays counts MEDIA_PLAYED events per media id and sums MEDIA_PLAYS_GRANTED plays.
	MediaPlays(ctx context.Context, attemptID string) (map[string]int, int, error)
	// InsertEvent appends an event; false when its client_event_id was already stored.
	InsertEvent(ctx context.Context, e *modelAttempt.EventEntity) (bool, error)

	// grading & regrade
	GradingSummary(ctx context.Context, examID string) ([]modelAttempt.GradingSummaryRow, error)
	GradingQueue(ctx context.Context, examID string, f modelAttempt.GradingFilter) ([]modelAttempt.GradingQueueRow, int64, error)
	FindItem(ctx context.Context, attemptItemID string) (ItemRow, error)
	GradeHistory(ctx context.Context, attemptItemID string) ([]modelAttempt.GradeHistoryRow, error)
	InsertManualGrade(ctx context.Context, g *modelAttempt.ManualGradeEntity) error
	ListAdjustments(ctx context.Context, examID string) ([]modelAttempt.AdjustmentEntity, error)
	FindAdjustment(ctx context.Context, id string) (modelAttempt.AdjustmentEntity, error)
	CreateAdjustment(ctx context.Context, a *modelAttempt.AdjustmentEntity) error
	RevokeAdjustment(ctx context.Context, id string, at time.Time) error
	// VersionInExam returns the type and content of a version if the exam can deliver it.
	VersionInExam(ctx context.Context, examID, versionID string) (string, []byte, bool, error)
	// AttemptsWithVersion returns submitted/graded attempts of the exam containing the version (locked).
	AttemptsWithVersion(ctx context.Context, examID, versionID string) ([]modelAttempt.AttemptEntity, error)
	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error

	RunningAttempts(ctx context.Context, sessionID string) ([]modelAttempt.AttemptEntity, error)
	Monitor(ctx context.Context, sessionID string) ([]MonitorAttempt, error)
	MyAttempts(ctx context.Context, userID string) ([]modelAttempt.MyAttemptDTO, error)
}

// MediaLinker signs links for media referenced by item HTML (business/media). It runs
// inside the caller's tenant transaction.
type MediaLinker interface {
	Refs(ctx context.Context, tenantID string, ids []string) (map[string]modelMedia.Ref, error)
}

// Announcements is the session domain's announcement board (business/session). Both
// methods run inside the caller's tenant transaction.
type Announcements interface {
	Cursor(ctx context.Context, sessionID string) (string, error)
	MarkRead(ctx context.Context, announcementID, userID string, at time.Time) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
