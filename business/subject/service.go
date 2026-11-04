package subject

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSubject "doexam/models/subject"
	"doexam/pkg/errr"
)

var (
	errSubjectNotFound    = errr.WithMessage(errr.ErrNotFound, "subject not found")
	errCompetencyNotFound = errr.WithMessage(errr.ErrNotFound, "competency not found")
	// readPerms: authors and exam organizers both need the subject list.
	readPerms  = []string{modelAuth.PermItemRead, modelAuth.PermExamRead}
	writePerms = []string{modelAuth.PermItemWrite}
)

// SubjectService manages subjects (test domains) and their competency trees.
// Reading needs item.read or exam.read; changes need item.write.
type SubjectService struct {
	storer SubjectStorer
	tx     TxManager
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewSubjectService returns a concrete SubjectService.
func NewSubjectService(storer SubjectStorer, tx TxManager, logger *slog.Logger) *SubjectService {
	return &SubjectService{storer: storer, tx: tx, now: time.Now, tracer: otel.Tracer("doexam.service.subject"), logger: logger}
}

// List returns one page of subjects.
func (s *SubjectService) List(ctx context.Context, p modelAuth.Principal, f modelSubject.ListFilter) ([]modelSubject.SubjectDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.List")
	defer span.End()

	var (
		out   []modelSubject.SubjectDTO
		total int64
	)
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		rows, n, err := s.storer.ListSubjects(ctx, f)
		if err != nil {
			return err
		}
		out = make([]modelSubject.SubjectDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, modelSubject.ToDTO(r))
		}
		total = n
		return nil
	})
	return out, total, err
}

// Get returns one subject.
func (s *SubjectService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelSubject.SubjectDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.Get", trace.WithAttributes(attribute.String("subject.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelSubject.SubjectDTO{}, errSubjectNotFound
	}
	var out modelSubject.SubjectDTO
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		e, err := s.storer.FindSubject(ctx, id)
		out = modelSubject.ToDTO(e)
		return err
	})
	return out, err
}

// Create adds a subject.
func (s *SubjectService) Create(ctx context.Context, p modelAuth.Principal, in modelSubject.SubjectInput, client modelAudit.Client) (modelSubject.SubjectDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.Create")
	defer span.End()

	in.Normalize()
	if err := validateCodeName(in.Code, in.Name); err != nil {
		return modelSubject.SubjectDTO{}, fail(span, err)
	}
	e := modelSubject.SubjectEntity{ID: newID(), TenantID: p.TenantID, Code: in.Code, Name: in.Name, CreatedAt: s.now().UTC()}
	err := s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		if err := s.storer.CreateSubject(ctx, &e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSubjectCreate, "subject", e.ID, nil, modelSubject.ToDTO(e))
	})
	return modelSubject.ToDTO(e), err
}

// Update changes code and/or name of a subject.
func (s *SubjectService) Update(ctx context.Context, p modelAuth.Principal, id string, in modelSubject.SubjectUpdateInput, client modelAudit.Client) (modelSubject.SubjectDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.Update", trace.WithAttributes(attribute.String("subject.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelSubject.SubjectDTO{}, errSubjectNotFound
	}
	in.Normalize()
	var out modelSubject.SubjectDTO
	err := s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		e, err := s.storer.FindSubject(ctx, id)
		if err != nil {
			return err
		}
		before := modelSubject.ToDTO(e)
		if in.Code != nil {
			e.Code = *in.Code
		}
		if in.Name != nil {
			e.Name = *in.Name
		}
		if err := validateCodeName(e.Code, e.Name); err != nil {
			return err
		}
		if err := s.storer.UpdateSubject(ctx, &e); err != nil {
			return err
		}
		out = modelSubject.ToDTO(e)
		return s.audit(ctx, p, client, modelAudit.ActionSubjectUpdate, "subject", id, before, out)
	})
	return out, err
}

// Delete soft-deletes a subject that has no item banks.
func (s *SubjectService) Delete(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "SubjectService.Delete", trace.WithAttributes(attribute.String("subject.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return errSubjectNotFound
	}
	return s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		e, err := s.storer.FindSubject(ctx, id)
		if err != nil {
			return err
		}
		n, err := s.storer.CountBanks(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "subject still has item banks")
		}
		if err := s.storer.SoftDeleteSubject(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionSubjectDelete, "subject", id, modelSubject.ToDTO(e), nil)
	})
}

// ListCompetencies returns the whole competency tree of a subject (flat, ordered).
func (s *SubjectService) ListCompetencies(ctx context.Context, p modelAuth.Principal, subjectID string) ([]modelSubject.CompetencyDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.ListCompetencies", trace.WithAttributes(attribute.String("subject.id", subjectID)))
	defer span.End()

	if uuid.Validate(subjectID) != nil {
		return nil, errSubjectNotFound
	}
	var out []modelSubject.CompetencyDTO
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		if _, err := s.storer.FindSubject(ctx, subjectID); err != nil {
			return err
		}
		rows, err := s.storer.ListCompetencies(ctx, subjectID)
		if err != nil {
			return err
		}
		out = make([]modelSubject.CompetencyDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, modelSubject.ToCompetencyDTO(r))
		}
		return nil
	})
	return out, err
}

// CreateCompetency adds a competency under a subject (optionally under a parent of the same subject).
func (s *SubjectService) CreateCompetency(ctx context.Context, p modelAuth.Principal, subjectID string, in modelSubject.CompetencyInput, client modelAudit.Client) (modelSubject.CompetencyDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.CreateCompetency", trace.WithAttributes(attribute.String("subject.id", subjectID)))
	defer span.End()

	if uuid.Validate(subjectID) != nil {
		return modelSubject.CompetencyDTO{}, errSubjectNotFound
	}
	in.Normalize()
	if err := validateCodeName(in.Code, in.Name); err != nil {
		return modelSubject.CompetencyDTO{}, fail(span, err)
	}
	e := modelSubject.CompetencyEntity{
		ID: newID(), TenantID: p.TenantID, SubjectID: subjectID, ParentID: in.ParentID,
		Code: in.Code, Name: in.Name, SortOrder: in.SortOrder, CreatedAt: s.now().UTC(),
	}
	err := s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		if _, err := s.storer.FindSubject(ctx, subjectID); err != nil {
			return err
		}
		if e.ParentID != nil {
			if err := s.checkParent(ctx, subjectID, *e.ParentID); err != nil {
				return err
			}
		}
		if err := s.storer.CreateCompetency(ctx, &e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionCompetencyCreate, "competency", e.ID, nil, modelSubject.ToCompetencyDTO(e))
	})
	return modelSubject.ToCompetencyDTO(e), err
}

// UpdateCompetency changes a competency; moving it under its own descendant is rejected.
func (s *SubjectService) UpdateCompetency(ctx context.Context, p modelAuth.Principal, id string, in modelSubject.CompetencyUpdateInput, client modelAudit.Client) (modelSubject.CompetencyDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SubjectService.UpdateCompetency", trace.WithAttributes(attribute.String("competency.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelSubject.CompetencyDTO{}, errCompetencyNotFound
	}
	in.Normalize()
	var out modelSubject.CompetencyDTO
	err := s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		e, err := s.storer.FindCompetency(ctx, id)
		if err != nil {
			return err
		}
		before := modelSubject.ToCompetencyDTO(e)
		if in.ParentID != nil {
			e.ParentID = nil
			if *in.ParentID != "" {
				if *in.ParentID == id {
					return &errr.ValidationError{Fields: map[string]string{"parent_id": "a competency cannot be its own parent"}}
				}
				if err := s.checkParent(ctx, e.SubjectID, *in.ParentID); err != nil {
					return err
				}
				ancestors, err := s.storer.CompetencyAncestorIDs(ctx, *in.ParentID)
				if err != nil {
					return err
				}
				if slices.Contains(ancestors, id) {
					return &errr.ValidationError{Fields: map[string]string{"parent_id": "cannot move a competency under its own child"}}
				}
				e.ParentID = in.ParentID
			}
		}
		if in.Code != nil {
			e.Code = *in.Code
		}
		if in.Name != nil {
			e.Name = *in.Name
		}
		if in.SortOrder != nil {
			e.SortOrder = *in.SortOrder
		}
		if err := validateCodeName(e.Code, e.Name); err != nil {
			return err
		}
		if err := s.storer.UpdateCompetency(ctx, &e); err != nil {
			return err
		}
		out = modelSubject.ToCompetencyDTO(e)
		return s.audit(ctx, p, client, modelAudit.ActionCompetencyUpdate, "competency", id, before, out)
	})
	return out, err
}

// DeleteCompetency soft-deletes a leaf competency that no item uses.
func (s *SubjectService) DeleteCompetency(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "SubjectService.DeleteCompetency", trace.WithAttributes(attribute.String("competency.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return errCompetencyNotFound
	}
	return s.run(ctx, span, p, writePerms, func(ctx context.Context) error {
		e, err := s.storer.FindCompetency(ctx, id)
		if err != nil {
			return err
		}
		children, err := s.storer.CountChildCompetencies(ctx, id)
		if err != nil {
			return err
		}
		if children > 0 {
			return errr.WithMessage(errr.ErrConflict, "competency still has children")
		}
		used, err := s.storer.CountItemVersionsUsing(ctx, id)
		if err != nil {
			return err
		}
		if used > 0 {
			return errr.WithMessage(errr.ErrConflict, "competency is used by items")
		}
		if err := s.storer.SoftDeleteCompetency(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionCompetencyDelete, "competency", id, modelSubject.ToCompetencyDTO(e), nil)
	})
}

func (s *SubjectService) checkParent(ctx context.Context, subjectID, parentID string) error {
	invalid := &errr.ValidationError{Fields: map[string]string{"parent_id": "competency not found in this subject"}}
	if uuid.Validate(parentID) != nil {
		return invalid
	}
	parent, err := s.storer.FindCompetency(ctx, parentID)
	if errors.Is(err, errr.ErrNotFound) || (err == nil && parent.SubjectID != subjectID) {
		return invalid
	}
	return err
}

func (s *SubjectService) run(ctx context.Context, span trace.Span, p modelAuth.Principal, perms []string, fn func(ctx context.Context) error) error {
	if !slices.ContainsFunc(perms, p.Has) {
		return fail(span, errr.ErrForbidden)
	}
	if err := s.tx.WithTenant(ctx, p.TenantID, fn); err != nil {
		return fail(span, err)
	}
	return nil
}

func (s *SubjectService) audit(ctx context.Context, p modelAuth.Principal, client modelAudit.Client, action, entityType, id string, before, after any) error {
	return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: p.TenantID, ActorID: p.UserID, Action: action, EntityType: entityType, EntityID: id,
		Before: before, After: after, Client: client,
	}))
}

func validateCodeName(code, name string) error {
	v := errr.Validation{}
	v.Check(code != "" && len(code) <= 50, "code", "required, max 50 chars")
	v.Check(name != "" && len(name) <= 200, "name", "required, max 200 chars")
	return v.Err()
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string { return uuid.Must(uuid.NewV7()).String() }
