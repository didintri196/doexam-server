package report

import (
	"encoding/json"
	"time"
)

// RecapFilter narrows GET /api/v1/exams/:id/recap. PerPage 0 means every participant
// (the Excel export); otherwise one page of participants ordered by name.
type RecapFilter struct {
	SessionID string
	GroupID   string
	Page      int
	PerPage   int
}

// Recap statuses.
const (
	RecapNotStarted     = "not_started"
	RecapInProgress     = "in_progress"
	RecapPendingGrading = "pending_grading"
	RecapGraded         = "graded"
)

// RecapRow is one participant in the score recap.
type RecapRow struct {
	UserID      string     `json:"user_id"`
	Username    string     `json:"username"`
	FullName    string     `json:"full_name"`
	ExternalID  *string    `json:"external_id"`
	SessionName string     `json:"session_name"`
	Attempts    int        `json:"attempts"`
	Status      string     `json:"status"`
	Score       *float64   `json:"score"` // per the exam's attempt_scoring (highest/latest/average)
	Passed      *bool      `json:"passed"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

// RecapDTO is the score recap of an exam.
type RecapDTO struct {
	ExamID         string         `json:"exam_id"`
	ExamTitle      string         `json:"exam_title"`
	AttemptScoring string         `json:"attempt_scoring"`
	PassingScore   *float64       `json:"passing_score"`
	Counts         map[string]int `json:"counts"`
	Rows           []RecapRow     `json:"rows"`
}

// OptionStat is how often one option was chosen and by whom.
type OptionStat struct {
	N     int      `json:"n"`
	Pct   float64  `json:"pct"`
	Pbis  *float64 `json:"pbis"` // correlation of choosing it with the rest score
	IsKey bool     `json:"is_key"`
	Flag  string   `json:"flag,omitempty"` // non_functional | attracts_high_scorers
}

// ItemStat is the classical analysis of one item version.
type ItemStat struct {
	ItemVersionID     string                `json:"item_version_id"`
	ItemID            string                `json:"item_id"`
	Code              *string               `json:"code"`
	Type              string                `json:"type"`
	StemPreview       string                `json:"stem_preview"`
	N                 int                   `json:"n"`
	Omitted           int                   `json:"omitted"`
	PValue            *float64              `json:"p_value"`        // difficulty: mean proportion of points
	Discrimination    *float64              `json:"discrimination"` // corrected item-rest correlation (point-biserial)
	DifficultyLevel   string                `json:"difficulty_level,omitempty"`
	DiscriminationLvl string                `json:"discrimination_level,omitempty"`
	Recommendation    string                `json:"recommendation,omitempty"` // keep | revise | check_key
	Options           map[string]OptionStat `json:"options,omitempty"`
}

// ExamStat is the analysis of the whole test.
type ExamStat struct {
	NAttempts    int      `json:"n_attempts"`
	Mean         *float64 `json:"mean"`
	SD           *float64 `json:"sd"`
	Min          *float64 `json:"min"`
	Max          *float64 `json:"max"`
	Median       *float64 `json:"median"`
	Reliability  *float64 `json:"reliability"` // Cronbach alpha / KR-20 over items everyone received
	CommonItems  int      `json:"common_items"`
	PassRate     *float64 `json:"pass_rate"`
	Distribution []int    `json:"distribution"` // 10 bins: 0-10, 10-20, ..., 90-100
}

// AnalysisDTO is GET /api/v1/exams/:id/analysis.
type AnalysisDTO struct {
	ExamID     string     `json:"exam_id"`
	SessionID  *string    `json:"session_id"`
	ComputedAt time.Time  `json:"computed_at"`
	Warnings   []string   `json:"warnings"`
	Exam       ExamStat   `json:"exam"`
	Items      []ItemStat `json:"items"`
}

// SectionReport is one section in an individual report.
type SectionReport struct {
	Title    string   `json:"title"`
	RawScore *float64 `json:"raw_score"`
	MaxScore *float64 `json:"max_score"`
	Passed   *bool    `json:"passed"`
}

// CompetencyReport is mastery of one competency in an individual report.
type CompetencyReport struct {
	CompetencyID string  `json:"competency_id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Score        float64 `json:"score"`
	MaxScore     float64 `json:"max_score"`
	Percent      float64 `json:"percent"`
	Items        int     `json:"items"`
}

// ItemReport is one item in an individual report (staff view: includes the key).
type ItemReport struct {
	Position    int             `json:"position"`
	ItemID      string          `json:"item_id"`
	Code        *string         `json:"code"`
	Type        string          `json:"type"`
	StemPreview string          `json:"stem_preview"`
	Response    json.RawMessage `json:"response"`
	AnswerKey   json.RawMessage `json:"answer_key"`
	Score       *float64        `json:"score"`
	MaxPoints   float64         `json:"max_points"`
	IsCorrect   *bool           `json:"is_correct"`
	ScoreSource *string         `json:"score_source"`
	TimeSpentMs int             `json:"time_spent_ms"`
	Visits      int             `json:"visits"`
	Flagged     bool            `json:"flagged"`
}

// AttemptReportDTO is GET /api/v1/attempts/:id/report.
type AttemptReportDTO struct {
	AttemptID     string             `json:"attempt_id"`
	ExamID        string             `json:"exam_id"`
	ExamTitle     string             `json:"exam_title"`
	SessionName   string             `json:"session_name"`
	UserID        string             `json:"user_id"`
	Username      string             `json:"username"`
	FullName      string             `json:"full_name"`
	ExternalID    *string            `json:"external_id"`
	AttemptNo     int                `json:"attempt_no"`
	Status        string             `json:"status"`
	SubmitReason  *string            `json:"submit_reason"`
	StartedAt     time.Time          `json:"started_at"`
	SubmittedAt   *time.Time         `json:"submitted_at"`
	DurationSec   *int64             `json:"duration_seconds"`
	RawScore      *float64           `json:"raw_score"`
	MaxScore      *float64           `json:"max_score"`
	FinalScore    *float64           `json:"final_score"`
	Passed        *bool              `json:"passed"`
	PendingManual int                `json:"pending_manual"`
	Violations    map[string]int     `json:"violations"`
	Sections      []SectionReport    `json:"sections"`
	Competencies  []CompetencyReport `json:"competencies"`
	Items         []ItemReport       `json:"items"`
}
