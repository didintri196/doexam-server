package exam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
)

var validItemTypes = []string{"mcq_single", "mcq_multi", "true_false", "short_answer", "essay"}

// ---- sections ----

// CreateSection appends a section to a draft exam.
func (s *ExamService) CreateSection(ctx context.Context, p modelAuth.Principal, examID string, in modelExam.SectionInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	return s.structure(ctx, p, examID, "ExamService.CreateSection", client, func(ctx context.Context, e modelExam.ExamEntity) error {
		sections, err := s.storer.ListSections(ctx, examID)
		if err != nil {
			return err
		}
		sec := modelExam.SectionEntity{
			ID: newID(), TenantID: p.TenantID, ExamID: examID, SortOrder: nextOrder(len(sections), func(i int) int { return sections[i].SortOrder }),
			Selection: modelExam.SelectionFixed, AllowRevisit: true,
		}
		if e.Mode == modelExam.ModeRandomBlueprint {
			sec.Selection = modelExam.SelectionRandom
		}
		v := errr.Validation{}
		v.Check(in.Title != nil, "title", "required")
		applySectionInput(&sec, e, in, v)
		if err := v.Err(); err != nil {
			return err
		}
		return s.storer.CreateSection(ctx, &sec)
	})
}

// UpdateSection changes a section of a draft exam. Changing selection requires the
// section to be empty (no items and no rules).
func (s *ExamService) UpdateSection(ctx context.Context, p modelAuth.Principal, sectionID string, in modelExam.SectionInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	sec, err := s.sectionOf(ctx, p, sectionID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.UpdateSection", client, func(ctx context.Context, e modelExam.ExamEntity) error {
		if in.Selection != nil && *in.Selection != sec.Selection {
			empty, err := s.sectionEmpty(ctx, e.ID, sec.ID)
			if err != nil {
				return err
			}
			if !empty {
				return &errr.ValidationError{Fields: map[string]string{"selection": "remove the section's items or rules before changing selection"}}
			}
		}
		v := errr.Validation{}
		applySectionInput(&sec, e, in, v)
		if err := v.Err(); err != nil {
			return err
		}
		return s.storer.UpdateSection(ctx, &sec)
	})
}

// DeleteSection removes a section with its items and rules from a draft exam.
func (s *ExamService) DeleteSection(ctx context.Context, p modelAuth.Principal, sectionID string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	sec, err := s.sectionOf(ctx, p, sectionID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.DeleteSection", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		return s.storer.DeleteSection(ctx, sectionID)
	})
}

// OrderSections sets the section order; ids must list every section exactly once.
func (s *ExamService) OrderSections(ctx context.Context, p modelAuth.Principal, examID string, in modelExam.OrderInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	return s.structure(ctx, p, examID, "ExamService.OrderSections", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		sections, err := s.storer.ListSections(ctx, examID)
		if err != nil {
			return err
		}
		have := make([]string, len(sections))
		for i, sec := range sections {
			have[i] = sec.ID
		}
		if err := samePermutation(in.IDs, have); err != nil {
			return err
		}
		for i, id := range in.IDs {
			if err := s.storer.SetSectionOrder(ctx, id, i+1); err != nil {
				return err
			}
		}
		return nil
	})
}

func applySectionInput(sec *modelExam.SectionEntity, e modelExam.ExamEntity, in modelExam.SectionInput, v errr.Validation) {
	if in.Title != nil {
		sec.Title = strings.TrimSpace(*in.Title)
		v.Check(sec.Title != "" && utf8.RuneCountInString(sec.Title) <= 200, "title", "required, max 200 chars")
	}
	if in.Instructions != nil {
		sec.Instructions = richBody(*in.Instructions, "instructions", v)
	}
	if in.DurationMinutes.Set {
		sec.DurationMinutes = in.DurationMinutes.Ptr()
		v.Check(sec.DurationMinutes == nil || (*sec.DurationMinutes >= 1 && *sec.DurationMinutes <= 1440), "duration_minutes", "1-1440 or null")
	}
	if in.Selection != nil {
		sec.Selection = *in.Selection
		v.Check(sec.Selection == modelExam.SelectionFixed || sec.Selection == modelExam.SelectionRandom, "selection", "must be fixed or random")
	}
	v.Check(!(e.Mode == modelExam.ModeLinear && sec.Selection == modelExam.SelectionRandom), "selection", "linear exams only have fixed sections")
	if in.AllowRevisit != nil {
		sec.AllowRevisit = *in.AllowRevisit
	}
	if in.PassingScore.Set {
		sec.PassingScore = roundPtr(in.PassingScore.Ptr())
		v.Check(sec.PassingScore == nil || (*sec.PassingScore >= 0 && *sec.PassingScore <= 100), "passing_score", "0-100 or null")
	}
}

// ---- hand-picked items ----

// AddItems appends items to a fixed section. Items must be in banks the caller can read,
// not retired, and not already used anywhere in the exam.
func (s *ExamService) AddItems(ctx context.Context, p modelAuth.Principal, sectionID string, in modelExam.AddItemsInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	sec, err := s.sectionOf(ctx, p, sectionID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.AddItems", client, func(ctx context.Context, e modelExam.ExamEntity) error {
		if sec.Selection != modelExam.SelectionFixed {
			return &errr.ValidationError{Fields: map[string]string{"items": "random sections use rules, not items"}}
		}
		v := errr.Validation{}
		v.Check(len(in.Items) >= 1 && len(in.Items) <= modelExam.MaxItemsPerRequest, "items", fmt.Sprintf("1-%d items", modelExam.MaxItemsPerRequest))
		ids := make([]string, 0, len(in.Items))
		for i, it := range in.Items {
			id := strings.ToLower(strings.TrimSpace(it.ItemID))
			v.Check(uuid.Validate(id) == nil, fmt.Sprintf("items[%d].item_id", i), "must be a UUID")
			v.Check(!slices.Contains(ids, id), fmt.Sprintf("items[%d].item_id", i), "listed twice")
			v.Check(it.Points == nil || (*it.Points >= 0 && *it.Points <= 10000), fmt.Sprintf("items[%d].points", i), "0-10000 or null")
			ids = append(ids, id)
		}
		if err := v.Err(); err != nil {
			return err
		}

		refs, err := s.storer.FindItems(ctx, ids)
		if err != nil {
			return err
		}
		existing, err := s.storer.ListSectionItems(ctx, e.ID)
		if err != nil {
			return err
		}
		used := map[string]bool{}
		maxOrder := 0
		for _, x := range existing {
			used[x.ItemID] = true
			if x.SectionID == sectionID {
				maxOrder = max(maxOrder, x.SortOrder)
			}
		}
		access := map[string]bool{}
		rows := make([]modelExam.SectionItemEntity, 0, len(ids))
		for i, id := range ids {
			field := fmt.Sprintf("items[%d].item_id", i)
			idx := slices.IndexFunc(refs, func(r ItemRef) bool { return r.ID == id })
			if idx < 0 {
				v.Check(false, field, "item not found")
				continue
			}
			ref := refs[idx]
			if _, seen := access[ref.BankID]; !seen {
				ok, err := s.canReadBank(ctx, p, ref.BankID)
				if err != nil {
					return err
				}
				access[ref.BankID] = ok
			}
			v.Check(access[ref.BankID], field, "item not found") // do not reveal items of other banks
			v.Check(ref.Status != "retired", field, "item is retired")
			v.Check(!used[id], field, "item is already in this exam")
			rows = append(rows, modelExam.SectionItemEntity{
				ID: newID(), TenantID: p.TenantID, SectionID: sectionID, ItemID: id,
				SortOrder: maxOrder + i + 1, Points: roundPtr(in.Items[i].Points), LockOptions: in.Items[i].LockOptions,
			})
		}
		if err := v.Err(); err != nil {
			return err
		}
		return s.storer.CreateSectionItems(ctx, rows)
	})
}

// UpdateSectionItem changes points or lock_options of a hand-picked item.
func (s *ExamService) UpdateSectionItem(ctx context.Context, p modelAuth.Principal, id string, in modelExam.SectionItemUpdateInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	it, sec, err := s.sectionItemOf(ctx, p, id)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.UpdateSectionItem", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		if in.Points.Set {
			it.Points = roundPtr(in.Points.Ptr())
			if it.Points != nil && (*it.Points < 0 || *it.Points > 10000) {
				return &errr.ValidationError{Fields: map[string]string{"points": "0-10000 or null"}}
			}
		}
		if in.LockOptions != nil {
			it.LockOptions = *in.LockOptions
		}
		return s.storer.UpdateSectionItem(ctx, &it)
	})
}

// RemoveSectionItem removes a hand-picked item.
func (s *ExamService) RemoveSectionItem(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	_, sec, err := s.sectionItemOf(ctx, p, id)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.RemoveSectionItem", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		return s.storer.DeleteSectionItem(ctx, id)
	})
}

// OrderSectionItems sets the item order of a section; ids are section-item ids, all of them once.
func (s *ExamService) OrderSectionItems(ctx context.Context, p modelAuth.Principal, sectionID string, in modelExam.OrderInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	sec, err := s.sectionOf(ctx, p, sectionID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.OrderSectionItems", client, func(ctx context.Context, e modelExam.ExamEntity) error {
		all, err := s.storer.ListSectionItems(ctx, e.ID)
		if err != nil {
			return err
		}
		var have []string
		for _, x := range all {
			if x.SectionID == sectionID {
				have = append(have, x.ID)
			}
		}
		if err := samePermutation(in.IDs, have); err != nil {
			return err
		}
		for i, id := range in.IDs {
			if err := s.storer.SetSectionItemOrder(ctx, id, i+1); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- blueprint rules ----

// CreateRule adds a blueprint rule to a random section.
func (s *ExamService) CreateRule(ctx context.Context, p modelAuth.Principal, sectionID string, in modelExam.RuleInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	sec, err := s.sectionOf(ctx, p, sectionID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.CreateRule", client, func(ctx context.Context, e modelExam.ExamEntity) error {
		if sec.Selection != modelExam.SelectionRandom {
			return &errr.ValidationError{Fields: map[string]string{"section": "rules are only for random sections"}}
		}
		rules, err := s.storer.ListRules(ctx, e.ID)
		if err != nil {
			return err
		}
		order := 0
		for _, r := range rules {
			if r.SectionID == sectionID {
				order = max(order, r.SortOrder)
			}
		}
		ru := modelExam.RuleEntity{ID: newID(), TenantID: p.TenantID, SectionID: sectionID, SortOrder: order + 1, TagsAny: []string{}}
		v := errr.Validation{}
		v.Check(in.BankID != nil, "bank_id", "required")
		v.Check(in.ItemCount != nil, "item_count", "required")
		if err := s.applyRuleInput(ctx, p, &ru, in, v); err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			return err
		}
		return s.storer.CreateRule(ctx, &ru)
	})
}

// UpdateRule changes a blueprint rule.
func (s *ExamService) UpdateRule(ctx context.Context, p modelAuth.Principal, id string, in modelExam.RuleInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ru, sec, err := s.ruleOf(ctx, p, id)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.UpdateRule", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		v := errr.Validation{}
		if err := s.applyRuleInput(ctx, p, &ru, in, v); err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			return err
		}
		return s.storer.UpdateRule(ctx, &ru)
	})
}

// DeleteRule removes a blueprint rule.
func (s *ExamService) DeleteRule(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelExam.ExamDTO, error) {
	_, sec, err := s.ruleOf(ctx, p, id)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	return s.structure(ctx, p, sec.ExamID, "ExamService.DeleteRule", client, func(ctx context.Context, _ modelExam.ExamEntity) error {
		return s.storer.DeleteRule(ctx, id)
	})
}

func (s *ExamService) applyRuleInput(ctx context.Context, p modelAuth.Principal, ru *modelExam.RuleEntity, in modelExam.RuleInput, v errr.Validation) error {
	if in.BankID != nil {
		ru.BankID = strings.ToLower(strings.TrimSpace(*in.BankID))
	}
	if in.CompetencyID != nil {
		ru.CompetencyID = optional(strings.ToLower(strings.TrimSpace(*in.CompetencyID)))
	}
	if in.ItemType != nil {
		ru.ItemType = optional(strings.TrimSpace(*in.ItemType))
	}
	if in.Difficulty != nil {
		ru.Difficulty = optional(strings.ToLower(strings.TrimSpace(*in.Difficulty)))
	}
	if in.TagsAny != nil {
		tags := []string{}
		for _, t := range *in.TagsAny {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" && !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
		v.Check(len(tags) <= 20, "tags_any", "max 20 tags")
		ru.TagsAny = tags
	}
	if in.ItemCount != nil {
		ru.ItemCount = *in.ItemCount
	}
	if in.Points.Set {
		ru.Points = roundPtr(in.Points.Ptr())
	}
	v.Check(ru.ItemCount >= 1 && ru.ItemCount <= modelExam.MaxRuleItemCount, "item_count", fmt.Sprintf("1-%d", modelExam.MaxRuleItemCount))
	v.Check(ru.ItemType == nil || slices.Contains(validItemTypes, *ru.ItemType), "item_type", "unknown item type")
	v.Check(ru.Difficulty == nil || slices.Contains([]string{"easy", "medium", "hard"}, *ru.Difficulty), "difficulty", "must be easy, medium or hard")
	v.Check(ru.Points == nil || (*ru.Points >= 0 && *ru.Points <= 10000), "points", "0-10000 or null")
	v.Check(ru.CompetencyID == nil || uuid.Validate(*ru.CompetencyID) == nil, "competency_id", "must be a UUID")
	if uuid.Validate(ru.BankID) != nil {
		v.Check(false, "bank_id", "must be a UUID")
		return nil
	}
	subjectID, err := s.storer.BankSubject(ctx, ru.BankID)
	if err != nil {
		return err
	}
	ok := subjectID != ""
	if ok {
		if ok, err = s.canReadBank(ctx, p, ru.BankID); err != nil {
			return err
		}
	}
	if !ok {
		v.Check(false, "bank_id", "item bank not found")
		return nil
	}
	if ru.CompetencyID != nil && uuid.Validate(*ru.CompetencyID) == nil {
		ok, err := s.storer.CompetencyInSubject(ctx, *ru.CompetencyID, subjectID)
		if err != nil {
			return err
		}
		v.Check(ok, "competency_id", "competency not found in the bank's subject")
	}
	return nil
}

// ---- shared ----

// structure runs a change to the structure of a draft exam and returns the updated exam.
func (s *ExamService) structure(ctx context.Context, p modelAuth.Principal, examID, spanName string, client modelAudit.Client,
	fn func(ctx context.Context, e modelExam.ExamEntity) error) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, spanName, trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForEdit(ctx, p, examID)
		if err != nil {
			return err
		}
		if e.Status != modelExam.StatusDraft {
			return errNotDraft
		}
		if err := fn(ctx, e); err != nil {
			return err
		}
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamStructure, examID, nil,
			map[string]any{"change": strings.TrimPrefix(spanName, "ExamService."), "sections": out.Sections})
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

func (s *ExamService) sectionOf(ctx context.Context, p modelAuth.Principal, id string) (modelExam.SectionEntity, error) {
	notFound := errr.WithMessage(errr.ErrNotFound, "section not found")
	if uuid.Validate(id) != nil || !canRead(p) {
		return modelExam.SectionEntity{}, notFound
	}
	var sec modelExam.SectionEntity
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		sec, err = s.storer.FindSection(ctx, id)
		return err
	})
	return sec, err
}

func (s *ExamService) sectionItemOf(ctx context.Context, p modelAuth.Principal, id string) (modelExam.SectionItemEntity, modelExam.SectionEntity, error) {
	notFound := errr.WithMessage(errr.ErrNotFound, "section item not found")
	if uuid.Validate(id) != nil || !canRead(p) {
		return modelExam.SectionItemEntity{}, modelExam.SectionEntity{}, notFound
	}
	var (
		it  modelExam.SectionItemEntity
		sec modelExam.SectionEntity
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		if it, err = s.storer.FindSectionItem(ctx, id); err != nil {
			return err
		}
		sec, err = s.storer.FindSection(ctx, it.SectionID)
		return err
	})
	return it, sec, err
}

func (s *ExamService) ruleOf(ctx context.Context, p modelAuth.Principal, id string) (modelExam.RuleEntity, modelExam.SectionEntity, error) {
	notFound := errr.WithMessage(errr.ErrNotFound, "rule not found")
	if uuid.Validate(id) != nil || !canRead(p) {
		return modelExam.RuleEntity{}, modelExam.SectionEntity{}, notFound
	}
	var (
		ru  modelExam.RuleEntity
		sec modelExam.SectionEntity
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		if ru, err = s.storer.FindRule(ctx, id); err != nil {
			return err
		}
		sec, err = s.storer.FindSection(ctx, ru.SectionID)
		return err
	})
	return ru, sec, err
}

func (s *ExamService) sectionEmpty(ctx context.Context, examID, sectionID string) (bool, error) {
	items, err := s.storer.ListSectionItems(ctx, examID)
	if err != nil {
		return false, err
	}
	rules, err := s.storer.ListRules(ctx, examID)
	if err != nil {
		return false, err
	}
	return !slices.ContainsFunc(items, func(x SectionItemRow) bool { return x.SectionID == sectionID }) &&
		!slices.ContainsFunc(rules, func(r modelExam.RuleEntity) bool { return r.SectionID == sectionID }), nil
}

// canReadBank mirrors the item bank rule: tenant.manage, or membership plus item.read/item.write.
func (s *ExamService) canReadBank(ctx context.Context, p modelAuth.Principal, bankID string) (bool, error) {
	if p.Has(modelAuth.PermTenantManage) {
		return true, nil
	}
	if !p.Has(modelAuth.PermItemRead) && !p.Has(modelAuth.PermItemWrite) {
		return false, nil
	}
	access, err := s.storer.BankMemberAccess(ctx, bankID, p.UserID)
	return access != "", err
}

// detail assembles the full ExamDTO. Must run in a tenant transaction.
func (s *ExamService) detail(ctx context.Context, p modelAuth.Principal, e modelExam.ExamEntity) (modelExam.ExamDTO, error) {
	sections, err := s.storer.ListSections(ctx, e.ID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	items, err := s.storer.ListSectionItems(ctx, e.ID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	rules, err := s.storer.ListRules(ctx, e.ID)
	if err != nil {
		return modelExam.ExamDTO{}, err
	}
	fixedIDs := make([]string, len(items))
	for i, it := range items {
		fixedIDs[i] = it.ItemID
	}

	dto := modelExam.ExamDTO{
		ID: e.ID, SubjectID: e.SubjectID, Title: e.Title, Description: e.Description, Instructions: bodyOf(e.Instructions),
		Mode: e.Mode, Status: e.Status, DurationMinutes: e.DurationMinutes, Navigation: e.Navigation,
		ShuffleItems: e.ShuffleItems, ShuffleOptions: e.ShuffleOptions, ResultVisibility: e.ResultVisibility,
		ShowExplanation: e.ShowExplanation, MaxAttempts: e.MaxAttempts, AttemptScoring: e.AttemptScoring,
		PassingScore: e.PassingScore, NegativeMarking: e.NegativeMarking, Security: e.Security, Settings: e.Settings,
		CanEdit: canEdit(p, e), PublishedAt: e.PublishedAt, CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		Sections: make([]modelExam.SectionDTO, 0, len(sections)),
	}
	total, totalKnown := 0.0, true
	minutes, minutesKnown := 0, true
	for _, sec := range sections {
		sd := modelExam.SectionDTO{
			ID: sec.ID, SortOrder: sec.SortOrder, Title: sec.Title, Instructions: bodyOf(sec.Instructions),
			DurationMinutes: sec.DurationMinutes, Selection: sec.Selection, AllowRevisit: sec.AllowRevisit,
			PassingScore: sec.PassingScore, Items: []modelExam.SectionItemDTO{}, Rules: []modelExam.RuleDTO{},
		}
		for _, it := range items {
			if it.SectionID != sec.ID {
				continue
			}
			eff := it.DefaultPoints
			if it.Points != nil {
				eff = *it.Points
			}
			total += eff
			sd.Items = append(sd.Items, modelExam.SectionItemDTO{
				ID: it.ID, ItemID: it.ItemID, Code: it.Code, Type: it.Type, ItemStatus: it.ItemStatus,
				StemPreview: preview(it.SearchText), VersionID: it.VersionID, VersionNo: it.VersionNo,
				Pinned: it.ItemVersionID != nil, Points: it.Points, EffectivePoints: eff,
				LockOptions: it.LockOptions, SortOrder: it.SortOrder,
			})
		}
		for _, ru := range rules {
			if ru.SectionID != sec.ID {
				continue
			}
			var available int64
			if e.Status == modelExam.StatusDraft {
				cands, err := s.storer.RuleCandidates(ctx, ru, fixedIDs)
				if err != nil {
					return modelExam.ExamDTO{}, err
				}
				available = int64(len(cands))
			} else if available, err = s.storer.PoolSize(ctx, ru.ID); err != nil {
				return modelExam.ExamDTO{}, err
			}
			if ru.Points != nil {
				total += *ru.Points * float64(ru.ItemCount)
			} else {
				totalKnown = false
			}
			tags := []string(ru.TagsAny)
			if tags == nil {
				tags = []string{}
			}
			sd.ItemCount += ru.ItemCount
			sd.Rules = append(sd.Rules, modelExam.RuleDTO{
				ID: ru.ID, BankID: ru.BankID, CompetencyID: ru.CompetencyID, ItemType: ru.ItemType, Difficulty: ru.Difficulty,
				TagsAny: tags, ItemCount: ru.ItemCount, Points: ru.Points, Available: available, SortOrder: ru.SortOrder,
			})
		}
		sd.ItemCount += len(sd.Items)
		dto.ItemCount += sd.ItemCount
		if sec.DurationMinutes != nil {
			minutes += *sec.DurationMinutes
		} else {
			minutesKnown = false
		}
		dto.Sections = append(dto.Sections, sd)
	}
	if totalKnown {
		t := round2(total)
		dto.TotalPoints = &t
	}
	switch {
	case e.DurationMinutes != nil:
		dto.TotalMinutes = e.DurationMinutes
	case minutesKnown && len(sections) > 0:
		dto.TotalMinutes = &minutes
	}
	return dto, nil
}

// samePermutation checks that ids lists exactly the elements of have, once each.
func samePermutation(ids, have []string) error {
	bad := &errr.ValidationError{Fields: map[string]string{"ids": "must list every id exactly once"}}
	if len(ids) != len(have) {
		return bad
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !slices.Contains(have, id) {
			return bad
		}
		seen[id] = true
	}
	return nil
}

func nextOrder(n int, at func(int) int) int {
	m := 0
	for i := range n {
		m = max(m, at(i))
	}
	return m + 1
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func roundPtr(f *float64) *float64 {
	if f == nil {
		return nil
	}
	r := round2(*f)
	return &r
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func preview(s *string) string {
	if s == nil {
		return ""
	}
	if utf8.RuneCountInString(*s) <= 160 {
		return *s
	}
	return string([]rune(*s)[:160]) + "…"
}

// mustJSON marshals plain values without escaping < > & (HTML is already sanitized).
func mustJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("marshal exam json: %v", err)) // plain values: unreachable
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
