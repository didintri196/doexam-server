package attempt

import (
	"context"
	"math"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
)

// Submit hands in the caller's attempt. A locked attempt cannot be submitted by the participant.
func (s *AttemptService) Submit(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.ResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Submit", trace.WithAttributes(attribute.String("attempt.id", id)))
	defer span.End()

	var out modelAttempt.ResultDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		a, err := s.own(ctx, p, id, true)
		if err != nil {
			return err
		}
		if err := s.tick(ctx, &a, client); err != nil {
			return err
		}
		switch a.Status {
		case modelAttempt.StatusLocked:
			return errr.WithMessage(errr.ErrConflict, "the attempt is locked by the proctor")
		case modelAttempt.StatusInProgress:
			if err := s.finalize(ctx, &a, modelAttempt.ReasonParticipant, nil, client); err != nil {
				return err
			}
		}
		out, err = s.result(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.ResultDTO{}, fail(span, err)
	}
	return out, nil
}

// FinishSection closes the caller's current section. In sequential mode the next section
// starts; in shared mode it only applies to sections that do not allow revisiting.
func (s *AttemptService) FinishSection(ctx context.Context, p modelAuth.Principal, id, attemptSectionID string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.FinishSection", trace.WithAttributes(attribute.String("attempt.id", id)))
	defer span.End()

	var out modelAttempt.AttemptView
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		a, err := s.own(ctx, p, id, true)
		if err != nil {
			return err
		}
		if err := s.tick(ctx, &a, client); err != nil {
			return err
		}
		if a.Status != modelAttempt.StatusInProgress {
			return errr.WithMessage(errr.ErrConflict, "the attempt is not in progress")
		}
		snap, err := decodeSnapshot(a.ConfigSnapshot)
		if err != nil {
			return err
		}
		sections, err := s.storer.ListAttemptSections(ctx, a.ID)
		if err != nil {
			return err
		}
		idx := -1
		for i, sec := range sections {
			if sec.ID == attemptSectionID {
				idx = i
			}
		}
		if idx < 0 || uuid.Validate(attemptSectionID) != nil {
			return errr.WithMessage(errr.ErrNotFound, "section not found")
		}
		if sectionState(sections[idx]) != stateActive {
			return errr.WithMessage(errr.ErrConflict, "the section is not active")
		}
		if snap.SectionMode == modeShared && sectionMeta(snap, sections[idx].SectionID).AllowRevisit {
			return errr.WithMessage(errr.ErrConflict, "this section can be revisited; just move on or submit")
		}
		if snap.SectionMode == modeSequential && idx == len(sections)-1 {
			return errr.WithMessage(errr.ErrConflict, "this is the last section; submit the attempt instead")
		}
		if err := s.closeSection(ctx, &a, sections, idx, client); err != nil {
			return err
		}
		out, err = s.view(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.AttemptView{}, fail(span, err)
	}
	return out, nil
}

// finalize submits a running attempt and scores it (see recompute), closing open sections.
func (s *AttemptService) finalize(ctx context.Context, a *modelAttempt.AttemptEntity, reason modelAttempt.SubmitReason, actorID *string, client modelAudit.Client) error {
	now := s.now().UTC()
	a.Status, a.SubmittedAt, a.SubmitReason = modelAttempt.StatusSubmitted, &now, &reason
	if err := s.recompute(ctx, a, true); err != nil {
		return err
	}
	return s.event(ctx, *a, modelAttempt.EventSubmitted, actorID, map[string]any{"reason": reason, "pending_manual": a.PendingManualCount}, client)
}

// recompute (re)scores every item of a submitted attempt and its section and attempt totals.
// It is deterministic, so submission, manual grading and regrades all share it:
//   - objective items: scoreItem with the frozen answer key, or an active adjustment;
//   - essays: the latest manual grade, unless an annulment applies; otherwise pending;
//   - an "annul_remove" adjustment takes the item out of the maximum (max_points itself is never changed,
//     so revoking the adjustment restores it).
//
// The attempt becomes "graded" when nothing is pending, "submitted" otherwise.
func (s *AttemptService) recompute(ctx context.Context, a *modelAttempt.AttemptEntity, closeSections bool) error {
	now := s.now().UTC()
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil {
		return err
	}
	items, err := s.storer.ListItems(ctx, a.ID)
	if err != nil {
		return err
	}
	sections, err := s.storer.ListAttemptSections(ctx, a.ID)
	if err != nil {
		return err
	}
	adjustments, err := s.storer.Adjustments(ctx, a.ExamID)
	if err != nil {
		return err
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	manual, err := s.storer.LatestManualScores(ctx, ids)
	if err != nil {
		return err
	}

	type sum struct{ raw, max float64 }
	bySection := map[string]*sum{}
	total, pending := sum{}, 0
	var changed []modelAttempt.AttemptItemEntity
	for _, it := range items {
		var adj *adjustment
		if x, ok := adjustments[it.ItemVersionID]; ok {
			adj = &x
		}
		res := scoreWithManual(it, snap.NegativeMarking, adj, manual[it.ID])
		if res.Score == nil {
			pending++
		}
		if !sameScore(it.AttemptItemEntity, res) {
			e := it.AttemptItemEntity
			e.Score, e.IsCorrect = res.Score, res.IsCorrect
			e.ScoreSource, e.ScoredAt = nil, nil
			if res.Score != nil {
				src := res.Source
				e.ScoreSource, e.ScoredAt = &src, &now
			}
			changed = append(changed, e)
		}
		ss := bySection[it.AttemptSectionID]
		if ss == nil {
			ss = &sum{}
			bySection[it.AttemptSectionID] = ss
		}
		ss.max += res.MaxPoints
		total.max += res.MaxPoints
		if res.Score != nil {
			ss.raw += *res.Score
			total.raw += *res.Score
		}
	}
	if err := s.storer.SaveScores(ctx, changed); err != nil {
		return err
	}
	for i := range sections {
		sec := &sections[i]
		ss := bySection[sec.ID]
		if ss == nil {
			ss = &sum{}
		}
		sec.RawScore, sec.MaxScore, sec.Passed = f(round2(ss.raw)), f(round2(ss.max)), nil
		if ps := sectionMeta(snap, sec.SectionID).PassingScore; ps != nil && pending == 0 {
			sec.Passed = b(percent(ss.raw, ss.max) >= *ps)
		}
		if closeSections && sec.ClosedAt == nil {
			sec.ClosedAt = &now
		}
		if err := s.storer.UpdateAttemptSection(ctx, sec); err != nil {
			return err
		}
	}

	a.RawScore, a.MaxScore = f(round2(total.raw)), f(round2(total.max))
	a.FinalScore = f(percent(total.raw, total.max))
	a.PendingManualCount, a.Passed = pending, nil
	if pending == 0 {
		if a.GradedAt == nil || a.Status != modelAttempt.StatusGraded {
			a.GradedAt = &now
		}
		a.Status = modelAttempt.StatusGraded
		if snap.PassingScore != nil {
			a.Passed = b(*a.FinalScore >= *snap.PassingScore)
		}
	} else {
		a.Status, a.GradedAt = modelAttempt.StatusSubmitted, nil
	}
	return s.storer.UpdateAttempt(ctx, a)
}

// scoreWithManual scores one item, using the latest manual grade for essays.
func scoreWithManual(it ItemRow, negative float64, adj *adjustment, manual *float64) itemScore {
	annulled := adj != nil && (adj.Action == adjFullCredit || adj.Action == adjRemove)
	if it.Type == typeEssay && manual != nil && !annulled {
		return itemScore{Score: f(*manual), MaxPoints: it.MaxPoints, Source: modelAttempt.SourceManual}
	}
	return scoreItem(it.Type, it.AnswerKey, it.Scoring, it.Response, it.MaxPoints, negative, adj)
}

func sameScore(e modelAttempt.AttemptItemEntity, res itemScore) bool {
	eqF := (e.Score == nil) == (res.Score == nil) && (e.Score == nil || *e.Score == *res.Score)
	eqB := (e.IsCorrect == nil) == (res.IsCorrect == nil) && (e.IsCorrect == nil || *e.IsCorrect == *res.IsCorrect)
	eqS := res.Score == nil || (e.ScoreSource != nil && *e.ScoreSource == res.Source)
	return eqF && eqB && eqS
}

// percent maps raw/max to 0-100 (two decimals). Negative marking can push raw below 0; the
// final score never goes under 0.
func percent(raw, maxScore float64) float64 {
	if maxScore <= 0 {
		return 0
	}
	return round2(math.Min(100, math.Max(0, raw/maxScore*100)))
}

// Result returns the caller's result, respecting the exam's result_visibility.
func (s *AttemptService) Result(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.ResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Result", trace.WithAttributes(attribute.String("attempt.id", id)))
	defer span.End()

	var out modelAttempt.ResultDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		a, err := s.own(ctx, p, id, true)
		if err != nil {
			return err
		}
		if err := s.tick(ctx, &a, client); err != nil {
			return err
		}
		out, err = s.result(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.ResultDTO{}, fail(span, err)
	}
	return out, nil
}

func (s *AttemptService) result(ctx context.Context, a modelAttempt.AttemptEntity) (modelAttempt.ResultDTO, error) {
	out := modelAttempt.ResultDTO{AttemptID: a.ID, Status: a.Status, PendingManual: a.PendingManualCount}
	if a.Status != modelAttempt.StatusSubmitted && a.Status != modelAttempt.StatusGraded {
		return out, nil
	}
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil {
		return out, err
	}
	switch modelExam.ResultVisibility(snap.ResultVisibility) {
	case modelExam.ResultImmediately:
		out.Visible = true
	case modelExam.ResultAfterGrading:
		out.Visible = a.Status == modelAttempt.StatusGraded
	case modelExam.ResultAfterSession:
		sess, err := s.storer.FindSession(ctx, a.SessionID)
		if err != nil {
			return out, err
		}
		out.Visible = a.Status == modelAttempt.StatusGraded && (sess.Status == "closed" || !s.now().Before(sess.EndsAt))
	}
	if !out.Visible {
		return out, nil
	}
	out.RawScore, out.MaxScore, out.FinalScore, out.Passed = a.RawScore, a.MaxScore, a.FinalScore, a.Passed
	items, err := s.storer.ListItems(ctx, a.ID)
	if err != nil {
		return out, err
	}
	adjustments, err := s.storer.Adjustments(ctx, a.ExamID)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		r := modelAttempt.ItemResult{Position: it.Position, Score: it.Score, MaxPoints: it.MaxPoints, IsCorrect: it.IsCorrect}
		if adj, ok := adjustments[it.ItemVersionID]; ok && adj.Action == adjRemove {
			r.MaxPoints = 0 // annulled and removed: does not count, consistent with max_score
		}
		if snap.ShowExplanation {
			r.Explanation = bodyOf(it.Explanation)
		}
		out.Items = append(out.Items, r)
	}
	return out, nil
}

// MyAttempts lists the caller's attempts, newest first.
func (s *AttemptService) MyAttempts(ctx context.Context, p modelAuth.Principal) ([]modelAttempt.MyAttemptDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.MyAttempts")
	defer span.End()

	var out []modelAttempt.MyAttemptDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		out, err = s.storer.MyAttempts(ctx, p.UserID)
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}
