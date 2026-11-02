package itembank

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
)

var errBankNotFound = errr.WithMessage(errr.ErrNotFound, "item bank not found")

// ItemBankService manages item banks, their members, stimuli and items.
//
// Access = role permission AND bank membership:
//   - read  (items, stimuli, members): item.read  + viewer/editor/owner
//   - write (items, stimuli):          item.write + editor/owner
//   - manage (bank itself, members):   item.write + owner
//
// tenant.manage (admin) counts as owner of every bank. Banks the caller cannot
// access are reported as not found, so their existence does not leak.
type ItemBankService struct {
	storer ItemBankStorer
	tx     TxManager
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewItemBankService returns a concrete ItemBankService.
func NewItemBankService(storer ItemBankStorer, tx TxManager, logger *slog.Logger) *ItemBankService {
	return &ItemBankService{storer: storer, tx: tx, now: time.Now, tracer: otel.Tracer("doexam.service.itembank"), logger: logger}
}

// level is the access a call needs.
type level int

const (
	levelRead level = iota
	levelWrite
	levelManage
)

// bankAccess loads the bank and checks that p may act on it at lvl. Must run in a tenant transaction.
func (s *ItemBankService) bankAccess(ctx context.Context, p modelAuth.Principal, bankID string, lvl level) (modelBank.BankEntity, modelBank.Access, error) {
	if uuid.Validate(bankID) != nil {
		return modelBank.BankEntity{}, "", errBankNotFound
	}
	bank, err := s.storer.FindBank(ctx, bankID)
	if err != nil {
		return modelBank.BankEntity{}, "", err
	}
	access := modelBank.AccessOwner
	if !p.Has(modelAuth.PermTenantManage) {
		if access, err = s.storer.MemberAccess(ctx, bankID, p.UserID); err != nil {
			return modelBank.BankEntity{}, "", err
		}
	}
	canRead := p.Has(modelAuth.PermItemRead) || p.Has(modelAuth.PermItemWrite)
	if access == "" || !canRead {
		return modelBank.BankEntity{}, "", errBankNotFound
	}
	switch lvl {
	case levelWrite:
		if !p.Has(modelAuth.PermItemWrite) || access.Rank() < modelBank.AccessEditor.Rank() {
			return modelBank.BankEntity{}, "", errr.ErrForbidden
		}
	case levelManage:
		if !p.Has(modelAuth.PermItemWrite) || access != modelBank.AccessOwner {
			return modelBank.BankEntity{}, "", errr.ErrForbidden
		}
	}
	return bank, access, nil
}

// ListBanks returns the banks the caller can access.
func (s *ItemBankService) ListBanks(ctx context.Context, p modelAuth.Principal, f modelBank.BankFilter) ([]modelBank.BankDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ListBanks")
	defer span.End()

	if !p.Has(modelAuth.PermItemRead) && !p.Has(modelAuth.PermItemWrite) {
		return nil, 0, fail(span, errr.ErrForbidden)
	}
	if f.SubjectID != "" && uuid.Validate(f.SubjectID) != nil {
		return nil, 0, &errr.ValidationError{Fields: map[string]string{"subject_id": "must be a UUID"}}
	}
	var (
		out   []modelBank.BankDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		rows, n, err := s.storer.ListBanks(ctx, f, p.UserID, p.Has(modelAuth.PermTenantManage))
		if err != nil {
			return err
		}
		out = make([]modelBank.BankDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, toBankDTO(r.BankEntity, r.MyAccess, r.ItemCount))
		}
		total = n
		return nil
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	return out, total, nil
}

// GetBank returns one bank.
func (s *ItemBankService) GetBank(ctx context.Context, p modelAuth.Principal, id string) (modelBank.BankDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.GetBank", trace.WithAttributes(attribute.String("bank.id", id)))
	defer span.End()

	var out modelBank.BankDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		bank, access, err := s.bankAccess(ctx, p, id, levelRead)
		if err != nil {
			return err
		}
		n, err := s.storer.CountItems(ctx, id)
		out = toBankDTO(bank, access, n)
		return err
	})
	if err != nil {
		return modelBank.BankDTO{}, fail(span, err)
	}
	return out, nil
}

// CreateBank creates a bank under a subject; the creator becomes its owner.
func (s *ItemBankService) CreateBank(ctx context.Context, p modelAuth.Principal, in modelBank.BankInput, client modelAudit.Client) (modelBank.BankDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.CreateBank")
	defer span.End()

	if !p.Has(modelAuth.PermItemWrite) {
		return modelBank.BankDTO{}, fail(span, errr.ErrForbidden)
	}
	in.Normalize()
	v := errr.Validation{}
	v.Check(uuid.Validate(in.SubjectID) == nil, "subject_id", "required UUID")
	v.Check(in.Name != "" && len(in.Name) <= 200, "name", "required, max 200 chars")
	v.Check(in.Description == nil || len(*in.Description) <= 2000, "description", "max 2000 chars")
	if err := v.Err(); err != nil {
		return modelBank.BankDTO{}, fail(span, err)
	}

	bank := modelBank.BankEntity{
		ID: newID(), TenantID: p.TenantID, SubjectID: in.SubjectID, Name: in.Name,
		Description: in.Description, CreatedBy: &p.UserID, CreatedAt: s.now().UTC(),
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		ok, err := s.storer.SubjectExists(ctx, in.SubjectID)
		if err != nil {
			return err
		}
		if !ok {
			return &errr.ValidationError{Fields: map[string]string{"subject_id": "subject not found"}}
		}
		if err := s.storer.CreateBank(ctx, &bank); err != nil {
			return err
		}
		if err := s.storer.UpsertMember(ctx, &modelBank.BankMemberEntity{
			BankID: bank.ID, UserID: p.UserID, TenantID: p.TenantID, Access: modelBank.AccessOwner,
		}); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionBankCreate, "item_bank", bank.ID, nil, toBankDTO(bank, modelBank.AccessOwner, 0))
	})
	if err != nil {
		return modelBank.BankDTO{}, fail(span, err)
	}
	return toBankDTO(bank, modelBank.AccessOwner, 0), nil
}

// UpdateBank renames a bank or changes its description (owner).
func (s *ItemBankService) UpdateBank(ctx context.Context, p modelAuth.Principal, id string, in modelBank.BankUpdateInput, client modelAudit.Client) (modelBank.BankDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.UpdateBank", trace.WithAttributes(attribute.String("bank.id", id)))
	defer span.End()

	var out modelBank.BankDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		bank, access, err := s.bankAccess(ctx, p, id, levelManage)
		if err != nil {
			return err
		}
		n, err := s.storer.CountItems(ctx, id)
		if err != nil {
			return err
		}
		before := toBankDTO(bank, access, n)
		v := errr.Validation{}
		if in.Name != nil {
			bank.Name = strings.TrimSpace(*in.Name)
			v.Check(bank.Name != "" && len(bank.Name) <= 200, "name", "required, max 200 chars")
		}
		if in.Description != nil {
			d := strings.TrimSpace(*in.Description)
			v.Check(len(d) <= 2000, "description", "max 2000 chars")
			bank.Description = optional(d)
		}
		if err := v.Err(); err != nil {
			return err
		}
		if err := s.storer.UpdateBank(ctx, &bank); err != nil {
			return err
		}
		out = toBankDTO(bank, access, n)
		return s.audit(ctx, p, client, modelAudit.ActionBankUpdate, "item_bank", id, before, out)
	})
	if err != nil {
		return modelBank.BankDTO{}, fail(span, err)
	}
	return out, nil
}

// DeleteBank soft-deletes an empty bank (owner).
func (s *ItemBankService) DeleteBank(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.DeleteBank", trace.WithAttributes(attribute.String("bank.id", id)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		bank, access, err := s.bankAccess(ctx, p, id, levelManage)
		if err != nil {
			return err
		}
		n, err := s.storer.CountItems(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "item bank still has items")
		}
		if err := s.storer.SoftDeleteBank(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionBankDelete, "item_bank", id, toBankDTO(bank, access, 0), nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// ListMembers returns who can access a bank (readers may see it).
func (s *ItemBankService) ListMembers(ctx context.Context, p modelAuth.Principal, bankID string) ([]modelBank.MemberDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ListMembers", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	var out []modelBank.MemberDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelRead); err != nil {
			return err
		}
		var err error
		out, err = s.storer.ListMembers(ctx, bankID)
		return err
	})
	if err != nil {
		return nil, fail(span, err)
	}
	return out, nil
}

// SetMember grants or changes a user's access to a bank (owner). The last owner cannot be demoted.
func (s *ItemBankService) SetMember(ctx context.Context, p modelAuth.Principal, bankID string, in modelBank.MemberInput, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.SetMember", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	in.UserID = strings.ToLower(strings.TrimSpace(in.UserID))
	v := errr.Validation{}
	v.Check(uuid.Validate(in.UserID) == nil, "user_id", "required UUID")
	v.Check(in.Access.Rank() > 0, "access", "must be viewer, editor or owner")
	if err := v.Err(); err != nil {
		return fail(span, err)
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelManage); err != nil {
			return err
		}
		ok, err := s.storer.UserExists(ctx, in.UserID)
		if err != nil {
			return err
		}
		if !ok {
			return &errr.ValidationError{Fields: map[string]string{"user_id": "user not found"}}
		}
		if in.Access != modelBank.AccessOwner {
			if err := s.ensureAnotherOwner(ctx, bankID, in.UserID); err != nil {
				return err
			}
		}
		if err := s.storer.UpsertMember(ctx, &modelBank.BankMemberEntity{
			BankID: bankID, UserID: in.UserID, TenantID: p.TenantID, Access: in.Access,
		}); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionBankMemberSet, "item_bank", bankID, nil,
			map[string]any{"user_id": in.UserID, "access": in.Access})
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// RemoveMember revokes a user's access to a bank (owner). The last owner cannot be removed.
func (s *ItemBankService) RemoveMember(ctx context.Context, p modelAuth.Principal, bankID, userID string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.RemoveMember", trace.WithAttributes(attribute.String("bank.id", bankID)))
	defer span.End()

	if uuid.Validate(userID) != nil {
		return errr.WithMessage(errr.ErrNotFound, "member not found")
	}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.bankAccess(ctx, p, bankID, levelManage); err != nil {
			return err
		}
		if err := s.ensureAnotherOwner(ctx, bankID, userID); err != nil {
			return err
		}
		removed, err := s.storer.RemoveMember(ctx, bankID, userID)
		if err != nil {
			return err
		}
		if !removed {
			return errr.WithMessage(errr.ErrNotFound, "member not found")
		}
		return s.audit(ctx, p, client, modelAudit.ActionBankMemberRemove, "item_bank", bankID,
			map[string]any{"user_id": userID}, nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// ensureAnotherOwner fails when userID is currently the only active owner of the bank.
func (s *ItemBankService) ensureAnotherOwner(ctx context.Context, bankID, userID string) error {
	current, err := s.storer.MemberAccess(ctx, bankID, userID)
	if err != nil || current != modelBank.AccessOwner {
		return err
	}
	n, err := s.storer.CountOwners(ctx, bankID, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		return errr.WithMessage(errr.ErrForbidden, "item bank must keep at least one owner")
	}
	return nil
}

func (s *ItemBankService) audit(ctx context.Context, p modelAuth.Principal, client modelAudit.Client, action, entityType, id string, before, after any) error {
	return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: p.TenantID, ActorID: p.UserID, Action: action, EntityType: entityType, EntityID: id,
		Before: before, After: after, Client: client,
	}))
}

func toBankDTO(b modelBank.BankEntity, access modelBank.Access, items int64) modelBank.BankDTO {
	return modelBank.BankDTO{
		ID: b.ID, SubjectID: b.SubjectID, Name: b.Name, Description: b.Description,
		MyAccess: access, ItemCount: items, CreatedAt: b.CreatedAt,
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string { return uuid.Must(uuid.NewV7()).String() }
