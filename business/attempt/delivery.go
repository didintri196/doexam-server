package attempt

import (
	"context"
	modelMedia "doexam/models/media"
	"doexam/pkg/richtext"
	"encoding/json"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
)

// Section states in the participant view.
const (
	statePending = "pending"
	stateActive  = "active"
	stateClosed  = "closed"
)

// Get returns the caller's attempt as they take it (no answer keys). Expired attempts
// and sections are closed first, so the view always reflects the server clock.
func (s *AttemptService) Get(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelAttempt.AttemptView, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Get", trace.WithAttributes(attribute.String("attempt.id", id)))
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
		out, err = s.view(ctx, a)
		return err
	})
	if err != nil {
		return modelAttempt.AttemptView{}, fail(span, err)
	}
	return out, nil
}

// view builds the participant view. Items are included only for active sections, and
// only while the attempt is running; answer keys, scoring and explanations never are.
func (s *AttemptService) view(ctx context.Context, a modelAttempt.AttemptEntity) (modelAttempt.AttemptView, error) {
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil {
		return modelAttempt.AttemptView{}, err
	}
	sections, err := s.storer.ListAttemptSections(ctx, a.ID)
	if err != nil {
		return modelAttempt.AttemptView{}, err
	}
	running := a.Status == modelAttempt.StatusInProgress || a.Status == modelAttempt.StatusLocked
	var items []ItemRow
	if running {
		if items, err = s.items(ctx, a); err != nil {
			return modelAttempt.AttemptView{}, err
		}
	}
	out := modelAttempt.AttemptView{
		ID: a.ID, SessionID: a.SessionID, ExamTitle: snap.ExamTitle, Instructions: snap.Instructions, Status: a.Status, Navigation: snap.Navigation,
		SectionMode: snap.SectionMode, SecurityProfile: snap.SecurityProfile, Settings: snap.Settings,
		RequireCamera: snap.RequireCamera, RequireScreen: snap.RequireScreen,
		StartedAt: a.StartedAt, DeadlineAt: a.DeadlineAt, ServerTime: s.now().UTC(), Sections: []modelAttempt.SectionView{},
	}
	for _, sec := range sections {
		meta := sectionMeta(snap, sec.SectionID)
		sv := modelAttempt.SectionView{
			ID: sec.ID, Title: meta.Title, Instructions: meta.Instructions, SortOrder: sec.SortOrder,
			State: sectionState(sec), DeadlineAt: sec.DeadlineAt, AllowRevisit: meta.AllowRevisit, Items: []modelAttempt.ItemView{},
		}
		if sv.DeadlineAt == nil && sv.State != stateClosed {
			sv.DeadlineAt = &a.DeadlineAt
		}
		for _, it := range items {
			if it.AttemptSectionID != sec.ID {
				continue
			}
			sv.ItemCount++
			if sv.State == stateActive {
				sv.Items = append(sv.Items, itemView(it))
			}
		}
		out.Sections = append(out.Sections, sv)
	}
	var html []string
	for _, sec := range out.Sections {
		for _, it := range sec.Items {
			html = append(html, it.Stem)
			for _, o := range it.Options {
				html = append(html, o.Body)
			}
			if it.Stimulus != nil {
				html = append(html, it.Stimulus.Body)
			}
		}
	}
	if out.Media, err = s.mediaRefs(ctx, a.TenantID, html...); err != nil {
		return modelAttempt.AttemptView{}, err
	}
	out.MediaPlays = map[string]int{}
	if running && len(out.Media) > 0 {
		if out.MediaPlays, out.ExtraPlays, err = s.storer.MediaPlays(ctx, a.ID); err != nil {
			return modelAttempt.AttemptView{}, err
		}
	}
	return out, nil
}

// mediaRefs signs links for every data-media-id in html. The exam client downloads them
// when the attempt starts, so pictures stay visible if the network drops.
func (s *AttemptService) mediaRefs(ctx context.Context, tenantID string, html ...string) (map[string]modelMedia.Ref, error) {
	var ids []string
	for _, h := range html {
		ids = append(ids, richtext.MediaIDs(h)...)
	}
	if s.media == nil || len(ids) == 0 {
		return map[string]modelMedia.Ref{}, nil
	}
	return s.media.Refs(ctx, tenantID, ids)
}

// itemView strips an item down to what the participant may see, with options in their order.
func itemView(it ItemRow) modelAttempt.ItemView {
	var c struct {
		Stem    string `json:"stem"`
		Options []struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"options"`
		Input    string `json:"input"`
		MaxWords *int   `json:"max_words"`
	}
	_ = json.Unmarshal(it.Content, &c)
	v := modelAttempt.ItemView{
		ID: it.ID, Position: it.Position, Type: it.Type, Stem: c.Stem, Input: c.Input, MaxWords: c.MaxWords,
		MaxPoints: it.MaxPoints, Response: json.RawMessage("null"), Flagged: it.Flagged,
	}
	if len(it.Response) > 0 {
		v.Response = it.Response
	}
	byID := map[string]string{}
	for _, o := range c.Options {
		byID[o.ID] = o.Body
	}
	for _, id := range it.OptionOrder {
		if body, ok := byID[id]; ok {
			v.Options = append(v.Options, modelAttempt.OptionView{ID: id, Body: body})
		}
	}
	if it.StimulusID != nil {
		var sc struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(it.StimulusContent, &sc)
		v.Stimulus = &modelAttempt.StimulusView{ID: *it.StimulusID, Title: deref2(it.StimulusTitle), Body: sc.Body}
	}
	return v
}

// tick applies the clock: an attempt past its deadline (+ grace) is submitted as a timeout;
// in sequential mode an expired section is closed and the next one started.
// There is no background sweeper (the app role cannot scan across tenants), so every
// path that touches an attempt calls tick first.
func (s *AttemptService) tick(ctx context.Context, a *modelAttempt.AttemptEntity, client modelAudit.Client) error {
	if a.Status != modelAttempt.StatusInProgress && a.Status != modelAttempt.StatusLocked {
		return nil
	}
	now := s.now().UTC()
	if now.After(a.DeadlineAt.Add(syncGrace)) {
		return s.finalize(ctx, a, modelAttempt.ReasonTimeout, nil, client)
	}
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil || snap.SectionMode != modeSequential {
		return err
	}
	sections, err := s.storer.ListAttemptSections(ctx, a.ID)
	if err != nil {
		return err
	}
	for i := range sections {
		sec := &sections[i]
		if sec.StartedAt == nil || sec.ClosedAt != nil || sec.DeadlineAt == nil || !now.After(sec.DeadlineAt.Add(syncGrace)) {
			continue
		}
		if err := s.closeSection(ctx, a, sections, i, client); err != nil {
			return err
		}
		s.metrics.sectionTimedOut(ctx)
	}
	return nil
}

// closeSection closes section i and, in sequential mode, starts the next one.
func (s *AttemptService) closeSection(ctx context.Context, a *modelAttempt.AttemptEntity, sections []modelAttempt.AttemptSectionEntity, i int, client modelAudit.Client) error {
	now := s.now().UTC()
	sec := &sections[i]
	sec.ClosedAt = &now
	if err := s.storer.UpdateAttemptSection(ctx, sec); err != nil {
		return err
	}
	if err := s.event(ctx, *a, modelAttempt.EventSectionClosed, nil, map[string]any{"attempt_section_id": sec.ID}, client); err != nil {
		return err
	}
	snap, err := decodeSnapshot(a.ConfigSnapshot)
	if err != nil || snap.SectionMode != modeSequential {
		return err
	}
	if i+1 >= len(sections) {
		return s.finalize(ctx, a, modelAttempt.ReasonTimeout, nil, client) // last timed section is over
	}
	next := &sections[i+1]
	if next.StartedAt != nil {
		return nil
	}
	next.StartedAt = &now
	next.DeadlineAt = sectionDeadline(now, sectionMeta(snap, next.SectionID), minutes(snap.ExtraMinutes), a.DeadlineAt)
	if err := s.storer.UpdateAttemptSection(ctx, next); err != nil {
		return err
	}
	return s.event(ctx, *a, modelAttempt.EventSectionStarted, nil, map[string]any{"attempt_section_id": next.ID}, client)
}

func sectionState(sec modelAttempt.AttemptSectionEntity) string {
	switch {
	case sec.ClosedAt != nil:
		return stateClosed
	case sec.StartedAt != nil:
		return stateActive
	}
	return statePending
}

func sectionMeta(snap snapshot, sectionID string) snapSection {
	for _, s := range snap.Sections {
		if s.SectionID == sectionID {
			return s
		}
	}
	return snapSection{}
}

func deref2(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
