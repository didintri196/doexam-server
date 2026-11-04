package itembank

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
)

// previewRunes is the length of the stem preview in item lists.
const previewRunes = 160

var errItemNotFound = errr.WithMessage(errr.ErrNotFound, "item not found")

// ListItems returns one page of a bank's items (current versions, no answer keys).
func (s *ItemBankService) ListItems(ctx context.Context, p modelAuth.Principal, bankID string, f modelBank.ItemFilter) ([]modelBank.ItemSummaryDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ListItems", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	v := errr.Validation{}
	v.Check(f.Status == "" || f.Status == modelBank.StatusDraft || f.Status == modelBank.StatusActive || f.Status == modelBank.StatusRetired,
		"status", "must be draft, active or retired")
	v.Check(f.CompetencyID == "" || uuid.Validate(f.CompetencyID) == nil, "competency_id", "must be a UUID")
	if err := v.Err(); err != nil {
		return nil, 0, err
	}
	f.Tag = strings.ToLower(strings.TrimSpace(f.Tag))

	var (
		out   []modelBank.ItemSummaryDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelRead); err != nil {
			return err
		}
		rows, n, err := s.storer.ListItems(ctx, bankID, f)
		if err != nil {
			return err
		}
		out = make([]modelBank.ItemSummaryDTO, 0, len(rows))
		for _, r := range rows {
			tags := []string(r.Tags)
			if tags == nil {
				tags = []string{}
			}
			out = append(out, modelBank.ItemSummaryDTO{
				ID: r.ID, Code: r.Code, Status: r.Status, Type: r.Type, VersionNo: r.VersionNo,
				StemPreview: preview(r.SearchText), Difficulty: r.Difficulty, Tags: tags,
				CompetencyID: r.CompetencyID, Locked: r.LockedAt != nil, UpdatedAt: r.UpdatedAt,
			})
		}
		total = n
		return nil
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	return out, total, nil
}

// GetItem returns an item with its current version, including the answer key.
func (s *ItemBankService) GetItem(ctx context.Context, p modelAuth.Principal, id string) (modelBank.ItemDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.GetItem", trace.WithAttributes(attribute.String("item.id", id)))
	defer span.End()

	var out modelBank.ItemDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, _, err := s.loadItem(ctx, p, id, levelRead, false)
		if err != nil {
			return err
		}
		cur, err := s.storer.FindVersion(ctx, *it.CurrentVersionID)
		if err != nil {
			return err
		}
		out, err = s.toItemDTO(ctx, it, cur)
		return err
	})
	if err != nil {
		return modelBank.ItemDTO{}, fail(span, err)
	}
	return out, nil
}

// ListVersions returns the version history of an item, newest first.
func (s *ItemBankService) ListVersions(ctx context.Context, p modelAuth.Principal, id string) ([]modelBank.VersionSummaryDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ListVersions", trace.WithAttributes(attribute.String("item.id", id)))
	defer span.End()

	var out []modelBank.VersionSummaryDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, _, err := s.loadItem(ctx, p, id, levelRead, false)
		if err != nil {
			return err
		}
		versions, err := s.storer.ListVersions(ctx, id)
		if err != nil {
			return err
		}
		out = make([]modelBank.VersionSummaryDTO, 0, len(versions))
		for _, v := range versions {
			out = append(out, modelBank.VersionSummaryDTO{
				ID: v.ID, VersionNo: v.VersionNo, Type: v.Type, Locked: v.LockedAt != nil,
				Current: it.CurrentVersionID != nil && *it.CurrentVersionID == v.ID, CreatedAt: v.CreatedAt,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// CreateItem adds an item (status draft) with its first version.
func (s *ItemBankService) CreateItem(ctx context.Context, p modelAuth.Principal, bankID string, in modelBank.ItemInput, client modelAudit.Client) (modelBank.ItemDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.CreateItem", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	it, ver, err := s.prepareItem(p, bankID, &in)
	if err != nil {
		return modelBank.ItemDTO{}, fail(span, err)
	}
	var out modelBank.ItemDTO
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		bank, _, err := s.bankAccess(ctx, p, bankID, levelWrite)
		if err != nil {
			return err
		}
		out, err = s.insertItem(ctx, p, bank, in, it, ver, client)
		return err
	})
	if err != nil {
		return modelBank.ItemDTO{}, fail(span, err)
	}
	return out, nil
}

// prepareItem validates a new item and builds its rows (no database access).
func (s *ItemBankService) prepareItem(p modelAuth.Principal, bankID string, in *modelBank.ItemInput) (modelBank.ItemEntity, modelBank.ItemVersionEntity, error) {
	v := errr.Validation{}
	normalizeMeta(&in.VersionInput, v)
	body := normalizeBody(in.Type, in.Content, in.AnswerKey, in.Scoring, v)
	code := normalizeCode(in.Code, v)
	if err := v.Err(); err != nil {
		return modelBank.ItemEntity{}, modelBank.ItemVersionEntity{}, err
	}
	now := s.now().UTC()
	it := modelBank.ItemEntity{
		ID: newID(), TenantID: p.TenantID, BankID: bankID, Code: code, Status: modelBank.StatusDraft,
		CreatedBy: &p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	ver := modelBank.ItemVersionEntity{
		ID: newID(), TenantID: p.TenantID, ItemID: it.ID, VersionNo: 1, DefaultPoints: 1,
		Tags: []string{}, CreatedBy: &p.UserID, CreatedAt: now,
	}
	applyBody(&ver, body)
	applyMeta(&ver, in.VersionInput)
	it.CurrentVersionID = &ver.ID
	return it, ver, nil
}

// insertItem resolves references, checks media and stores a prepared item with its audit
// entry. It runs inside the caller's tenant transaction after the bank access check.
func (s *ItemBankService) insertItem(ctx context.Context, p modelAuth.Principal, bank modelBank.BankEntity, in modelBank.ItemInput,
	it modelBank.ItemEntity, ver modelBank.ItemVersionEntity, client modelAudit.Client) (modelBank.ItemDTO, error) {
	if err := s.resolveRefs(ctx, bank, in.VersionInput, &ver); err != nil {
		return modelBank.ItemDTO{}, err
	}
	if err := s.checkItemMedia(ctx, ver); err != nil {
		return modelBank.ItemDTO{}, err
	}
	if err := s.storer.CreateItem(ctx, &it, &ver); err != nil {
		return modelBank.ItemDTO{}, err
	}
	out, err := s.toItemDTO(ctx, it, ver)
	if err != nil {
		return modelBank.ItemDTO{}, err
	}
	return out, s.audit(ctx, p, client, modelAudit.ActionItemCreate, "item", it.ID, nil, out)
}

// UpdateItem changes an item. Versioned fields (type, content, answer_key, scoring,
// stimulus) of a version that is already locked by an exam go into a NEW version;
// otherwise the current version is updated in place. Metadata (tags, difficulty,
// points, competency, explanation), code and status never create a version.
func (s *ItemBankService) UpdateItem(ctx context.Context, p modelAuth.Principal, id string, in modelBank.ItemUpdateInput, client modelAudit.Client) (modelBank.ItemDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.UpdateItem", trace.WithAttributes(attribute.String("item.id", id)))
	defer span.End()

	var out modelBank.ItemDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, bank, err := s.loadItem(ctx, p, id, levelWrite, true)
		if err != nil {
			return err
		}
		cur, err := s.storer.FindVersion(ctx, *it.CurrentVersionID)
		if err != nil {
			return err
		}
		before, err := s.toItemDTO(ctx, it, cur)
		if err != nil {
			return err
		}

		v := errr.Validation{}
		normalizeMeta(&in.VersionInput, v)
		next := cur
		changed := false
		if in.Type != "" || in.Content != nil || in.AnswerKey != nil || in.Scoring != nil {
			t := cur.Type
			if in.Type != "" {
				t = in.Type
			}
			body := normalizeBody(t, pick(in.Content, cur.Content), pick(in.AnswerKey, cur.AnswerKey), pick(in.Scoring, cur.Scoring), v)
			old := normalizeBody(cur.Type, cur.Content, cur.AnswerKey, cur.Scoring, errr.Validation{})
			if !body.equal(old) {
				applyBody(&next, body)
				changed = true
			}
		}
		if in.Code != nil {
			it.Code = normalizeCode(in.Code, v)
		}
		if in.Status != nil {
			st := *in.Status
			v.Check(st == modelBank.StatusDraft || st == modelBank.StatusActive || st == modelBank.StatusRetired,
				"status", "must be draft, active or retired")
			it.Status = st
		}
		if err := v.Err(); err != nil {
			return err
		}
		applyMeta(&next, in.VersionInput)
		prevStimulus := next.StimulusVersionID
		if err := s.resolveRefs(ctx, bank, in.VersionInput, &next); err != nil {
			return err
		}
		if !samePtr(prevStimulus, next.StimulusVersionID) {
			changed = true
		}
		if err := s.checkItemMedia(ctx, next); err != nil {
			return err
		}

		if changed && cur.LockedAt != nil {
			n, err := s.storer.MaxVersionNo(ctx, id)
			if err != nil {
				return err
			}
			next.ID, next.VersionNo, next.LockedAt = newID(), n+1, nil
			next.CreatedBy, next.CreatedAt = &p.UserID, s.now().UTC()
			if err := s.storer.CreateVersion(ctx, &next); err != nil {
				return err
			}
			it.CurrentVersionID = &next.ID
		} else if err := s.storer.UpdateVersion(ctx, &next); err != nil {
			return err
		}
		if err := s.storer.UpdateItem(ctx, &it); err != nil {
			return err
		}
		it.UpdatedAt = s.now().UTC()
		if out, err = s.toItemDTO(ctx, it, next); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionItemUpdate, "item", id, before, out)
	})
	if err != nil {
		return modelBank.ItemDTO{}, fail(span, err)
	}
	return out, nil
}

// DeleteItem soft-deletes an item that was never used by an exam. Used items must be retired instead.
func (s *ItemBankService) DeleteItem(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.DeleteItem", trace.WithAttributes(attribute.String("item.id", id)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		it, _, err := s.loadItem(ctx, p, id, levelWrite, true)
		if err != nil {
			return err
		}
		locked, err := s.storer.HasLockedVersion(ctx, id)
		if err != nil {
			return err
		}
		if locked {
			return errr.WithMessage(errr.ErrConflict, "item was used in an exam; retire it instead")
		}
		if err := s.storer.SoftDeleteItem(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionItemDelete, "item", id,
			map[string]any{"code": it.Code, "bank_id": it.BankID, "version_id": it.CurrentVersionID}, nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// loadItem loads an item and checks bank access. Items in banks the caller cannot read are "not found".
func (s *ItemBankService) loadItem(ctx context.Context, p modelAuth.Principal, id string, lvl level, forUpdate bool) (modelBank.ItemEntity, modelBank.BankEntity, error) {
	if uuid.Validate(id) != nil {
		return modelBank.ItemEntity{}, modelBank.BankEntity{}, errItemNotFound
	}
	it, err := s.storer.FindItem(ctx, id, forUpdate)
	if err != nil {
		return modelBank.ItemEntity{}, modelBank.BankEntity{}, err
	}
	bank, _, err := s.bankAccess(ctx, p, it.BankID, lvl)
	if err != nil {
		if errors.Is(err, errBankNotFound) {
			err = errItemNotFound
		}
		return modelBank.ItemEntity{}, modelBank.BankEntity{}, err
	}
	return it, bank, nil
}

// resolveRefs applies competency_id and stimulus_id from in to ver, checking they belong
// to the bank's subject / the bank. A stimulus that is already attached keeps its pinned version.
func (s *ItemBankService) resolveRefs(ctx context.Context, bank modelBank.BankEntity, in modelBank.VersionInput, ver *modelBank.ItemVersionEntity) error {
	if in.CompetencyID != nil {
		ver.CompetencyID = nil
		if c := strings.TrimSpace(*in.CompetencyID); c != "" {
			ok := uuid.Validate(c) == nil
			if ok {
				var err error
				if ok, err = s.storer.CompetencyInSubject(ctx, c, bank.SubjectID); err != nil {
					return err
				}
			}
			if !ok {
				return &errr.ValidationError{Fields: map[string]string{"competency_id": "competency not found in this bank's subject"}}
			}
			ver.CompetencyID = &c
		}
	}
	if in.StimulusID != nil {
		sid := strings.TrimSpace(*in.StimulusID)
		if sid == "" {
			ver.StimulusVersionID = nil
			return nil
		}
		invalid := &errr.ValidationError{Fields: map[string]string{"stimulus_id": "stimulus not found in this bank"}}
		if uuid.Validate(sid) != nil {
			return invalid
		}
		if ver.StimulusVersionID != nil {
			pinned, err := s.storer.FindStimulusVersion(ctx, *ver.StimulusVersionID)
			if err != nil {
				return err
			}
			if pinned.ID == sid {
				return nil // same stimulus: keep the pinned version
			}
		}
		st, err := s.storer.FindStimulus(ctx, sid)
		if errors.Is(err, errr.ErrNotFound) || (err == nil && st.BankID != bank.ID) {
			return invalid
		}
		if err != nil {
			return err
		}
		ver.StimulusVersionID = st.CurrentVersionID
	}
	return nil
}

func (s *ItemBankService) toItemDTO(ctx context.Context, it modelBank.ItemEntity, v modelBank.ItemVersionEntity) (modelBank.ItemDTO, error) {
	tags := []string(v.Tags)
	if tags == nil {
		tags = []string{}
	}
	key := json.RawMessage("null")
	if len(v.AnswerKey) > 0 {
		key = v.AnswerKey
	}
	dto := modelBank.ItemDTO{
		ID: it.ID, BankID: it.BankID, Code: it.Code, Status: it.Status, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
		Version: modelBank.VersionDTO{
			ID: v.ID, VersionNo: v.VersionNo, Type: v.Type, Content: v.Content, AnswerKey: key, Scoring: v.Scoring,
			DefaultPoints: v.DefaultPoints, Difficulty: v.Difficulty, CognitiveLevel: v.CognitiveLevel,
			Tags: tags, CompetencyID: v.CompetencyID, Locked: v.LockedAt != nil, CreatedAt: v.CreatedAt,
		},
	}
	if len(v.Explanation) > 0 {
		var e modelBank.Explanation
		if json.Unmarshal(v.Explanation, &e) == nil && e.Body != "" {
			dto.Version.Explanation = &e.Body
		}
	}
	if v.StimulusVersionID != nil {
		st, err := s.storer.FindStimulusVersion(ctx, *v.StimulusVersionID)
		if err != nil {
			return modelBank.ItemDTO{}, err
		}
		dto.Version.Stimulus = &modelBank.StimulusRefDTO{ID: st.ID, Title: st.Title, VersionID: *v.StimulusVersionID, VersionNo: st.VersionNo}
	}
	return dto, nil
}

func applyBody(ver *modelBank.ItemVersionEntity, b canonical) {
	ver.Type, ver.Content, ver.AnswerKey, ver.Scoring = b.Type, b.Content, b.AnswerKey, b.Scoring
	ver.SearchText = optional(b.SearchText)
}

func applyMeta(ver *modelBank.ItemVersionEntity, in modelBank.VersionInput) {
	if in.DefaultPoints != nil {
		ver.DefaultPoints = *in.DefaultPoints
	}
	if in.Difficulty != nil {
		ver.Difficulty = optional(*in.Difficulty)
	}
	if in.CognitiveLevel != nil {
		ver.CognitiveLevel = optional(*in.CognitiveLevel)
	}
	if in.Tags != nil {
		ver.Tags = *in.Tags
	}
	if in.Explanation != nil {
		ver.Explanation = nil
		if *in.Explanation != "" {
			ver.Explanation = mustJSON(modelBank.Explanation{Body: *in.Explanation})
		}
	}
}

func normalizeCode(code *string, v errr.Validation) *string {
	if code == nil {
		return nil
	}
	c := strings.TrimSpace(*code)
	v.Check(len(c) <= 50, "code", "max 50 chars")
	return optional(c)
}

func pick(in json.RawMessage, stored []byte) json.RawMessage {
	if in != nil {
		return in
	}
	return stored
}

func samePtr(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func preview(s *string) string {
	if s == nil {
		return ""
	}
	if utf8.RuneCountInString(*s) <= previewRunes {
		return *s
	}
	r := []rune(*s)
	return string(r[:previewRunes]) + "…"
}

// checkItemMedia checks the media referenced by content, answer key and explanation.
func (s *ItemBankService) checkItemMedia(ctx context.Context, ver modelBank.ItemVersionEntity) error {
	if err := s.checkMedia(ctx, "content", string(ver.Content), string(ver.AnswerKey)); err != nil {
		return err
	}
	return s.checkMedia(ctx, "explanation", string(ver.Explanation))
}
