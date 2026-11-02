package attempt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// gradingExam loads an exam the caller may grade: tenant.manage, grade.write, or the exam's
// creator with exam.write (teachers grade their own exams).
func (s *AttemptService) gradingExam(ctx context.Context, p modelAuth.Principal, examID string) (modelExam.ExamEntity, error) {
	if uuid.Validate(examID) != nil {
		return modelExam.ExamEntity{}, errr.WithMessage(errr.ErrNotFound, "exam not found")
	}
	e, err := s.storer.FindExam(ctx, examID)
	if err != nil {
		return e, err
	}
	creator := e.CreatedBy != nil && *e.CreatedBy == p.UserID && p.Has(modelAuth.PermExamWrite)
	if !p.Has(modelAuth.PermTenantManage) && !p.Has(modelAuth.PermGradeWrite) && !creator {
		return modelExam.ExamEntity{}, errr.WithMessage(errr.ErrForbidden, "needs grade.write or being the exam's creator")
	}
	return e, nil
}

// GradingSummary lists the essay items of an exam with pending/graded counts.
func (s *AttemptService) GradingSummary(ctx context.Context, p modelAuth.Principal, examID string) ([]modelAttempt.GradingSummaryRow, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.GradingSummary", trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	var out []modelAttempt.GradingSummaryRow
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, err := s.gradingExam(ctx, p, examID); err != nil {
			return err
		}
		var err error
		out, err = s.storer.GradingSummary(ctx, examID)
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// GradingQueue lists essay answers to grade (default: pending). Blind mode hides who answered.
func (s *AttemptService) GradingQueue(ctx context.Context, p modelAuth.Principal, examID string, f modelAttempt.GradingFilter) ([]modelAttempt.GradingQueueRow, int64, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.GradingQueue", trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	v := errr.Validation{}
	v.Check(f.ItemVersionID == "" || uuid.Validate(f.ItemVersionID) == nil, "item_version_id", "must be a UUID")
	v.Check(f.Status == "" || f.Status == "pending" || f.Status == "graded" || f.Status == "all", "status", "must be pending, graded or all")
	if err := v.Err(); err != nil {
		return nil, 0, err
	}
	var (
		out   []modelAttempt.GradingQueueRow
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, err := s.gradingExam(ctx, p, examID); err != nil {
			return err
		}
		var err error
		out, total, err = s.storer.GradingQueue(ctx, examID, f)
		return err
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	if f.Blind {
		for i := range out {
			out[i].UserID, out[i].Username, out[i].FullName = nil, nil, nil
		}
	}
	return out, total, nil
}

// GradingItem returns one essay answer with stem, rubric, model answer and grade history.
func (s *AttemptService) GradingItem(ctx context.Context, p modelAuth.Principal, attemptItemID string) (modelAttempt.GradingItemDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.GradingItem", trace.WithAttributes(attribute.String("attempt_item.id", attemptItemID)))
	defer span.End()

	var out modelAttempt.GradingItemDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, a, err := s.gradableItem(ctx, p, attemptItemID, false)
		if err != nil {
			return err
		}
		out, err = s.gradingDTO(ctx, it, a)
		return err
	})
	if err != nil {
		return modelAttempt.GradingItemDTO{}, fail(span, err)
	}
	return out, nil
}

// Grade records a manual grade for an essay answer and recomputes the attempt.
// Re-grading is allowed; the history is kept and the latest grade counts.
func (s *AttemptService) Grade(ctx context.Context, p modelAuth.Principal, attemptItemID string, in modelAttempt.GradeInput, client modelAudit.Client) (modelAttempt.GradingItemDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Grade", trace.WithAttributes(attribute.String("attempt_item.id", attemptItemID)))
	defer span.End()

	var out modelAttempt.GradingItemDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, a, err := s.gradableItem(ctx, p, attemptItemID, true)
		if err != nil {
			return err
		}
		adjustments, err := s.storer.Adjustments(ctx, a.ExamID)
		if err != nil {
			return err
		}
		if adj, ok := adjustments[it.ItemVersionID]; ok && adj.Action != adjOverrideKey {
			return errr.WithMessage(errr.ErrConflict, "this item is annulled for the exam; manual grades do not count")
		}
		score, rubric, err := gradeScore(in, it)
		if err != nil {
			return err
		}
		g := modelAttempt.ManualGradeEntity{
			ID: newID(), TenantID: a.TenantID, AttemptItemID: it.ID, GraderID: p.UserID, Score: score,
			RubricScores: rubric, Comment: trimmed(in.Comment, 2000), CreatedAt: s.now().UTC(),
		}
		if err := s.storer.InsertManualGrade(ctx, &g); err != nil {
			return err
		}
		before := a.FinalScore
		if err := s.recompute(ctx, &a, false); err != nil {
			return err
		}
		if err := s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: a.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGradeManual, EntityType: "attempt_item", EntityID: it.ID,
			Before: map[string]any{"score": it.Score, "attempt_final_score": before},
			After:  map[string]any{"score": score, "attempt_final_score": a.FinalScore, "attempt_id": a.ID}, Client: client,
		})); err != nil {
			return err
		}
		if it, err = s.storer.FindItem(ctx, it.ID); err != nil {
			return err
		}
		out, err = s.gradingDTO(ctx, it, a)
		return err
	})
	if err != nil {
		return modelAttempt.GradingItemDTO{}, fail(span, err)
	}
	return out, nil
}

// gradableItem loads a submitted essay answer the caller may grade.
func (s *AttemptService) gradableItem(ctx context.Context, p modelAuth.Principal, attemptItemID string, lock bool) (ItemRow, modelAttempt.AttemptEntity, error) {
	notFound := errr.WithMessage(errr.ErrNotFound, "answer not found")
	if uuid.Validate(attemptItemID) != nil {
		return ItemRow{}, modelAttempt.AttemptEntity{}, notFound
	}
	it, err := s.storer.FindItem(ctx, attemptItemID)
	if err != nil {
		return ItemRow{}, modelAttempt.AttemptEntity{}, err
	}
	a, err := s.storer.FindAttempt(ctx, it.AttemptID, lock) // lock serializes grades of one attempt
	if err != nil {
		return ItemRow{}, modelAttempt.AttemptEntity{}, err
	}
	if _, err := s.gradingExam(ctx, p, a.ExamID); err != nil {
		return ItemRow{}, modelAttempt.AttemptEntity{}, err
	}
	switch {
	case it.Type != typeEssay:
		return ItemRow{}, modelAttempt.AttemptEntity{}, errr.WithMessage(errr.ErrConflict, "only essay answers are graded manually")
	case a.Status != modelAttempt.StatusSubmitted && a.Status != modelAttempt.StatusGraded:
		return ItemRow{}, modelAttempt.AttemptEntity{}, errr.WithMessage(errr.ErrConflict, "the attempt is not submitted yet")
	case len(it.Response) == 0:
		return ItemRow{}, modelAttempt.AttemptEntity{}, errr.WithMessage(errr.ErrConflict, "the answer is empty (scored 0 automatically)")
	}
	return it, a, nil
}

func (s *AttemptService) gradingDTO(ctx context.Context, it ItemRow, a modelAttempt.AttemptEntity) (modelAttempt.GradingItemDTO, error) {
	history, err := s.storer.GradeHistory(ctx, it.ID)
	if err != nil {
		return modelAttempt.GradingItemDTO{}, err
	}
	var content struct {
		Stem string `json:"stem"`
	}
	_ = json.Unmarshal(it.Content, &content)
	var key struct {
		ModelAnswer string `json:"model_answer"`
	}
	_ = json.Unmarshal(it.AnswerKey, &key)
	var scoring struct {
		Rubric json.RawMessage `json:"rubric"`
	}
	_ = json.Unmarshal(it.Scoring, &scoring)
	if len(scoring.Rubric) == 0 {
		scoring.Rubric = json.RawMessage("[]")
	}
	dto := modelAttempt.GradingItemDTO{
		AttemptItemID: it.ID, AttemptID: a.ID, ItemVersionID: it.ItemVersionID, Stem: content.Stem,
		Rubric: scoring.Rubric, Response: it.Response, MaxPoints: it.MaxPoints, Score: it.Score, ScoreSource: it.ScoreSource,
		History: history, AttemptScore: a.FinalScore, AttemptStatus: a.Status,
	}
	if key.ModelAnswer != "" {
		dto.ModelAnswer = &key.ModelAnswer
	}
	return dto, nil
}

// gradeScore validates a grade and returns the score (two decimals) and the stored rubric scores.
// With rubric scores the total is scaled to the item's points when the rubric adds up differently.
func gradeScore(in modelAttempt.GradeInput, it ItemRow) (float64, []byte, error) {
	if len(in.RubricScores) == 0 {
		if in.Score == nil {
			return 0, nil, &errr.ValidationError{Fields: map[string]string{"score": "give score or rubric_scores"}}
		}
		sc := round2(*in.Score)
		if sc < 0 || sc > it.MaxPoints {
			return 0, nil, &errr.ValidationError{Fields: map[string]string{"score": fmt.Sprintf("0-%g", it.MaxPoints)}}
		}
		return sc, nil, nil
	}
	var scoring struct {
		Rubric []struct {
			Criterion string  `json:"criterion"`
			Max       float64 `json:"max"`
		} `json:"rubric"`
	}
	_ = json.Unmarshal(it.Scoring, &scoring)
	if len(scoring.Rubric) == 0 {
		return 0, nil, &errr.ValidationError{Fields: map[string]string{"rubric_scores": "this item has no rubric; give score"}}
	}
	given := map[string]float64{}
	for i, rs := range in.RubricScores {
		c := strings.TrimSpace(rs.Criterion)
		if _, dup := given[c]; dup {
			return 0, nil, &errr.ValidationError{Fields: map[string]string{fmt.Sprintf("rubric_scores[%d]", i): "criterion listed twice"}}
		}
		given[c] = rs.Score
	}
	total, rubricMax := 0.0, 0.0
	stored := make([]modelAttempt.RubricScore, 0, len(scoring.Rubric))
	for _, r := range scoring.Rubric {
		sc, ok := given[r.Criterion]
		if !ok {
			return 0, nil, &errr.ValidationError{Fields: map[string]string{"rubric_scores": "missing criterion: " + r.Criterion}}
		}
		if sc < 0 || sc > r.Max {
			return 0, nil, &errr.ValidationError{Fields: map[string]string{"rubric_scores": fmt.Sprintf("%s: 0-%g", r.Criterion, r.Max)}}
		}
		delete(given, r.Criterion)
		total += sc
		rubricMax += r.Max
		stored = append(stored, modelAttempt.RubricScore{Criterion: r.Criterion, Score: sc})
	}
	for c := range given {
		return 0, nil, &errr.ValidationError{Fields: map[string]string{"rubric_scores": "unknown criterion: " + c}}
	}
	score := total
	if rubricMax > 0 && rubricMax != it.MaxPoints {
		score = total / rubricMax * it.MaxPoints
	}
	return round2(math.Min(score, it.MaxPoints)), mustJSON(stored), nil
}

func trimmed(s *string, maxRunes int) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	if utf8.RuneCountInString(v) > maxRunes {
		v = string([]rune(v)[:maxRunes])
	}
	return &v
}
