package exam

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
)

// plan is what publishing writes: the candidate pool of every rule.
type plan struct {
	pools map[string][]Candidate // rule id → candidates
}

// Check reports whether a draft exam can be published, without changing anything.
func (s *ExamService) Check(ctx context.Context, p modelAuth.Principal, id string) (modelExam.CheckDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Check", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	if !canRead(p) {
		return modelExam.CheckDTO{}, fail(span, errr.ErrForbidden)
	}
	if uuid.Validate(id) != nil {
		return modelExam.CheckDTO{}, errExamNotFound
	}
	var out modelExam.CheckDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.storer.FindExam(ctx, id, false)
		if err != nil {
			return err
		}
		problems, _, err := s.check(ctx, e)
		out = modelExam.CheckDTO{Ready: len(problems) == 0, Problems: problems}
		return err
	})
	if err != nil {
		return modelExam.CheckDTO{}, fail(span, err)
	}
	return out, nil
}

// Publish validates a draft exam and, when it is ready, pins the current version of
// every hand-picked item, snapshots the candidate pool of every blueprint rule, and
// locks all those item (and stimulus) versions so they can no longer change.
// When not ready it returns a validation error listing the problems.
func (s *ExamService) Publish(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Publish", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForPublish(ctx, p, id)
		if err != nil {
			return err
		}
		if e.Status != modelExam.StatusDraft {
			return errr.WithMessage(errr.ErrConflict, "only draft exams can be published")
		}
		problems, pl, err := s.check(ctx, e)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return &errr.ValidationError{Fields: problemFields(problems)}
		}

		now := s.now().UTC()
		if err := s.storer.PinSectionItems(ctx, id); err != nil {
			return err
		}
		var pool []modelExam.RulePoolEntity
		for ruleID, cands := range pl.pools {
			for _, c := range cands {
				pool = append(pool, modelExam.RulePoolEntity{RuleID: ruleID, ItemVersionID: c.VersionID, TenantID: p.TenantID})
			}
		}
		if err := s.storer.InsertPool(ctx, pool); err != nil {
			return err
		}
		if err := s.storer.LockVersions(ctx, id, now); err != nil {
			return err
		}
		e.Status, e.PublishedAt = modelExam.StatusPublished, &now
		if err := s.storer.UpdateExam(ctx, &e); err != nil {
			return err
		}
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamPublish, id, nil,
			map[string]any{"item_count": out.ItemCount, "total_points": out.TotalPoints, "pool_size": len(pool)})
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	s.logger.InfoContext(ctx, "exam published", slog.String("tenant_id", p.TenantID), slog.String("exam_id", id))
	return out, nil
}

// Unpublish returns a published exam to draft while no session uses it.
// Pins and pools are cleared; the versions stay locked (editing them makes new versions).
func (s *ExamService) Unpublish(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Unpublish", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForPublish(ctx, p, id)
		if err != nil {
			return err
		}
		if e.Status != modelExam.StatusPublished {
			return errr.WithMessage(errr.ErrConflict, "exam is not published")
		}
		n, err := s.storer.CountLiveSessions(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "exam already has sessions; archive it instead")
		}
		if err := s.storer.ClearPins(ctx, id); err != nil {
			return err
		}
		e.Status, e.PublishedAt = modelExam.StatusDraft, nil
		if err := s.storer.UpdateExam(ctx, &e); err != nil {
			return err
		}
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamUnpublish, id, nil, nil)
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

// Archive retires an exam (read-only from then on).
func (s *ExamService) Archive(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Archive", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForPublish(ctx, p, id)
		if err != nil {
			return err
		}
		if e.Status == modelExam.StatusArchived {
			return errr.WithMessage(errr.ErrConflict, "exam is already archived")
		}
		e.Status = modelExam.StatusArchived
		if err := s.storer.UpdateExam(ctx, &e); err != nil {
			return err
		}
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamArchive, id, nil, nil)
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

func (s *ExamService) loadForPublish(ctx context.Context, p modelAuth.Principal, id string) (modelExam.ExamEntity, error) {
	e, err := s.loadForEdit(ctx, p, id)
	if err != nil {
		return e, err
	}
	if !p.Has(modelAuth.PermExamPublish) {
		return modelExam.ExamEntity{}, errr.WithMessage(errr.ErrForbidden, "publishing needs the exam.publish permission")
	}
	return e, nil
}

// check lists every reason the exam cannot be published and, if there is none,
// the rule pools to snapshot. Must run in a tenant transaction.
func (s *ExamService) check(ctx context.Context, e modelExam.ExamEntity) ([]modelExam.Problem, plan, error) {
	var problems []modelExam.Problem
	add := func(pr modelExam.Problem) { problems = append(problems, pr) }
	pl := plan{pools: map[string][]Candidate{}}

	if e.Status != modelExam.StatusDraft {
		add(modelExam.Problem{Message: "exam is not a draft"})
	}
	sections, err := s.storer.ListSections(ctx, e.ID)
	if err != nil {
		return nil, pl, err
	}
	items, err := s.storer.ListSectionItems(ctx, e.ID)
	if err != nil {
		return nil, pl, err
	}
	rules, err := s.storer.ListRules(ctx, e.ID)
	if err != nil {
		return nil, pl, err
	}
	if len(sections) == 0 {
		add(modelExam.Problem{Message: "exam has no sections"})
	}

	fixedIDs := make([]string, 0, len(items))
	for _, it := range items {
		fixedIDs = append(fixedIDs, it.ItemID)
		if it.ItemStatus != "active" {
			add(modelExam.Problem{SectionID: it.SectionID, ItemID: it.ItemID,
				Message: fmt.Sprintf("item %s is %s; only active items can be published", label(it), it.ItemStatus)})
		}
	}
	for _, sec := range sections {
		if e.DurationMinutes == nil && sec.DurationMinutes == nil {
			add(modelExam.Problem{SectionID: sec.ID, Message: fmt.Sprintf("section %q needs a duration (or set the exam duration)", sec.Title)})
		}
		if e.Mode == modelExam.ModeLinear && sec.Selection == modelExam.SelectionRandom {
			add(modelExam.Problem{SectionID: sec.ID, Message: fmt.Sprintf("section %q is random but the exam is linear", sec.Title)})
		}
		count := 0
		if sec.Selection == modelExam.SelectionFixed {
			for _, it := range items {
				if it.SectionID == sec.ID {
					count++
				}
			}
		} else {
			for _, ru := range rules {
				if ru.SectionID == sec.ID {
					count++
				}
			}
		}
		if count == 0 {
			what := "items"
			if sec.Selection == modelExam.SelectionRandom {
				what = "rules"
			}
			add(modelExam.Problem{SectionID: sec.ID, Message: fmt.Sprintf("section %q has no %s", sec.Title, what)})
		}
	}

	// Blueprint feasibility over the whole exam: every rule slot needs its own item,
	// hand-picked items are excluded, and rules may compete for the same items.
	if len(rules) > 0 {
		for _, ru := range rules {
			cands, err := s.storer.RuleCandidates(ctx, ru, fixedIDs)
			if err != nil {
				return nil, pl, err
			}
			pl.pools[ru.ID] = cands
		}
		for _, short := range unmetRules(rules, pl.pools) {
			add(modelExam.Problem{SectionID: short.rule.SectionID, RuleID: short.rule.ID, Message: fmt.Sprintf(
				"rule needs %d items but only %d can be assigned (%d match, some are shared with other rules or picked by hand)",
				short.rule.ItemCount, short.assigned, len(pl.pools[short.rule.ID]))})
		}
	}
	if problems == nil {
		problems = []modelExam.Problem{}
	}
	return problems, pl, nil
}

type shortage struct {
	rule     modelExam.RuleEntity
	assigned int
}

// unmetRules solves a bipartite matching between rule slots (item_count per rule) and
// candidate items with Kuhn's augmenting paths, and returns the rules left short.
// Greedy assignment is not enough: overlapping rules can fail greedily although a
// valid assignment exists.
func unmetRules(rules []modelExam.RuleEntity, pools map[string][]Candidate) []shortage {
	type slot struct{ rule int }
	var slots []slot
	for i, ru := range rules {
		for range ru.ItemCount {
			slots = append(slots, slot{rule: i})
		}
	}
	owner := map[string]int{} // item id → slot index
	var try func(si int, seen map[string]bool) bool
	try = func(si int, seen map[string]bool) bool {
		for _, c := range pools[rules[slots[si].rule].ID] {
			if seen[c.ItemID] {
				continue
			}
			seen[c.ItemID] = true
			cur, taken := owner[c.ItemID]
			if !taken || try(cur, seen) {
				owner[c.ItemID] = si
				return true
			}
		}
		return false
	}
	assigned := make([]int, len(rules))
	for si := range slots {
		if try(si, map[string]bool{}) {
			assigned[slots[si].rule]++
		}
	}
	// augmenting paths may move items between slots; recount from the final owners
	for i := range assigned {
		assigned[i] = 0
	}
	for _, si := range owner {
		assigned[slots[si].rule]++
	}
	var out []shortage
	for i, ru := range rules {
		if assigned[i] < ru.ItemCount {
			out = append(out, shortage{rule: ru, assigned: assigned[i]})
		}
	}
	return out
}

// problemFields turns problems into a validation error body: {"problems[0]": "..."}.
func problemFields(problems []modelExam.Problem) map[string]string {
	out := make(map[string]string, len(problems))
	for i, pr := range problems {
		out[fmt.Sprintf("problems[%d]", i)] = pr.Message
	}
	return out
}

func label(it SectionItemRow) string {
	if it.Code != nil {
		return *it.Code
	}
	return it.ItemID
}
