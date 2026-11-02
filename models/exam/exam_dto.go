package exam

import (
	"encoding/json"
	"time"

	"doexam/models/opt"
)

// Limits.
const (
	MaxItemsPerRequest = 500
	MaxRuleItemCount   = 500
	MaxSettingsBytes   = 4 << 10
)

// ListFilter pages GET /api/v1/exams.
type ListFilter struct {
	Page      int
	PerPage   int
	Status    Status
	SubjectID string
	Query     string
	Mine      bool // only exams created by the caller
}

// SecurityConfig is exams.security.
type SecurityConfig struct {
	Profile string `json:"profile"` // light | standard
}

// ExamInput is the body of POST /api/v1/exams and PATCH /api/v1/exams/:id.
// Every field is optional on PATCH. For subject_id, description and instructions
// "" clears; duration_minutes and passing_score are cleared with null.
type ExamInput struct {
	SubjectID        *string               `json:"subject_id"`
	Title            *string               `json:"title"`
	Description      *string               `json:"description"`
	Instructions     *string               `json:"instructions"` // HTML
	Mode             *Mode                 `json:"mode"`
	DurationMinutes  opt.Nullable[int]     `json:"duration_minutes"`
	Navigation       *Navigation           `json:"navigation"`
	ShuffleItems     *bool                 `json:"shuffle_items"`
	ShuffleOptions   *bool                 `json:"shuffle_options"`
	ResultVisibility *ResultVisibility     `json:"result_visibility"`
	ShowExplanation  *bool                 `json:"show_explanation"`
	MaxAttempts      *int                  `json:"max_attempts"`
	AttemptScoring   *AttemptScoring       `json:"attempt_scoring"`
	PassingScore     opt.Nullable[float64] `json:"passing_score"`
	NegativeMarking  *float64              `json:"negative_marking"`
	Security         *SecurityConfig       `json:"security"`
	Settings         json.RawMessage       `json:"settings"` // UI options for the exam client (opaque object)
}

// SectionInput is the body of POST /api/v1/exams/:id/sections and PATCH /api/v1/sections/:id.
type SectionInput struct {
	Title           *string               `json:"title"`
	Instructions    *string               `json:"instructions"` // HTML; "" clears
	DurationMinutes opt.Nullable[int]     `json:"duration_minutes"`
	Selection       *Selection            `json:"selection"`
	AllowRevisit    *bool                 `json:"allow_revisit"`
	PassingScore    opt.Nullable[float64] `json:"passing_score"`
}

// AddItem is one entry of POST /api/v1/sections/:id/items.
type AddItem struct {
	ItemID      string   `json:"item_id"`
	Points      *float64 `json:"points"`
	LockOptions bool     `json:"lock_options"`
}

// AddItemsInput is the body of POST /api/v1/sections/:id/items (appended in order).
type AddItemsInput struct {
	Items []AddItem `json:"items"`
}

// SectionItemUpdateInput is the body of PATCH /api/v1/section-items/:id.
type SectionItemUpdateInput struct {
	Points      opt.Nullable[float64] `json:"points"` // null = item default
	LockOptions *bool                 `json:"lock_options"`
}

// OrderInput is the body of the reorder endpoints: every id, in the new order.
type OrderInput struct {
	IDs []string `json:"ids"`
}

// RuleInput is the body of POST /api/v1/sections/:id/rules and PATCH /api/v1/section-rules/:id.
// competency_id, item_type and difficulty: "" clears; points: null = item default.
type RuleInput struct {
	BankID       *string               `json:"bank_id"`
	CompetencyID *string               `json:"competency_id"`
	ItemType     *string               `json:"item_type"`
	Difficulty   *string               `json:"difficulty"`
	TagsAny      *[]string             `json:"tags_any"`
	ItemCount    *int                  `json:"item_count"`
	Points       opt.Nullable[float64] `json:"points"`
}

// ExamSummaryDTO is a list row.
type ExamSummaryDTO struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	SubjectID       *string    `json:"subject_id"`
	Mode            Mode       `json:"mode"`
	Status          Status     `json:"status"`
	DurationMinutes *int       `json:"duration_minutes"`
	SectionCount    int64      `json:"section_count"`
	CreatedBy       *string    `json:"created_by"`
	PublishedAt     *time.Time `json:"published_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// SectionItemDTO is a hand-picked item in a section.
type SectionItemDTO struct {
	ID              string   `json:"id"`
	ItemID          string   `json:"item_id"`
	Code            *string  `json:"code"`
	Type            string   `json:"type"`
	ItemStatus      string   `json:"item_status"`
	StemPreview     string   `json:"stem_preview"`
	VersionID       string   `json:"version_id"` // pinned version after publish, else current
	VersionNo       int      `json:"version_no"`
	Pinned          bool     `json:"pinned"`
	Points          *float64 `json:"points"`
	EffectivePoints float64  `json:"effective_points"`
	LockOptions     bool     `json:"lock_options"`
	SortOrder       int      `json:"sort_order"`
}

// RuleDTO is a blueprint rule. Available = matching items now (draft) or pool size (published).
type RuleDTO struct {
	ID           string   `json:"id"`
	BankID       string   `json:"bank_id"`
	CompetencyID *string  `json:"competency_id"`
	ItemType     *string  `json:"item_type"`
	Difficulty   *string  `json:"difficulty"`
	TagsAny      []string `json:"tags_any"`
	ItemCount    int      `json:"item_count"`
	Points       *float64 `json:"points"`
	Available    int64    `json:"available"`
	SortOrder    int      `json:"sort_order"`
}

// SectionDTO is a section with its items or rules.
type SectionDTO struct {
	ID              string           `json:"id"`
	SortOrder       int              `json:"sort_order"`
	Title           string           `json:"title"`
	Instructions    *string          `json:"instructions"`
	DurationMinutes *int             `json:"duration_minutes"`
	Selection       Selection        `json:"selection"`
	AllowRevisit    bool             `json:"allow_revisit"`
	PassingScore    *float64         `json:"passing_score"`
	ItemCount       int              `json:"item_count"` // items each participant gets
	Items           []SectionItemDTO `json:"items"`
	Rules           []RuleDTO        `json:"rules"`
}

// ExamDTO is an exam with its full structure.
type ExamDTO struct {
	ID               string           `json:"id"`
	SubjectID        *string          `json:"subject_id"`
	Title            string           `json:"title"`
	Description      *string          `json:"description"`
	Instructions     *string          `json:"instructions"`
	Mode             Mode             `json:"mode"`
	Status           Status           `json:"status"`
	DurationMinutes  *int             `json:"duration_minutes"`
	Navigation       Navigation       `json:"navigation"`
	ShuffleItems     bool             `json:"shuffle_items"`
	ShuffleOptions   bool             `json:"shuffle_options"`
	ResultVisibility ResultVisibility `json:"result_visibility"`
	ShowExplanation  bool             `json:"show_explanation"`
	MaxAttempts      int              `json:"max_attempts"`
	AttemptScoring   AttemptScoring   `json:"attempt_scoring"`
	PassingScore     *float64         `json:"passing_score"`
	NegativeMarking  float64          `json:"negative_marking"`
	Security         json.RawMessage  `json:"security"`
	Settings         json.RawMessage  `json:"settings"`
	ItemCount        int              `json:"item_count"`   // items each participant gets
	TotalPoints      *float64         `json:"total_points"` // nil when a rule uses item defaults (varies per participant)
	TotalMinutes     *int             `json:"total_minutes"`
	Sections         []SectionDTO     `json:"sections"`
	CanEdit          bool             `json:"can_edit"`
	PublishedAt      *time.Time       `json:"published_at"`
	CreatedBy        *string          `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// Problem is one reason an exam cannot be published.
type Problem struct {
	SectionID string `json:"section_id,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	ItemID    string `json:"item_id,omitempty"`
	Message   string `json:"message"`
}

// CheckDTO is the result of GET /api/v1/exams/:id/check.
type CheckDTO struct {
	Ready    bool      `json:"ready"`
	Problems []Problem `json:"problems"`
}
