package itembank

import modelMedia "doexam/models/media"

// Limits of POST /api/v1/item-banks/:id/import-word. The import runs in one request,
// so the item count stays well under what one transaction writes in about a second.
const (
	MaxWordBytes = 20 << 20
	MaxWordItems = 300
)

// WordIssue is a problem with one item of an imported document.
type WordIssue struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// WordOptionDTO is one answer option as read from the document.
type WordOptionDTO struct {
	Label   string `json:"label"` // A, B, …
	Body    string `json:"body"`  // sanitized HTML
	Correct bool   `json:"correct"`
}

// WordItemDTO is one item as read from the document, with what is wrong with it.
type WordItemDTO struct {
	No          int             `json:"no"` // order in the document, from 1
	Type        ItemType        `json:"type"`
	Stem        string          `json:"stem"` // sanitized HTML
	Options     []WordOptionDTO `json:"options,omitempty"`
	Key         string          `json:"key,omitempty"` // as written after "Kunci:"
	Points      *float64        `json:"points,omitempty"`
	Competency  string          `json:"competency,omitempty"`
	Stimulus    string          `json:"stimulus,omitempty"` // title of its reading text
	Explanation string          `json:"explanation,omitempty"`
	Errors      []WordIssue     `json:"errors"`
	Warnings    []WordIssue     `json:"warnings"`
}

// WordImportDTO is the answer of the import: a preview, or the result of saving.
type WordImportDTO struct {
	Items    []WordItemDTO `json:"items"`
	Valid    int           `json:"valid"`
	Invalid  int           `json:"invalid"`
	Stimuli  int           `json:"stimuli"`
	Warnings []string      `json:"warnings"` // document-wide (unsupported equations, pictures)
	// Media lets the preview show the document's pictures (data: URLs, preview only).
	Media map[string]modelMedia.Ref `json:"media,omitempty"`
	// Saved: items and reading texts created (commit only).
	Created        int  `json:"created"`
	CreatedStimuli int  `json:"created_stimuli"`
	Committed      bool `json:"committed"`
}
