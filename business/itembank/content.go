package itembank

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
	"doexam/pkg/richtext"
)

var optionIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,16}$`)

// canonical is a validated, sanitized item version body ready to store.
type canonical struct {
	Type       modelBank.ItemType
	Content    []byte
	AnswerKey  []byte // nil only for essays without a model answer
	Scoring    []byte
	SearchText string
}

// equal reports whether two canonical bodies are the same (decides whether a locked
// version needs a new version).
func (c canonical) equal(o canonical) bool {
	return c.Type == o.Type && bytes.Equal(c.Content, o.Content) &&
		bytes.Equal(c.AnswerKey, o.AnswerKey) && bytes.Equal(c.Scoring, o.Scoring)
}

// normalizeBody validates content/answer_key/scoring for type t, sanitizes every rich
// text field and returns canonical JSON. Field errors are added to v.
func normalizeBody(t modelBank.ItemType, rawContent, rawKey, rawScoring json.RawMessage, v errr.Validation) canonical {
	out := canonical{Type: t}
	switch t {
	case modelBank.TypeMCQSingle, modelBank.TypeMCQMulti, modelBank.TypeTrueFalse, modelBank.TypeShortAnswer, modelBank.TypeEssay:
	default:
		v.Check(false, "type", "must be mcq_single, mcq_multi, true_false, short_answer or essay")
		return out
	}

	var content modelBank.Content
	if !decodeStrict(rawContent, &content) {
		v.Check(false, "content", "required JSON object with the fields of this item type")
		return out
	}
	content.Stem = sanitizeField(content.Stem, "content.stem", true, v)

	var key modelBank.AnswerKeyInput
	keyGiven := len(bytes.TrimSpace(rawKey)) > 0 && string(bytes.TrimSpace(rawKey)) != "null"
	if keyGiven && !decodeStrict(rawKey, &key) {
		v.Check(false, "answer_key", "invalid JSON object")
		return out
	}
	var scoring modelBank.Scoring
	if len(bytes.TrimSpace(rawScoring)) > 0 && string(bytes.TrimSpace(rawScoring)) != "null" && !decodeStrict(rawScoring, &scoring) {
		v.Check(false, "scoring", "invalid JSON object")
		return out
	}

	var storedKey any
	var storedScoring modelBank.Scoring
	search := []string{richtext.PlainText(content.Stem)}

	switch t {
	case modelBank.TypeMCQSingle, modelBank.TypeMCQMulti:
		content.Input, content.MaxWords = "", nil
		v.Check(len(content.Options) >= 2 && len(content.Options) <= modelBank.MaxOptions, "content.options",
			fmt.Sprintf("2-%d options", modelBank.MaxOptions))
		ids := make([]string, 0, len(content.Options))
		for i, o := range content.Options {
			field := fmt.Sprintf("content.options[%d]", i)
			v.Check(optionIDRe.MatchString(o.ID), field+".id", "1-16 chars: a-z, 0-9, _ -")
			v.Check(!slices.Contains(ids, o.ID), field+".id", "duplicate option id")
			ids = append(ids, o.ID)
			content.Options[i].Body = sanitizeField(o.Body, field+".body", true, v)
			search = append(search, richtext.PlainText(content.Options[i].Body))
		}
		if t == modelBank.TypeMCQSingle {
			var correct string
			ok := keyGiven && json.Unmarshal(key.Correct, &correct) == nil && slices.Contains(ids, correct)
			v.Check(ok, "answer_key.correct", "must be the id of one option")
			storedKey = modelBank.KeySingle{Correct: correct}
		} else {
			var correct []string
			ok := keyGiven && json.Unmarshal(key.Correct, &correct) == nil && len(correct) > 0
			for i, c := range correct {
				ok = ok && slices.Contains(ids, c) && !slices.Contains(correct[:i], c)
			}
			v.Check(ok, "answer_key.correct", "must be a non-empty list of distinct option ids")
			storedKey = modelBank.KeyMulti{Correct: correct}
			storedScoring.Method = scoring.Method
			if storedScoring.Method == "" {
				storedScoring.Method = modelBank.ScoringAllOrNothing
			}
			v.Check(storedScoring.Method == modelBank.ScoringAllOrNothing || storedScoring.Method == modelBank.ScoringPartial,
				"scoring.method", "must be all_or_nothing or partial")
		}

	case modelBank.TypeTrueFalse:
		content.Options, content.Input, content.MaxWords = nil, "", nil
		v.Check(keyGiven && key.Value != nil, "answer_key.value", "required: true or false")
		if key.Value != nil {
			storedKey = modelBank.KeyTrueFalse{Value: *key.Value}
		}

	case modelBank.TypeShortAnswer:
		content.Options, content.MaxWords = nil, nil
		if content.Input == "" {
			content.Input = "text"
		}
		v.Check(content.Input == "text" || content.Input == "number", "content.input", "must be text or number")
		v.Check(keyGiven && len(key.Accepted) >= 1 && len(key.Accepted) <= 20, "answer_key.accepted", "1-20 accepted answers")
		for i, a := range key.Accepted {
			field := fmt.Sprintf("answer_key.accepted[%d]", i)
			a.Value = strings.TrimSpace(a.Value)
			if a.Match == "" {
				a.Match = modelBank.MatchCaseInsensitive
				if content.Input == "number" {
					a.Match = modelBank.MatchNumeric
				}
			}
			v.Check(a.Value != "" && len(a.Value) <= 200, field+".value", "required, max 200 chars")
			v.Check(a.Match == modelBank.MatchExact || a.Match == modelBank.MatchCaseInsensitive || a.Match == modelBank.MatchNumeric,
				field+".match", "must be exact, case_insensitive or numeric")
			if a.Match == modelBank.MatchNumeric {
				_, err := strconv.ParseFloat(strings.ReplaceAll(a.Value, ",", "."), 64)
				v.Check(err == nil, field+".value", "must be a number")
				v.Check(a.Tolerance == nil || *a.Tolerance >= 0, field+".tolerance", "must be >= 0")
			} else {
				v.Check(a.Tolerance == nil, field+".tolerance", "only for numeric match")
			}
			key.Accepted[i] = a
		}
		storedKey = modelBank.KeyShort{Accepted: key.Accepted}

	case modelBank.TypeEssay:
		content.Options, content.Input = nil, ""
		v.Check(content.MaxWords == nil || (*content.MaxWords >= 1 && *content.MaxWords <= 10000), "content.max_words", "1-10000")
		if keyGiven && key.ModelAnswer != "" {
			storedKey = modelBank.KeyEssay{ModelAnswer: sanitizeField(key.ModelAnswer, "answer_key.model_answer", false, v)}
		}
		for i, r := range scoring.Rubric {
			field := fmt.Sprintf("scoring.rubric[%d]", i)
			r.Criterion = strings.TrimSpace(r.Criterion)
			v.Check(r.Criterion != "" && len(r.Criterion) <= 200, field+".criterion", "required, max 200 chars")
			v.Check(r.Max > 0 && r.Max <= 1000, field+".max", "must be > 0 and <= 1000")
			scoring.Rubric[i] = r
		}
		v.Check(len(scoring.Rubric) <= 20, "scoring.rubric", "max 20 criteria")
		storedScoring.Rubric = scoring.Rubric
	}

	out.Content = mustJSON(content)
	if storedKey != nil {
		out.AnswerKey = mustJSON(storedKey)
	}
	out.Scoring = mustJSON(storedScoring)
	out.SearchText = strings.TrimSpace(strings.Join(search, " "))
	return out
}

// normalizeMeta validates and canonicalizes the non-versioned metadata fields in place.
func normalizeMeta(in *modelBank.VersionInput, v errr.Validation) {
	if in.DefaultPoints != nil {
		p := math.Round(*in.DefaultPoints*100) / 100
		in.DefaultPoints = &p
		v.Check(p >= 0 && p <= 10000, "default_points", "must be 0-10000")
	}
	if in.Difficulty != nil {
		d := strings.ToLower(strings.TrimSpace(*in.Difficulty))
		in.Difficulty = &d
		v.Check(d == "" || d == "easy" || d == "medium" || d == "hard", "difficulty", "must be easy, medium or hard")
	}
	if in.CognitiveLevel != nil {
		c := strings.TrimSpace(*in.CognitiveLevel)
		in.CognitiveLevel = &c
		v.Check(len(c) <= 20, "cognitive_level", "max 20 chars")
	}
	if in.Tags != nil {
		seen := map[string]bool{}
		tags := []string{}
		for _, t := range *in.Tags {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" || seen[t] {
				continue
			}
			v.Check(len(t) <= 50, "tags", "each tag max 50 chars")
			seen[t] = true
			tags = append(tags, t)
		}
		v.Check(len(tags) <= modelBank.MaxTags, "tags", fmt.Sprintf("max %d tags", modelBank.MaxTags))
		in.Tags = &tags
	}
	if in.Explanation != nil {
		e := sanitizeField(*in.Explanation, "explanation", false, v)
		in.Explanation = &e
	}
}

// sanitizeField sanitizes one rich-text field; required fields must not be blank.
func sanitizeField(s, field string, required bool, v errr.Validation) string {
	out, err := richtext.Sanitize(s)
	if err != nil {
		v.Check(false, field, err.Error())
		return ""
	}
	if required {
		v.Check(!richtext.IsBlank(out), field, "required")
	}
	return out
}

func decodeStrict(raw json.RawMessage, dst any) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst) == nil
}

// mustJSON marshals plain structs without escaping < > & (the HTML is already sanitized,
// and escaped JSON is unreadable in the database).
func mustJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("marshal canonical item body: %v", err)) // plain structs: unreachable
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
