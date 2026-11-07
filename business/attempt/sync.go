package attempt

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
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

// maxSpentPerEvent caps time_spent_ms reported by one ITEM_VIEWED event.
const maxSpentPerEvent = 60 * 60 * 1000

// Sync applies a batch of client events. It is idempotent: an event whose client_event_id
// was stored before counts as a duplicate and changes nothing, so a client may resend a
// batch as often as needed. Answers only overwrite older answers (client_seq).
func (s *AttemptService) Sync(ctx context.Context, p modelAuth.Principal, id string, in modelAttempt.EventsInput, deviceID string, client modelAudit.Client) (modelAttempt.SyncDTO, error) {
	ctx, span := s.tracer.Start(ctx, "AttemptService.Sync", trace.WithAttributes(attribute.String("attempt.id", id), attribute.Int("events", len(in.Events))))
	defer span.End()

	if len(in.Events) > modelAttempt.MaxEventsPerBatch {
		return modelAttempt.SyncDTO{}, &errr.ValidationError{Fields: map[string]string{"events": fmt.Sprintf("max %d per batch", modelAttempt.MaxEventsPerBatch)}}
	}
	var out modelAttempt.SyncDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		a, err := s.own(ctx, p, id, true) // row lock serializes concurrent batches of one attempt
		if err != nil {
			return err
		}
		if err := s.tick(ctx, &a, client); err != nil {
			return err
		}
		out = modelAttempt.SyncDTO{Rejected: []modelAttempt.Rejected{}, Status: a.Status, DeadlineAt: a.DeadlineAt}
		snap, err := decodeSnapshot(a.ConfigSnapshot)
		if err != nil {
			return err
		}
		if a.Status != modelAttempt.StatusInProgress && a.Status != modelAttempt.StatusLocked {
			rejectAll(&out, in.Events, "attempt_closed")
			return nil
		}
		if snap.SecurityProfile == modelExam.SecurityStandard && a.ActiveDeviceID != nil && *a.ActiveDeviceID != deviceID {
			return errDeviceLocked
		}
		// Items and sections are only needed to check answers, flags and views. A heartbeat
		// (empty batch) or other events skip loading them: every open exam client sends one
		// every 15 seconds.
		var items []ItemRow
		byID := map[string]*ItemRow{}
		open := map[string]bool{}
		if slices.ContainsFunc(in.Events, func(ev modelAttempt.ClientEvent) bool { return isItemEvent(ev.Type) }) {
			sections, err := s.storer.ListAttemptSections(ctx, a.ID)
			if err != nil {
				return err
			}
			if items, err = s.items(ctx, a); err != nil {
				return err
			}
			for i := range items {
				byID[items[i].ID] = &items[i]
			}
			for _, sec := range sections {
				open[sec.ID] = sectionState(sec) == stateActive
			}
		}

		events := slices.Clone(in.Events)
		slices.SortStableFunc(events, func(x, y modelAttempt.ClientEvent) int { return x.ClientSeq - y.ClientSeq })
		now := s.now().UTC()
		maxSeq := a.LastEventSeq
		for _, ev := range events {
			reason, item := s.checkEvent(ev, a, snap, byID, open, items, now)
			if reason != "" {
				out.Rejected = append(out.Rejected, modelAttempt.Rejected{ClientEventID: ev.ClientEventID, Reason: reason})
				continue
			}
			var response []byte
			if ev.Type == modelAttempt.EventResponseSaved {
				var bad string
				if response, bad = normalizeResponse(item, ev.Payload); bad != "" {
					out.Rejected = append(out.Rejected, modelAttempt.Rejected{ClientEventID: ev.ClientEventID, Reason: bad})
					continue
				}
			}
			cid, seq := strings.ToLower(ev.ClientEventID), ev.ClientSeq
			e := modelAttempt.EventEntity{
				TenantID: a.TenantID, AttemptID: a.ID, ClientEventID: &cid, ClientSeq: &seq, Type: ev.Type,
				Payload: compactPayload(ev.Payload), ClientTS: ev.ClientTS, ServerTS: now, IP: nonEmpty(client.IP),
			}
			if item != nil {
				e.AttemptItemID = &item.ID
			}
			inserted, err := s.storer.InsertEvent(ctx, &e)
			if err != nil {
				return err
			}
			if !inserted {
				out.Duplicates++
				continue
			}
			if err := s.apply(ctx, a, ev, item, response, now); err != nil {
				return err
			}
			out.Accepted++
			maxSeq = max(maxSeq, ev.ClientSeq)
		}
		a.LastSeenAt, a.LastEventSeq = &now, maxSeq
		if s.board != nil {
			if out.AnnouncementCursor, err = s.board.Cursor(ctx, a.SessionID); err != nil {
				return err
			}
		}
		return s.storer.UpdateAttempt(ctx, &a)
	})
	if err != nil {
		return modelAttempt.SyncDTO{}, fail(span, err)
	}
	out.ServerTime = s.now().UTC()
	return out, nil
}

// isItemEvent reports whether an event targets one attempt item (and so needs the
// attempt's items and sections to be checked).
func isItemEvent(t string) bool {
	return t == modelAttempt.EventResponseSaved || t == modelAttempt.EventFlagSet || t == modelAttempt.EventItemViewed
}

// checkEvent returns a rejection reason ("" = ok) and the item the event targets.
func (s *AttemptService) checkEvent(ev modelAttempt.ClientEvent, a modelAttempt.AttemptEntity, snap snapshot, byID map[string]*ItemRow,
	open map[string]bool, items []ItemRow, now time.Time) (string, *ItemRow) {
	if uuid.Validate(ev.ClientEventID) != nil {
		return "client_event_id must be a UUID", nil
	}
	if !slices.Contains(modelAttempt.ClientEventTypes, ev.Type) {
		return "unknown event type", nil
	}
	if ev.Type == modelAttempt.EventAnnouncementRead {
		var body struct {
			AnnouncementID string `json:"announcement_id"`
		}
		if json.Unmarshal(ev.Payload, &body) != nil || uuid.Validate(body.AnnouncementID) != nil {
			return "payload must be {\"announcement_id\": uuid}", nil
		}
		return "", nil // not an answer: accepted even after the deadline
	}
	// after the deadline only events recorded (client_ts) before it are accepted; tick has
	// already closed attempts beyond the grace period
	if now.After(a.DeadlineAt) && (ev.ClientTS == nil || ev.ClientTS.After(a.DeadlineAt)) {
		return "after_deadline", nil
	}
	if ev.Type == modelAttempt.EventMediaPlayed {
		var body struct {
			MediaID string `json:"media_id"`
			PlayNo  int    `json:"play_no"`
		}
		if json.Unmarshal(ev.Payload, &body) != nil || uuid.Validate(body.MediaID) != nil || body.PlayNo < 1 || body.PlayNo > 1000 {
			return "payload must be {\"media_id\": uuid, \"play_no\": 1-1000}", nil
		}
		// Recorded even past the limit: the clip already played on the device, and the
		// count is what the proctor needs to see.
		return "", nil
	}
	if !isItemEvent(ev.Type) {
		return "", nil
	}
	item := byID[strings.ToLower(ev.AttemptItemID)]
	if item == nil {
		return "unknown attempt_item_id", nil
	}
	if !open[item.AttemptSectionID] {
		return "section_closed", nil
	}
	if ev.Type == modelAttempt.EventItemViewed {
		return "", item
	}
	if a.Status == modelAttempt.StatusLocked {
		return "locked", nil
	}
	if snap.Navigation == string(modelExam.NavigationForwardOnly) {
		for _, other := range items {
			if other.AttemptSectionID == item.AttemptSectionID && other.Position > item.Position && other.VisitCount > 0 {
				return "forward_only", nil
			}
		}
	}
	return "", item
}

// apply writes the effect of an accepted event.
func (s *AttemptService) apply(ctx context.Context, a modelAttempt.AttemptEntity, ev modelAttempt.ClientEvent, item *ItemRow, response []byte, now time.Time) error {
	switch ev.Type {
	case modelAttempt.EventAnnouncementRead:
		if s.board == nil {
			return nil
		}
		var body struct {
			AnnouncementID string `json:"announcement_id"`
		}
		_ = json.Unmarshal(ev.Payload, &body) // checked in checkEvent
		at := now
		if ev.ClientTS != nil && ev.ClientTS.Before(now) {
			at = ev.ClientTS.UTC()
		}
		return s.board.MarkRead(ctx, strings.ToLower(body.AnnouncementID), a.UserID, at)
	case modelAttempt.EventResponseSaved:
		at := now
		if ev.ClientTS != nil && ev.ClientTS.Before(now) {
			at = ev.ClientTS.UTC()
		}
		return s.storer.SaveResponse(ctx, item.ID, response, ev.ClientSeq, at)
	case modelAttempt.EventFlagSet:
		var body struct {
			Flagged bool `json:"flagged"`
		}
		_ = json.Unmarshal(ev.Payload, &body)
		return s.storer.SetFlag(ctx, item.ID, body.Flagged)
	case modelAttempt.EventItemViewed:
		var body struct {
			TimeSpentMs int `json:"time_spent_ms"`
		}
		_ = json.Unmarshal(ev.Payload, &body)
		item.VisitCount++ // forward-only checks later in the same batch see the visit
		return s.storer.AddVisit(ctx, item.ID, min(max(body.TimeSpentMs, 0), maxSpentPerEvent))
	}
	return nil
}

// normalizeResponse validates {"response": ...} for the item type and returns canonical
// JSON (nil = answer cleared) or a rejection reason.
func normalizeResponse(item *ItemRow, payload json.RawMessage) ([]byte, string) {
	var body struct {
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return nil, "payload must be {\"response\": ...}"
	}
	if len(body.Response) == 0 || string(body.Response) == "null" {
		return nil, ""
	}
	var r responseBody
	if json.Unmarshal(body.Response, &r) != nil {
		return nil, "invalid response"
	}
	opts := item.OptionOrder
	switch item.Type {
	case typeMCQSingle:
		var sel *string
		if json.Unmarshal(r.Selected, &sel) != nil {
			return nil, "selected must be an option id"
		}
		if sel == nil {
			return nil, ""
		}
		if !slices.Contains(opts, *sel) {
			return nil, "unknown option"
		}
		return mustJSON(map[string]string{"selected": *sel}), ""
	case typeMCQMulti:
		var sel []string
		if json.Unmarshal(r.Selected, &sel) != nil {
			return nil, "selected must be a list of option ids"
		}
		sel = uniq(sel)
		for _, o := range sel {
			if !slices.Contains(opts, o) {
				return nil, "unknown option"
			}
		}
		if len(sel) == 0 {
			return nil, ""
		}
		return mustJSON(map[string][]string{"selected": sel}), ""
	case typeTrueFalse:
		if r.Value == nil {
			return nil, ""
		}
		return mustJSON(map[string]bool{"value": *r.Value}), ""
	case typeShortAnswer, typeEssay:
		if r.Text == nil || strings.TrimSpace(*r.Text) == "" {
			return nil, ""
		}
		limit := modelAttempt.MaxShortAnswer
		if item.Type == typeEssay {
			limit = modelAttempt.MaxEssayChars
		}
		if utf8.RuneCountInString(*r.Text) > limit {
			return nil, fmt.Sprintf("text max %d characters", limit)
		}
		return mustJSON(map[string]string{"text": *r.Text}), ""
	}
	return nil, "unsupported item type"
}

func compactPayload(p json.RawMessage) []byte {
	if len(p) == 0 || !json.Valid(p) {
		return []byte("{}")
	}
	if len(p) > 64<<10 {
		return []byte(`{"truncated":true}`)
	}
	return p
}

func rejectAll(out *modelAttempt.SyncDTO, events []modelAttempt.ClientEvent, reason string) {
	for _, ev := range events {
		out.Rejected = append(out.Rejected, modelAttempt.Rejected{ClientEventID: ev.ClientEventID, Reason: reason})
	}
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }
