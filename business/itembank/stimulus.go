package itembank

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
)

var errStimulusNotFound = errr.WithMessage(errr.ErrNotFound, "stimulus not found")

// ListStimuli returns one page of a bank's stimuli.
func (s *ItemBankService) ListStimuli(ctx context.Context, p modelAuth.Principal, bankID, query string, page, perPage int) ([]modelBank.StimulusDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ListStimuli", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	var (
		out   []modelBank.StimulusDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelRead); err != nil {
			return err
		}
		rows, n, err := s.storer.ListStimuli(ctx, bankID, query, page, perPage)
		if err != nil {
			return err
		}
		out = make([]modelBank.StimulusDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, toStimulusDTO(r))
		}
		total = n
		return nil
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	return out, total, nil
}

// GetStimulus returns a stimulus with its current version.
func (s *ItemBankService) GetStimulus(ctx context.Context, p modelAuth.Principal, id string) (modelBank.StimulusDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.GetStimulus", trace.WithAttributes(attribute.String("stimulus.id", id)))
	defer span.End()

	var out modelBank.StimulusDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		row, err := s.loadStimulus(ctx, p, id, levelRead)
		out = toStimulusDTO(row)
		return err
	})
	if err != nil {
		return modelBank.StimulusDTO{}, fail(span, err)
	}
	return out, nil
}

// CreateStimulus adds a stimulus (reading passage, figure, audio) to a bank.
func (s *ItemBankService) CreateStimulus(ctx context.Context, p modelAuth.Principal, bankID string, in modelBank.StimulusInput, client modelAudit.Client) (modelBank.StimulusDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.CreateStimulus", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	v := errr.Validation{}
	title := strings.TrimSpace(in.Title)
	v.Check(title != "" && len(title) <= 200, "title", "required, max 200 chars")
	body := sanitizeField(in.Body, "body", true, v)
	if err := v.Err(); err != nil {
		return modelBank.StimulusDTO{}, fail(span, err)
	}

	var out modelBank.StimulusDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelWrite); err != nil {
			return err
		}
		var err error
		out, err = s.insertStimulus(ctx, p, bankID, title, body, client)
		return err
	})
	if err != nil {
		return modelBank.StimulusDTO{}, fail(span, err)
	}
	return out, nil
}

// insertStimulus stores a validated stimulus with its audit entry, inside the caller's
// tenant transaction after the bank access check.
func (s *ItemBankService) insertStimulus(ctx context.Context, p modelAuth.Principal, bankID, title, body string, client modelAudit.Client) (modelBank.StimulusDTO, error) {
	now := s.now().UTC()
	st := modelBank.StimulusEntity{ID: newID(), TenantID: p.TenantID, BankID: bankID, Title: title, CreatedBy: &p.UserID, CreatedAt: now, UpdatedAt: now}
	ver := modelBank.StimulusVersionEntity{
		ID: newID(), TenantID: p.TenantID, StimulusID: st.ID, VersionNo: 1,
		Content: mustJSON(modelBank.StimulusContent{Body: body}), CreatedBy: &p.UserID, CreatedAt: now,
	}
	st.CurrentVersionID = &ver.ID
	row := StimulusRow{StimulusEntity: st, VersionNo: 1, Content: ver.Content}
	if err := s.checkMedia(ctx, "body", body); err != nil {
		return modelBank.StimulusDTO{}, err
	}
	if err := s.storer.CreateStimulus(ctx, &st, &ver); err != nil {
		return modelBank.StimulusDTO{}, err
	}
	dto := toStimulusDTO(row)
	return dto, s.audit(ctx, p, client, modelAudit.ActionStimulusCreate, "stimulus", st.ID, nil, dto)
}

// UpdateStimulus changes the title and/or body. Changing the body of a locked version
// (already used by an exam) creates a new version; unlocked items follow the new version,
// locked item versions keep the text they were used with.
func (s *ItemBankService) UpdateStimulus(ctx context.Context, p modelAuth.Principal, id string, in modelBank.StimulusUpdateInput, client modelAudit.Client) (modelBank.StimulusDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.UpdateStimulus", trace.WithAttributes(attribute.String("stimulus.id", id)))
	defer span.End()

	v := errr.Validation{}
	var title, body string
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
		v.Check(title != "" && len(title) <= 200, "title", "required, max 200 chars")
	}
	if in.Body != nil {
		body = sanitizeField(*in.Body, "body", true, v)
	}
	if err := v.Err(); err != nil {
		return modelBank.StimulusDTO{}, fail(span, err)
	}

	var out modelBank.StimulusDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		row, err := s.loadStimulus(ctx, p, id, levelWrite)
		if err != nil {
			return err
		}
		before := toStimulusDTO(row)
		if in.Title != nil {
			row.Title = title
		}
		if in.Body != nil && body != before.Body {
			if err := s.checkMedia(ctx, "body", body); err != nil {
				return err
			}
			content := mustJSON(modelBank.StimulusContent{Body: body})
			if row.LockedAt == nil {
				if err := s.storer.UpdateStimulusContent(ctx, *row.CurrentVersionID, content); err != nil {
					return err
				}
			} else {
				ver := modelBank.StimulusVersionEntity{
					ID: newID(), TenantID: p.TenantID, StimulusID: id, VersionNo: row.VersionNo + 1,
					Content: content, CreatedBy: &p.UserID, CreatedAt: s.now().UTC(),
				}
				if err := s.storer.CreateStimulusVersion(ctx, &ver); err != nil {
					return err
				}
				if err := s.storer.RepointUnlockedItems(ctx, id, ver.ID); err != nil {
					return err
				}
				row.CurrentVersionID, row.VersionNo, row.LockedAt = &ver.ID, ver.VersionNo, nil
			}
			row.Content = content
		}
		if err := s.storer.UpdateStimulus(ctx, &row.StimulusEntity); err != nil {
			return err
		}
		row.UpdatedAt = s.now().UTC()
		out = toStimulusDTO(row)
		return s.audit(ctx, p, client, modelAudit.ActionStimulusUpdate, "stimulus", id, before, out)
	})
	if err != nil {
		return modelBank.StimulusDTO{}, fail(span, err)
	}
	return out, nil
}

// DeleteStimulus soft-deletes a stimulus no item uses anymore.
func (s *ItemBankService) DeleteStimulus(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.DeleteStimulus", trace.WithAttributes(attribute.String("stimulus.id", id)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		row, err := s.loadStimulus(ctx, p, id, levelWrite)
		if err != nil {
			return err
		}
		n, err := s.storer.CountItemsUsingStimulus(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "stimulus is used by items")
		}
		if err := s.storer.SoftDeleteStimulus(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionStimulusDelete, "stimulus", id, toStimulusDTO(row), nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

func (s *ItemBankService) loadStimulus(ctx context.Context, p modelAuth.Principal, id string, lvl level) (StimulusRow, error) {
	if uuid.Validate(id) != nil {
		return StimulusRow{}, errStimulusNotFound
	}
	row, err := s.storer.FindStimulus(ctx, id)
	if err != nil {
		return StimulusRow{}, err
	}
	if _, _, err := s.bankAccess(ctx, p, row.BankID, lvl); err != nil {
		if lvl == levelRead || errors.Is(err, errBankNotFound) {
			return StimulusRow{}, errStimulusNotFound
		}
		return StimulusRow{}, err
	}
	return row, nil
}

func toStimulusDTO(r StimulusRow) modelBank.StimulusDTO {
	var c modelBank.StimulusContent
	_ = json.Unmarshal(r.Content, &c) // written by us
	dto := modelBank.StimulusDTO{
		ID: r.ID, BankID: r.BankID, Title: r.Title, VersionNo: r.VersionNo,
		Body: c.Body, Locked: r.LockedAt != nil, UpdatedAt: r.UpdatedAt,
	}
	if r.CurrentVersionID != nil {
		dto.VersionID = *r.CurrentVersionID
	}
	return dto
}
