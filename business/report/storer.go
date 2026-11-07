package report

import (
	"context"
	"encoding/json"
	"time"

	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
)

// RecapAttempt is one participant × (optional) attempt row of an exam.
type RecapAttempt struct {
	UserID      string
	Username    string
	FullName    string
	ExternalID  *string
	SessionName string
	SessionAt   time.Time
	AttemptID   *string
	Status      *string
	FinalScore  *float64
	SubmittedAt *time.Time
}

// AnalysisAttempt is a graded attempt used for item analysis.
type AnalysisAttempt struct {
	ID         string
	UserID     string
	RawScore   float64
	MaxScore   float64
	FinalScore float64
	Passed     *bool
}

// AnalysisItem is one answered item of an analysed attempt, with its version.
type AnalysisItem struct {
	AttemptID     string
	ItemVersionID string
	ItemID        string
	Code          *string
	Type          string
	SearchText    *string
	Content       []byte
	AnswerKey     []byte
	Response      []byte
	Score         *float64
	MaxPoints     float64
}

// StoredStats is what was persisted for an analysis scope.
type StoredStats struct {
	ComputedAt time.Time
	Exam       modelReport.ExamStat
	Items      []modelReport.ItemStat
}

// ReportItem is an item row of an individual report.
type ReportItem struct {
	Position         int
	AttemptSectionID string
	ItemID           string
	Code             *string
	Type             string
	SearchText       *string
	Response         []byte
	AnswerKey        []byte
	Score            *float64
	MaxPoints        float64
	IsCorrect        *bool
	ScoreSource      *string
	TimeSpentMs      int
	VisitCount       int
	Flagged          bool
	CompetencyID     *string
	CompetencyCode   *string
	CompetencyName   *string
}

// ReportAttempt is the attempt part of an individual report.
type ReportAttempt struct {
	ID             string
	ExamID         string
	UserID         string
	Username       string
	FullName       string
	ExternalID     *string
	SessionName    string
	AttemptNo      int
	Status         string
	SubmitReason   *string
	StartedAt      time.Time
	SubmittedAt    *time.Time
	RawScore       *float64
	MaxScore       *float64
	FinalScore     *float64
	Passed         *bool
	PendingManual  int `gorm:"column:pending_manual_count"`
	ConfigSnapshot json.RawMessage
}

// ReportSection is a section row of an individual report.
type ReportSection struct {
	ID        string
	SectionID string
	RawScore  *float64
	MaxScore  *float64
	Passed    *bool
}

// ReportStorer is the persistence port for reports. Every method must run inside TxManager.WithTenant.
type ReportStorer interface {
	FindExam(ctx context.Context, id string) (modelExam.ExamEntity, error)
	SessionBelongs(ctx context.Context, sessionID, examID string) (bool, error)
	// RecapRows returns participant × attempt rows; with f.PerPage > 0 only those of one
	// page of participants (paged in SQL, ordered by name).
	RecapRows(ctx context.Context, examID string, f modelReport.RecapFilter) ([]RecapAttempt, error)
	// RecapCounts counts every matching participant per recap status, ignoring the page.
	// Its status rules must match buildRecap (checked by TestIntegrationRecapCountsMatchRows).
	RecapCounts(ctx context.Context, examID string, f modelReport.RecapFilter) (map[string]int, error)
	// AnalysisData returns each participant's first graded attempt and its items.
	AnalysisData(ctx context.Context, examID, sessionID string) ([]AnalysisAttempt, []AnalysisItem, error)
	SaveStats(ctx context.Context, tenantID, examID, sessionID string, st StoredStats) error
	LoadStats(ctx context.Context, examID, sessionID string) (StoredStats, bool, error)

	FindReportAttempt(ctx context.Context, attemptID string) (ReportAttempt, error)
	ReportSections(ctx context.Context, attemptID string) ([]ReportSection, error)
	ReportItems(ctx context.Context, attemptID string) ([]ReportItem, error)
	EventCounts(ctx context.Context, attemptID string, types []string) (map[string]int, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
