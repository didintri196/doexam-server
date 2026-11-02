package itembank

import (
	"encoding/json"
	"strings"
	"time"

	"doexam/models/pgtype"
)

// Limits.
const (
	MaxOptions = 10
	MaxTags    = 20
)

// ---- banks ----

// BankFilter pages GET /api/v1/item-banks.
type BankFilter struct {
	Page      int
	PerPage   int
	SubjectID string
	Query     string
}

// BankInput is the body of POST /api/v1/item-banks.
type BankInput struct {
	SubjectID   string  `json:"subject_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// BankUpdateInput is the body of PATCH /api/v1/item-banks/:id. "" clears description.
type BankUpdateInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// MemberInput is the body of POST /api/v1/item-banks/:id/members (upsert).
type MemberInput struct {
	UserID string `json:"user_id"`
	Access Access `json:"access"`
}

// BankDTO is a bank with the caller's access level and item count.
type BankDTO struct {
	ID          string    `json:"id"`
	SubjectID   string    `json:"subject_id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	MyAccess    Access    `json:"my_access"`
	ItemCount   int64     `json:"item_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// MemberDTO is a bank member.
type MemberDTO struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	FullName string `json:"full_name"`
	Access   Access `json:"access"`
}

// ---- stimuli ----

// StimulusInput is the body of POST /api/v1/item-banks/:id/stimuli.
type StimulusInput struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// StimulusUpdateInput is the body of PATCH /api/v1/stimuli/:id.
type StimulusUpdateInput struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
}

// StimulusDTO is a stimulus with its current version.
type StimulusDTO struct {
	ID        string    `json:"id"`
	BankID    string    `json:"bank_id"`
	Title     string    `json:"title"`
	VersionID string    `json:"version_id"`
	VersionNo int       `json:"version_no"`
	Body      string    `json:"body"`
	Locked    bool      `json:"locked"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---- items ----

// ItemFilter pages GET /api/v1/item-banks/:id/items.
type ItemFilter struct {
	Page         int
	PerPage      int
	Status       ItemStatus
	Type         ItemType
	CompetencyID string
	Difficulty   string
	Tag          string
	Query        string // full-text over stem and options
}

// VersionInput carries the versioned fields of an item. On PATCH every field is
// optional; content, answer_key and scoring replace the stored value as a whole.
type VersionInput struct {
	Type           ItemType        `json:"type"`
	Content        json.RawMessage `json:"content"`
	AnswerKey      json.RawMessage `json:"answer_key"`
	Scoring        json.RawMessage `json:"scoring"`
	Explanation    *string         `json:"explanation"` // HTML; "" clears
	DefaultPoints  *float64        `json:"default_points"`
	Difficulty     *string         `json:"difficulty"`      // easy|medium|hard; "" clears
	CognitiveLevel *string         `json:"cognitive_level"` // free text, e.g. C3 or HOTS; "" clears
	Tags           *[]string       `json:"tags"`
	CompetencyID   *string         `json:"competency_id"` // "" clears
	StimulusID     *string         `json:"stimulus_id"`   // "" clears
}

// ItemInput is the body of POST /api/v1/item-banks/:id/items.
type ItemInput struct {
	Code *string `json:"code"`
	VersionInput
}

// ItemUpdateInput is the body of PATCH /api/v1/items/:id.
type ItemUpdateInput struct {
	Code   *string     `json:"code"` // "" clears
	Status *ItemStatus `json:"status"`
	VersionInput
}

// StimulusRefDTO is the stimulus attached to an item version.
type StimulusRefDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	VersionID string `json:"version_id"`
	VersionNo int    `json:"version_no"`
}

// VersionDTO is a full item version for authors (it includes the answer key).
type VersionDTO struct {
	ID             string          `json:"id"`
	VersionNo      int             `json:"version_no"`
	Type           ItemType        `json:"type"`
	Content        json.RawMessage `json:"content"`
	AnswerKey      json.RawMessage `json:"answer_key"`
	Scoring        json.RawMessage `json:"scoring"`
	Explanation    *string         `json:"explanation"`
	DefaultPoints  float64         `json:"default_points"`
	Difficulty     *string         `json:"difficulty"`
	CognitiveLevel *string         `json:"cognitive_level"`
	Tags           []string        `json:"tags"`
	CompetencyID   *string         `json:"competency_id"`
	Stimulus       *StimulusRefDTO `json:"stimulus"`
	Locked         bool            `json:"locked"`
	CreatedAt      time.Time       `json:"created_at"`
}

// ItemDTO is an item with its current version.
type ItemDTO struct {
	ID        string     `json:"id"`
	BankID    string     `json:"bank_id"`
	Code      *string    `json:"code"`
	Status    ItemStatus `json:"status"`
	Version   VersionDTO `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ItemSummaryDTO is a list row (no answer key, stem as plain-text preview).
type ItemSummaryDTO struct {
	ID           string     `json:"id"`
	Code         *string    `json:"code"`
	Status       ItemStatus `json:"status"`
	Type         ItemType   `json:"type"`
	VersionNo    int        `json:"version_no"`
	StemPreview  string     `json:"stem_preview"`
	Difficulty   *string    `json:"difficulty"`
	Tags         []string   `json:"tags"`
	CompetencyID *string    `json:"competency_id"`
	Locked       bool       `json:"locked"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// VersionSummaryDTO is one row of an item's version history.
type VersionSummaryDTO struct {
	ID        string    `json:"id"`
	VersionNo int       `json:"version_no"`
	Type      ItemType  `json:"type"`
	Locked    bool      `json:"locked"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
}

// ItemListRow is an item joined with its current version (repo → service).
type ItemListRow struct {
	ID           string
	Code         *string
	Status       ItemStatus
	Type         ItemType
	VersionNo    int
	SearchText   *string
	Difficulty   *string
	Tags         pgtype.TextArray
	CompetencyID *string
	LockedAt     *time.Time
	UpdatedAt    time.Time
}

// Normalize trims text fields of BankInput.
func (in *BankInput) Normalize() {
	in.SubjectID, in.Name = strings.TrimSpace(in.SubjectID), strings.TrimSpace(in.Name)
	if in.Description != nil {
		v := strings.TrimSpace(*in.Description)
		if v == "" {
			in.Description = nil
		} else {
			in.Description = &v
		}
	}
}
