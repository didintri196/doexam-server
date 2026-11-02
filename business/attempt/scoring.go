package attempt

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"

	modelAttempt "doexam/models/attempt"
)

// Item types (mirror item_versions.type).
const (
	typeMCQSingle   = "mcq_single"
	typeMCQMulti    = "mcq_multi"
	typeTrueFalse   = "true_false"
	typeShortAnswer = "short_answer"
	typeEssay       = "essay"
)

// Adjustment actions (exam_item_adjustments.action).
const (
	adjFullCredit  = "annul_full_credit"
	adjRemove      = "annul_remove"
	adjOverrideKey = "override_key"
)

// adjustment is an active regrade decision for one item version.
type adjustment struct {
	Action    string
	AnswerKey []byte
}

// itemScore is the outcome of scoring one item.
type itemScore struct {
	Score     *float64 // nil = waits for manual grading
	MaxPoints float64  // 0 when the item is annulled and removed
	IsCorrect *bool
	Source    string
}

// response shapes per type (union).
type responseBody struct {
	Selected json.RawMessage `json:"selected"` // "o2" or ["o1","o3"]
	Value    *bool           `json:"value"`
	Text     *string         `json:"text"`
}

type keyBody struct {
	Correct  json.RawMessage `json:"correct"`
	Value    *bool           `json:"value"`
	Accepted []struct {
		Value     string   `json:"value"`
		Match     string   `json:"match"`
		Tolerance *float64 `json:"tolerance"`
	} `json:"accepted"`
}

// scoreItem scores one response. negative is subtracted for a wrong, non-blank
// objective answer (exams.negative_marking). Blank answers score 0. Non-blank essays
// return a nil score (manual grading); blank essays score 0.
func scoreItem(itemType string, key, scoring, response []byte, maxPoints, negative float64, adj *adjustment) itemScore {
	if adj != nil {
		switch adj.Action {
		case adjFullCredit:
			return itemScore{Score: f(maxPoints), MaxPoints: maxPoints, Source: modelAttempt.SourceAdjustment}
		case adjRemove:
			return itemScore{Score: f(0), MaxPoints: 0, Source: modelAttempt.SourceAdjustment}
		case adjOverrideKey:
			key = adj.AnswerKey
		}
	}
	source := modelAttempt.SourceAuto
	if adj != nil {
		source = modelAttempt.SourceAdjustment
	}

	var r responseBody
	blank := len(response) == 0 || string(response) == "null" || json.Unmarshal(response, &r) != nil || isBlank(itemType, r)
	if itemType == typeEssay {
		if blank {
			return itemScore{Score: f(0), MaxPoints: maxPoints, Source: source}
		}
		return itemScore{MaxPoints: maxPoints}
	}
	if blank {
		return itemScore{Score: f(0), MaxPoints: maxPoints, IsCorrect: b(false), Source: source}
	}

	var k keyBody
	_ = json.Unmarshal(key, &k) // keys are validated when items are saved
	fraction := 0.0
	switch itemType {
	case typeMCQSingle:
		var sel, want string
		_ = json.Unmarshal(r.Selected, &sel)
		_ = json.Unmarshal(k.Correct, &want)
		if sel == want {
			fraction = 1
		}
	case typeMCQMulti:
		var sel, want []string
		_ = json.Unmarshal(r.Selected, &sel)
		_ = json.Unmarshal(k.Correct, &want)
		var sc struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(scoring, &sc)
		right, wrong := 0, 0
		for _, s := range uniq(sel) {
			if slices.Contains(want, s) {
				right++
			} else {
				wrong++
			}
		}
		switch {
		case right == len(want) && wrong == 0:
			fraction = 1
		case sc.Method == "partial" && len(want) > 0:
			// (right − wrong) / total right, never below 0 (DOCS/PLAN/03-CBT-CAT §2.3)
			fraction = math.Max(0, float64(right-wrong)/float64(len(want)))
		}
	case typeTrueFalse:
		if k.Value != nil && *r.Value == *k.Value {
			fraction = 1
		}
	case typeShortAnswer:
		for _, a := range k.Accepted {
			if matchShort(*r.Text, a.Value, a.Match, a.Tolerance) {
				fraction = 1
				break
			}
		}
	}

	score := round2(fraction * maxPoints)
	correct := fraction == 1
	if fraction == 0 && negative > 0 {
		score = -negative
	}
	return itemScore{Score: f(score), MaxPoints: maxPoints, IsCorrect: b(correct), Source: source}
}

func isBlank(itemType string, r responseBody) bool {
	switch itemType {
	case typeMCQSingle:
		var s string
		return json.Unmarshal(r.Selected, &s) != nil || s == ""
	case typeMCQMulti:
		var s []string
		return json.Unmarshal(r.Selected, &s) != nil || len(s) == 0
	case typeTrueFalse:
		return r.Value == nil
	default:
		return r.Text == nil || strings.TrimSpace(*r.Text) == ""
	}
}

// matchShort compares a short answer with one accepted answer.
func matchShort(got, want, mode string, tolerance *float64) bool {
	switch mode {
	case "numeric":
		g, err1 := parseNumber(got)
		w, err2 := parseNumber(want)
		if err1 != nil || err2 != nil {
			return false
		}
		tol := 1e-9
		if tolerance != nil {
			tol = math.Max(*tolerance, tol)
		}
		return math.Abs(g-w) <= tol
	case "exact":
		return strings.TrimSpace(got) == strings.TrimSpace(want)
	default: // case_insensitive: also ignores repeated spaces
		return strings.EqualFold(collapse(got), collapse(want))
	}
}

// parseNumber accepts "3.14", "3,14" and thousands separators like "1.000,5" or "1,000.5".
func parseNumber(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	dot, comma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case dot >= 0 && comma >= 0 && comma > dot: // 1.000,5
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	case dot >= 0 && comma >= 0: // 1,000.5
		s = strings.ReplaceAll(s, ",", "")
	case comma >= 0: // 3,14
		s = strings.Replace(s, ",", ".", 1)
	}
	return strconv.ParseFloat(s, 64)
}

func collapse(s string) string { return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ") }

func uniq(in []string) []string {
	var out []string
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func f(x float64) *float64     { return &x }
func b(x bool) *bool           { return &x }
