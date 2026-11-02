package attempt

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
)

// ListAdjustments returns the regrade adjustments of an exam (active and revoked), newest first.
func (s *AttemptService) ListAdjustments(ctx context.Context, p modelAuth.Principal, examID string) ([]modelAttempt.AdjustmentDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.ListAdjustments", trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	var out []modelAttempt.AdjustmentDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, err := s.gradingExam(ctx, p, examID); err != nil {
			return err
		}
		rows, err := s.storer.ListAdjustments(ctx, examID)
		if err != nil {
			return err
		}
		out = make([]modelAttempt.AdjustmentDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, toAdjustmentDTO(r, 0))
		}
		return nil
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// CreateAdjustment annuls an item or corrects its key for one exam, then recomputes every
// submitted attempt that received the item. The item version itself stays untouched.
func (s *AttemptService) CreateAdjustment(ctx context.Context, p modelAuth.Principal, examID string, in modelAttempt.AdjustmentInput, client modelAudit.Client) (modelAttempt.AdjustmentDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.CreateAdjustment", trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	in.ItemVersionID = strings.ToLower(strings.TrimSpace(in.ItemVersionID))
	in.Reason = strings.TrimSpace(in.Reason)
	v := errr.Validation{}
	v.Check(uuid.Validate(in.ItemVersionID) == nil, "item_version_id", "required UUID")
	v.Check(slices.Contains([]string{modelAttempt.AdjustAnnulFullCredit, modelAttempt.AdjustAnnulRemove, modelAttempt.AdjustOverrideKey}, in.Action),
		"action", "must be annul_full_credit, annul_remove or override_key")
	v.Check(in.Reason != "" && utf8.RuneCountInString(in.Reason) <= 1000, "reason", "required, max 1000 chars")
	if err := v.Err(); err != nil {
		return modelAttempt.AdjustmentDTO{}, fail(span, err)
	}

	var out modelAttempt.AdjustmentDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		exam, err := s.gradingExam(ctx, p, examID)
		if err != nil {
			return err
		}
		if exam.Status == modelExam.StatusDraft {
			return errr.WithMessage(errr.ErrConflict, "the exam is not published")
		}
		typ, content, ok, err := s.storer.VersionInExam(ctx, examID, in.ItemVersionID)
		if err != nil {
			return err
		}
		if !ok {
			return &errr.ValidationError{Fields: map[string]string{"item_version_id": "this item version is not part of the exam"}}
		}
		adj := modelAttempt.AdjustmentEntity{
			ID: newID(), TenantID: p.TenantID, ExamID: examID, ItemVersionID: in.ItemVersionID, Action: in.Action,
			Reason: in.Reason, CreatedBy: &p.UserID, CreatedAt: s.now().UTC(),
		}
		if in.Action == modelAttempt.AdjustOverrideKey {
			key, bad := validateKey(typ, content, in.AnswerKey)
			if bad != "" {
				return &errr.ValidationError{Fields: map[string]string{"answer_key": bad}}
			}
			adj.AnswerKey = key
		}
		if err := s.storer.CreateAdjustment(ctx, &adj); err != nil {
			return err
		}
		n, err := s.regrade(ctx, examID, in.ItemVersionID)
		if err != nil {
			return err
		}
		out = toAdjustmentDTO(adj, n)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionAdjustmentCreate, EntityType: "exam", EntityID: examID,
			After: map[string]any{"adjustment": out, "regraded_attempts": n}, Client: client,
		}))
	})
	if err != nil {
		return modelAttempt.AdjustmentDTO{}, fail(span, err)
	}
	s.logger.InfoContext(ctx, "exam regraded", slog.String("exam_id", examID), slog.String("action", in.Action), slog.Int("attempts", out.Regraded))
	return out, nil
}

// RevokeAdjustment undoes an adjustment and recomputes the affected attempts again
// (manual essay grades given before an annulment count again).
func (s *AttemptService) RevokeAdjustment(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AdjustmentDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.RevokeAdjustment", trace.WithAttributes(attribute.String("adjustment.id", id)))
	defer span.End()

	var out modelAttempt.AdjustmentDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if uuid.Validate(id) != nil {
			return errr.WithMessage(errr.ErrNotFound, "adjustment not found")
		}
		adj, err := s.storer.FindAdjustment(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.gradingExam(ctx, p, adj.ExamID); err != nil {
			return err
		}
		if adj.RevokedAt != nil {
			return errr.WithMessage(errr.ErrConflict, "the adjustment is already revoked")
		}
		now := s.now().UTC()
		if err := s.storer.RevokeAdjustment(ctx, id, now); err != nil {
			return err
		}
		adj.RevokedAt = &now
		n, err := s.regrade(ctx, adj.ExamID, adj.ItemVersionID)
		if err != nil {
			return err
		}
		out = toAdjustmentDTO(adj, n)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionAdjustmentRevoke, EntityType: "exam", EntityID: adj.ExamID,
			After: map[string]any{"adjustment": out, "regraded_attempts": n}, Client: client,
		}))
	})
	if err != nil {
		return modelAttempt.AdjustmentDTO{}, fail(span, err)
	}
	return out, nil
}

// regrade recomputes every submitted attempt of the exam that contains the version.
func (s *AttemptService) regrade(ctx context.Context, examID, versionID string) (int, error) {
	attempts, err := s.storer.AttemptsWithVersion(ctx, examID, versionID)
	if err != nil {
		return 0, err
	}
	for i := range attempts {
		if err := s.recompute(ctx, &attempts[i], false); err != nil {
			return 0, err
		}
	}
	return len(attempts), nil
}

// validateKey checks an override key against the item's type and options and returns it canonical.
func validateKey(itemType string, content []byte, raw json.RawMessage) ([]byte, string) {
	ids := optionIDs(content)
	var k keyBody
	if len(raw) == 0 || json.Unmarshal(raw, &k) != nil {
		return nil, "required JSON object in the item's key shape"
	}
	switch itemType {
	case typeMCQSingle:
		var c string
		if json.Unmarshal(k.Correct, &c) != nil || !slices.Contains(ids, c) {
			return nil, `{"correct": "<option id>"}`
		}
		return mustJSON(map[string]string{"correct": c}), ""
	case typeMCQMulti:
		var c []string
		if json.Unmarshal(k.Correct, &c) != nil || len(uniq(c)) == 0 {
			return nil, `{"correct": ["<option id>", ...]}`
		}
		for _, x := range c {
			if !slices.Contains(ids, x) {
				return nil, "unknown option: " + x
			}
		}
		return mustJSON(map[string][]string{"correct": uniq(c)}), ""
	case typeTrueFalse:
		if k.Value == nil {
			return nil, `{"value": true|false}`
		}
		return mustJSON(map[string]bool{"value": *k.Value}), ""
	case typeShortAnswer:
		if len(k.Accepted) == 0 || len(k.Accepted) > 20 {
			return nil, `{"accepted": [{"value": "...", "match": "exact|case_insensitive|numeric"}]} (1-20)`
		}
		for _, a := range k.Accepted {
			if strings.TrimSpace(a.Value) == "" || !slices.Contains([]string{"exact", "case_insensitive", "numeric", ""}, a.Match) {
				return nil, "every accepted answer needs a value and a valid match"
			}
			if a.Match == "numeric" {
				if _, err := parseNumber(a.Value); err != nil {
					return nil, "numeric value is not a number: " + strconv.Quote(a.Value)
				}
			}
		}
		return mustJSON(map[string]any{"accepted": k.Accepted}), ""
	}
	return nil, "essays have no key to override; grade them manually or annul the item"
}

func toAdjustmentDTO(a modelAttempt.AdjustmentEntity, regraded int) modelAttempt.AdjustmentDTO {
	key := json.RawMessage("null")
	if len(a.AnswerKey) > 0 {
		key = a.AnswerKey
	}
	return modelAttempt.AdjustmentDTO{
		ID: a.ID, ItemVersionID: a.ItemVersionID, Action: a.Action, AnswerKey: key, Reason: a.Reason,
		CreatedBy: a.CreatedBy, CreatedAt: a.CreatedAt, RevokedAt: a.RevokedAt, Regraded: regraded,
	}
}
