package attempt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
)

// ItemContent is the immutable part of an attempt item: its item version and stimulus.
// Item versions never change once an exam pins them, so this is cached without
// invalidation (DOCS/PLAN/10-BINARY-TUNGGAL.md §2.7).
type ItemContent struct {
	VersionID       string  `json:"-"`
	Type            string  `json:"type"`
	Content         []byte  `json:"content"`
	AnswerKey       []byte  `json:"answer_key"`
	Scoring         []byte  `json:"scoring"`
	Explanation     []byte  `json:"explanation"`
	StimulusID      *string `json:"stimulus_id"`
	StimulusTitle   *string `json:"stimulus_title"`
	StimulusContent []byte  `json:"stimulus_content"`
}

// warmTimeout bounds one background warm-up (large pools load in one query).
const warmTimeout = time.Minute

// contentKey includes the tenant: the cache sits outside row-level security.
func contentKey(tenantID, versionID string) string {
	return "item-content:v1:" + tenantID + ":" + versionID
}

// items returns the attempt's items, in position order, joined with their content.
// Must run inside the attempt's tenant transaction.
func (s *AttemptService) items(ctx context.Context, a modelAttempt.AttemptEntity) ([]ItemRow, error) {
	rows, err := s.storer.ListAttemptItems(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ItemVersionID)
	}
	contents, err := s.contents(ctx, a.TenantID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ItemRow, len(rows))
	for i, r := range rows {
		c, ok := contents[r.ItemVersionID]
		if !ok {
			return nil, fmt.Errorf("attempt item %s: item version %s not found", r.ID, r.ItemVersionID)
		}
		out[i] = ItemRow{AttemptItemEntity: r, Type: c.Type, Content: c.Content, AnswerKey: c.AnswerKey,
			Scoring: c.Scoring, Explanation: c.Explanation, StimulusID: c.StimulusID,
			StimulusTitle: c.StimulusTitle, StimulusContent: c.StimulusContent}
	}
	return out, nil
}

// contents returns the content of each version id: from the cache when possible, the
// rest from the database (one query), which then fills the cache. A failing cache only
// costs speed: it is logged and the database answers. The ids must come from rows read
// inside the tenant transaction (attempt_items, exam pools), never from a request.
func (s *AttemptService) contents(ctx context.Context, tenantID string, ids []string) (map[string]ItemContent, error) {
	ids = uniqueSorted(ids)
	out := make(map[string]ItemContent, len(ids))
	missing := ids
	if s.content != nil && len(ids) > 0 {
		keys := make([]string, len(ids))
		for i, id := range ids {
			keys[i] = contentKey(tenantID, id)
		}
		hits, err := s.content.GetMulti(ctx, keys)
		if err != nil {
			s.logger.WarnContext(ctx, "item content cache read failed", slog.String("error", err.Error()))
		}
		missing = nil
		for i, id := range ids {
			var c ItemContent
			if b, ok := hits[keys[i]]; ok && json.Unmarshal(b, &c) == nil {
				c.VersionID = id
				out[id] = c
				continue
			}
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	loaded, err := s.loadContents(ctx, tenantID, missing)
	if err != nil {
		return nil, err
	}
	for id, c := range loaded {
		out[id] = c
	}
	return out, nil
}

// loadContents reads versions from the database and stores them in the cache. Requests
// missing the same versions at once (a whole room pressing "start") share one query.
func (s *AttemptService) loadContents(ctx context.Context, tenantID string, ids []string) (map[string]ItemContent, error) {
	sum := sha256.Sum256([]byte(tenantID + ":" + strings.Join(ids, ",")))
	v, err, shared := s.flight.Do(hex.EncodeToString(sum[:]), func() (any, error) {
		return s.loadAndStore(ctx, tenantID, ids)
	})
	if err != nil && shared {
		// the shared query ran in another request's transaction; its failure is not ours
		return s.loadAndStore(ctx, tenantID, ids)
	}
	if err != nil {
		return nil, err
	}
	return v.(map[string]ItemContent), nil
}

func (s *AttemptService) loadAndStore(ctx context.Context, tenantID string, ids []string) (map[string]ItemContent, error) {
	rows, err := s.storer.ItemContents(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ItemContent, len(rows))
	put := make(map[string][]byte, len(rows))
	for _, c := range rows {
		out[c.VersionID] = c
		if s.content == nil {
			continue
		}
		b, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("encode item content: %w", err)
		}
		put[contentKey(tenantID, c.VersionID)] = b
	}
	if s.content != nil && len(put) > 0 {
		if err := s.content.SetMulti(ctx, put); err != nil {
			s.logger.WarnContext(ctx, "item content cache write failed", slog.String("error", err.Error()))
		}
	}
	return out, nil
}

// WarmExam loads the exam's plan (sections and selection specs) and every item version
// it can deliver (hand-picked items and whole random pools) into the cache in the
// background, so participants starting together read memory instead of the database. It returns at once; failures are only logged.
// Call it after the caller's transaction (it opens its own).
func (s *AttemptService) WarmExam(ctx context.Context, tenantID, examID string) {
	if s.content == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(ctx, warmTimeout)
		defer cancel()
		ctx, span := s.tracer.Start(ctx, "AttemptService.WarmExam", trace.WithAttributes(attribute.String("exam.id", examID)))
		defer span.End()

		var n int
		err := s.tx.WithTenant(ctx, tenantID, func(ctx context.Context) error {
			exam, err := s.storer.FindExam(ctx, examID)
			if err != nil {
				return err
			}
			if _, err := s.plan(ctx, tenantID, exam); err != nil {
				return err
			}
			ids, err := s.storer.ExamVersionIDs(ctx, examID)
			if err != nil {
				return err
			}
			n = len(ids)
			_, err = s.contents(ctx, tenantID, ids)
			return err
		})
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			s.logger.WarnContext(ctx, "item content warm-up failed", slog.String("exam_id", examID), slog.String("error", err.Error()))
			return
		}
		s.logger.InfoContext(ctx, "item content warmed", slog.String("exam_id", examID), slog.Int("versions", n))
	}()
}

// Shutdown waits for running warm-ups to finish, or for ctx to end.
func (s *AttemptService) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("item content warm-up still running"), ctx.Err())
	}
}

func uniqueSorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
