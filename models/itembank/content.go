package itembank

import "encoding/json"

// JSON shapes stored in item_versions (DOCS/PLAN/08-DATABASE-MVP.md §4).
// Option ids are stable ("o1", "o2", ...) so shuffling and distractor analysis never mix them up.

// Option is one answer choice.
type Option struct {
	ID   string `json:"id"`
	Body string `json:"body"` // sanitized HTML
}

// Content is item_versions.content. Fields depend on the item type.
type Content struct {
	Stem     string   `json:"stem"`                // sanitized HTML, all types
	Options  []Option `json:"options,omitempty"`   // mcq_single, mcq_multi
	Input    string   `json:"input,omitempty"`     // short_answer: "text" | "number"
	MaxWords *int     `json:"max_words,omitempty"` // essay
}

// Short-answer match modes.
const (
	MatchExact           = "exact"
	MatchCaseInsensitive = "case_insensitive"
	MatchNumeric         = "numeric"
)

// AcceptedAnswer is one accepted short answer.
type AcceptedAnswer struct {
	Value     string   `json:"value"`
	Match     string   `json:"match"`
	Tolerance *float64 `json:"tolerance,omitempty"` // numeric only
}

// AnswerKeyInput is the union of every key shape, used to decode client input.
type AnswerKeyInput struct {
	Correct     json.RawMessage  `json:"correct"`      // "o2" (single) or ["o1","o3"] (multi)
	Value       *bool            `json:"value"`        // true_false
	Accepted    []AcceptedAnswer `json:"accepted"`     // short_answer
	ModelAnswer string           `json:"model_answer"` // essay (sanitized HTML)
}

// Stored key shapes, one per type.
type (
	KeySingle struct {
		Correct string `json:"correct"`
	}
	KeyMulti struct {
		Correct []string `json:"correct"`
	}
	KeyTrueFalse struct {
		Value bool `json:"value"`
	}
	KeyShort struct {
		Accepted []AcceptedAnswer `json:"accepted"`
	}
	KeyEssay struct {
		ModelAnswer string `json:"model_answer,omitempty"`
	}
)

// Scoring methods for mcq_multi.
const (
	ScoringAllOrNothing = "all_or_nothing"
	ScoringPartial      = "partial"
)

// RubricCriterion is one essay rubric row.
type RubricCriterion struct {
	Criterion string  `json:"criterion"`
	Max       float64 `json:"max"`
}

// Scoring is item_versions.scoring.
type Scoring struct {
	Method string            `json:"method,omitempty"` // mcq_multi
	Rubric []RubricCriterion `json:"rubric,omitempty"` // essay
}

// Explanation is item_versions.explanation.
type Explanation struct {
	Body string `json:"body"` // sanitized HTML
}

// StimulusContent is stimulus_versions.content.
type StimulusContent struct {
	Body string `json:"body"` // sanitized HTML
}
