package attempt

import (
	"encoding/json"
	"time"
)

// ManualGradeEntity mirrors the manual_grades table (history; the latest is the score).
type ManualGradeEntity struct {
	ID            string  `gorm:"type:uuid;primaryKey"`
	TenantID      string  `gorm:"type:uuid;not null"`
	AttemptItemID string  `gorm:"type:uuid;not null"`
	GraderID      string  `gorm:"type:uuid;not null"`
	Score         float64 `gorm:"type:numeric(8,2);not null"`
	RubricScores  []byte  `gorm:"type:jsonb"`
	Comment       *string
	CreatedAt     time.Time
}

func (ManualGradeEntity) TableName() string { return "manual_grades" }

// Adjustment actions (exam_item_adjustments.action).
const (
	AdjustAnnulFullCredit = "annul_full_credit" // everyone gets full points
	AdjustAnnulRemove     = "annul_remove"      // the item no longer counts
	AdjustOverrideKey     = "override_key"      // score with a corrected key
)

// AdjustmentEntity mirrors the exam_item_adjustments table.
type AdjustmentEntity struct {
	ID            string  `gorm:"type:uuid;primaryKey"`
	TenantID      string  `gorm:"type:uuid;not null"`
	ExamID        string  `gorm:"type:uuid;not null"`
	ItemVersionID string  `gorm:"type:uuid;not null"`
	Action        string  `gorm:"not null"`
	AnswerKey     []byte  `gorm:"type:jsonb"`
	Reason        string  `gorm:"not null"`
	CreatedBy     *string `gorm:"type:uuid"`
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

func (AdjustmentEntity) TableName() string { return "exam_item_adjustments" }

// ---- inputs ----

// RubricScore is one criterion score given by a grader.
type RubricScore struct {
	Criterion string  `json:"criterion"`
	Score     float64 `json:"score"`
}

// GradeInput is the body of POST /api/v1/grading/items/:id.
// Give either score, or rubric_scores (then score = rubric total scaled to the item's points).
type GradeInput struct {
	Score        *float64      `json:"score"`
	RubricScores []RubricScore `json:"rubric_scores"`
	Comment      *string       `json:"comment"`
}

// GradingFilter pages GET /api/v1/exams/:id/grading.
type GradingFilter struct {
	Page          int
	PerPage       int
	ItemVersionID string
	Status        string // pending (default) | graded | all
	Blind         bool   // hide who answered
}

// AdjustmentFilter pages GET /api/v1/exams/:id/adjustments.
type AdjustmentFilter struct {
	Page    int
	PerPage int
}

// AdjustmentInput is the body of POST /api/v1/exams/:id/adjustments.
type AdjustmentInput struct {
	ItemVersionID string          `json:"item_version_id"`
	Action        string          `json:"action"`
	AnswerKey     json.RawMessage `json:"answer_key"` // override_key only, same shape as the item's key
	Reason        string          `json:"reason"`
}

// ---- outputs ----

// GradingSummaryRow is one essay item of an exam with its grading progress.
type GradingSummaryRow struct {
	ItemVersionID string  `json:"item_version_id"`
	ItemID        string  `json:"item_id"`
	Code          *string `json:"code"`
	StemPreview   string  `json:"stem_preview"`
	Pending       int     `json:"pending"`
	Graded        int     `json:"graded"`
}

// GradingQueueRow is one essay answer in the grading queue.
type GradingQueueRow struct {
	AttemptItemID string          `json:"attempt_item_id"`
	AttemptID     string          `json:"attempt_id"`
	ItemVersionID string          `json:"item_version_id"`
	UserID        *string         `json:"user_id,omitempty"` // omitted in blind mode
	Username      *string         `json:"username,omitempty"`
	FullName      *string         `json:"full_name,omitempty"`
	Response      json.RawMessage `json:"response"`
	MaxPoints     float64         `json:"max_points"`
	Score         *float64        `json:"score"`
	ScoreSource   *string         `json:"score_source"`
	AnsweredAt    *time.Time      `json:"answered_at"`
}

// GradeHistoryRow is one past manual grade.
type GradeHistoryRow struct {
	GraderID     string          `json:"grader_id"`
	GraderName   string          `json:"grader_name"`
	Score        float64         `json:"score"`
	RubricScores json.RawMessage `json:"rubric_scores"`
	Comment      *string         `json:"comment"`
	CreatedAt    time.Time       `json:"created_at"`
}

// GradingItemDTO is everything a grader needs for one essay answer.
type GradingItemDTO struct {
	AttemptItemID string            `json:"attempt_item_id"`
	AttemptID     string            `json:"attempt_id"`
	ItemVersionID string            `json:"item_version_id"`
	Stem          string            `json:"stem"`
	ModelAnswer   *string           `json:"model_answer"`
	Rubric        json.RawMessage   `json:"rubric"`
	Response      json.RawMessage   `json:"response"`
	MaxPoints     float64           `json:"max_points"`
	Score         *float64          `json:"score"`
	ScoreSource   *string           `json:"score_source"`
	History       []GradeHistoryRow `json:"history"`
	AttemptScore  *float64          `json:"attempt_final_score"`
	AttemptStatus Status            `json:"attempt_status"`
}

// AdjustmentDTO is an adjustment with how many attempts it affected.
type AdjustmentDTO struct {
	ID            string          `json:"id"`
	ItemVersionID string          `json:"item_version_id"`
	Action        string          `json:"action"`
	AnswerKey     json.RawMessage `json:"answer_key"`
	Reason        string          `json:"reason"`
	CreatedBy     *string         `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	RevokedAt     *time.Time      `json:"revoked_at"`
	Regraded      int             `json:"regraded,omitempty"` // attempts recomputed by this request
}
