package attempt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	modelExam "doexam/models/exam"
)

// examPlan is what starting an attempt reads from a published exam: its sections and
// the selection specs (fixed items, random rules and their pools). The structure of a
// published exam is frozen and republishing changes published_at, so the plan is
// cached under that version without invalidation (DOCS/PLAN/10-BINARY-TUNGGAL.md §2.7).
type examPlan struct {
	Sections []modelExam.SectionEntity `json:"sections"`
	Specs    []sectionSpec             `json:"specs"`
}

// planKey is empty for an exam that is not published (nothing to pin a version to).
func planKey(tenantID string, exam modelExam.ExamEntity) string {
	if exam.PublishedAt == nil {
		return ""
	}
	return fmt.Sprintf("exam-plan:v1:%s:%s:%d", tenantID, exam.ID, exam.PublishedAt.UnixNano())
}

// plan returns the exam's plan: from the cache when possible, else from the database
// (requests missing it at once share one read), which then fills the cache. Must run
// inside the tenant transaction; a failing cache only costs speed.
func (s *AttemptService) plan(ctx context.Context, tenantID string, exam modelExam.ExamEntity) (examPlan, error) {
	key := planKey(tenantID, exam)
	if key == "" || s.content == nil {
		return s.loadPlan(ctx, exam.ID)
	}
	hits, err := s.content.GetMulti(ctx, []string{key})
	if err != nil {
		s.logger.WarnContext(ctx, "exam plan cache read failed", slog.String("error", err.Error()))
	}
	if b, ok := hits[key]; ok {
		var pl examPlan
		if json.Unmarshal(b, &pl) == nil {
			return pl, nil
		}
	}
	v, err, shared := s.flight.Do(key, func() (any, error) {
		pl, err := s.loadPlan(ctx, exam.ID)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(pl)
		if err != nil {
			return nil, fmt.Errorf("encode exam plan: %w", err)
		}
		if err := s.content.SetMulti(ctx, map[string][]byte{key: b}); err != nil {
			s.logger.WarnContext(ctx, "exam plan cache write failed", slog.String("error", err.Error()))
		}
		return pl, nil
	})
	if err != nil && shared {
		// the shared read ran in another request's transaction; its failure is not ours
		return s.loadPlan(ctx, exam.ID)
	}
	if err != nil {
		return examPlan{}, err
	}
	return v.(examPlan), nil
}

func (s *AttemptService) loadPlan(ctx context.Context, examID string) (examPlan, error) {
	sections, err := s.storer.ListSections(ctx, examID)
	if err != nil {
		return examPlan{}, err
	}
	specs, err := s.specs(ctx, examID, sections)
	if err != nil {
		return examPlan{}, err
	}
	return examPlan{Sections: sections, Specs: specs}, nil
}
