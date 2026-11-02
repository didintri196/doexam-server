package exam

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
	"doexam/pkg/richtext"
)

var (
	errExamNotFound = errr.WithMessage(errr.ErrNotFound, "exam not found")
	errNotDraft     = errr.WithMessage(errr.ErrConflict, "only draft exams can be changed; unpublish it first")
)

// ExamService composes exams: settings, sections, hand-picked items and blueprint rules,
// and publishes them (pinning and locking the item versions used).
//
// Reading needs exam.read (or exam.write). Changing needs exam.write AND being the
// creator (or tenant.manage). Publishing, unpublishing and archiving also need exam.publish.
type ExamService struct {
	storer ExamStorer
	tx     TxManager
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewExamService returns a concrete ExamService.
func NewExamService(storer ExamStorer, tx TxManager, logger *slog.Logger) *ExamService {
	return &ExamService{storer: storer, tx: tx, now: time.Now, tracer: otel.Tracer("doexam.service.exam"), logger: logger}
}

// List returns one page of exams.
func (s *ExamService) List(ctx context.Context, p modelAuth.Principal, f modelExam.ListFilter) ([]modelExam.ExamSummaryDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.List")
	defer span.End()

	if !canRead(p) {
		return nil, 0, fail(span, errr.ErrForbidden)
	}
	v := errr.Validation{}
	v.Check(f.Status == "" || f.Status == modelExam.StatusDraft || f.Status == modelExam.StatusPublished || f.Status == modelExam.StatusArchived,
		"status", "must be draft, published or archived")
	v.Check(f.SubjectID == "" || uuid.Validate(f.SubjectID) == nil, "subject_id", "must be a UUID")
	if err := v.Err(); err != nil {
		return nil, 0, err
	}
	var (
		out   []modelExam.ExamSummaryDTO
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		rows, n, err := s.storer.ListExams(ctx, f, p.UserID)
		if err != nil {
			return err
		}
		out = make([]modelExam.ExamSummaryDTO, 0, len(rows))
		for _, r := range rows {
			out = append(out, modelExam.ExamSummaryDTO{
				ID: r.ID, Title: r.Title, SubjectID: r.SubjectID, Mode: r.Mode, Status: r.Status,
				DurationMinutes: r.DurationMinutes, SectionCount: r.SectionCount, CreatedBy: r.CreatedBy,
				PublishedAt: r.PublishedAt, UpdatedAt: r.UpdatedAt,
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

// Get returns an exam with its full structure.
func (s *ExamService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Get", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	if !canRead(p) {
		return modelExam.ExamDTO{}, fail(span, errr.ErrForbidden)
	}
	if uuid.Validate(id) != nil {
		return modelExam.ExamDTO{}, errExamNotFound
	}
	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.storer.FindExam(ctx, id, false)
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, p, e)
		return err
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

// Create adds a draft exam owned by the caller.
func (s *ExamService) Create(ctx context.Context, p modelAuth.Principal, in modelExam.ExamInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Create")
	defer span.End()

	if !p.Has(modelAuth.PermExamWrite) {
		return modelExam.ExamDTO{}, fail(span, errr.ErrForbidden)
	}
	now := s.now().UTC()
	e := modelExam.ExamEntity{
		ID: newID(), TenantID: p.TenantID, Mode: modelExam.ModeLinear, Status: modelExam.StatusDraft,
		Navigation: modelExam.NavigationFree, ShuffleItems: true, ShuffleOptions: true,
		ResultVisibility: modelExam.ResultAfterGrading, MaxAttempts: 1, AttemptScoring: modelExam.ScoringHighest,
		Security: mustJSON(modelExam.SecurityConfig{Profile: modelExam.SecurityLight}), Settings: []byte("{}"),
		CreatedBy: &p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	v := errr.Validation{}
	v.Check(in.Title != nil, "title", "required")
	applyExamInput(&e, in, v)
	if err := v.Err(); err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.checkSubject(ctx, e.SubjectID); err != nil {
			return err
		}
		if err := s.storer.CreateExam(ctx, &e); err != nil {
			return err
		}
		var err error
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamCreate, e.ID, nil, out)
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

// publishedEditable are the only fields a published exam accepts (display/result settings).
func publishedEditable(in modelExam.ExamInput) bool {
	return in.SubjectID == nil && in.Mode == nil && !in.DurationMinutes.Set && in.Navigation == nil &&
		in.ShuffleItems == nil && in.ShuffleOptions == nil && in.MaxAttempts == nil && in.AttemptScoring == nil &&
		!in.PassingScore.Set && in.NegativeMarking == nil && in.Security == nil && in.Settings == nil
}

// Update changes exam settings. A published exam only accepts title, description,
// instructions, result_visibility and show_explanation.
func (s *ExamService) Update(ctx context.Context, p modelAuth.Principal, id string, in modelExam.ExamInput, client modelAudit.Client) (modelExam.ExamDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ExamService.Update", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	var out modelExam.ExamDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForEdit(ctx, p, id)
		if err != nil {
			return err
		}
		switch {
		case e.Status == modelExam.StatusArchived:
			return errr.WithMessage(errr.ErrConflict, "archived exams cannot be changed")
		case e.Status == modelExam.StatusPublished && !publishedEditable(in):
			return errr.WithMessage(errr.ErrConflict,
				"a published exam only accepts title, description, instructions, result_visibility and show_explanation; unpublish it first")
		}
		before, err := s.detail(ctx, p, e)
		if err != nil {
			return err
		}
		v := errr.Validation{}
		applyExamInput(&e, in, v)
		if err := v.Err(); err != nil {
			return err
		}
		if in.Mode != nil && *in.Mode == modelExam.ModeLinear {
			sections, err := s.storer.ListSections(ctx, id)
			if err != nil {
				return err
			}
			for _, sec := range sections {
				if sec.Selection == modelExam.SelectionRandom {
					return &errr.ValidationError{Fields: map[string]string{"mode": "linear exams cannot have random sections; remove them first"}}
				}
			}
		}
		if err := s.checkSubject(ctx, e.SubjectID); err != nil {
			return err
		}
		if err := s.storer.UpdateExam(ctx, &e); err != nil {
			return err
		}
		e.UpdatedAt = s.now().UTC()
		if out, err = s.detail(ctx, p, e); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamUpdate, id, examSettings(before), examSettings(out))
	})
	if err != nil {
		return modelExam.ExamDTO{}, fail(span, err)
	}
	return out, nil
}

// Delete soft-deletes a draft exam. Published exams are archived instead.
func (s *ExamService) Delete(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "ExamService.Delete", trace.WithAttributes(attribute.String("exam.id", id)))
	defer span.End()

	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.loadForEdit(ctx, p, id)
		if err != nil {
			return err
		}
		if e.Status != modelExam.StatusDraft {
			return errr.WithMessage(errr.ErrConflict, "only draft exams can be deleted; archive it instead")
		}
		if err := s.storer.SoftDeleteExam(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, p, client, modelAudit.ActionExamDelete, id, map[string]any{"title": e.Title}, nil)
	})
	if err != nil {
		return fail(span, err)
	}
	return nil
}

// applyExamInput validates in and applies the given fields to e.
func applyExamInput(e *modelExam.ExamEntity, in modelExam.ExamInput, v errr.Validation) {
	if in.SubjectID != nil {
		sid := strings.TrimSpace(*in.SubjectID)
		v.Check(sid == "" || uuid.Validate(sid) == nil, "subject_id", "must be a UUID")
		e.SubjectID = optional(sid)
	}
	if in.Title != nil {
		e.Title = strings.TrimSpace(*in.Title)
		v.Check(e.Title != "" && utf8.RuneCountInString(e.Title) <= 200, "title", "required, max 200 chars")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		v.Check(len(d) <= 2000, "description", "max 2000 chars")
		e.Description = optional(d)
	}
	if in.Instructions != nil {
		e.Instructions = richBody(*in.Instructions, "instructions", v)
	}
	if in.Mode != nil {
		v.Check(*in.Mode == modelExam.ModeLinear || *in.Mode == modelExam.ModeRandomBlueprint, "mode", "must be linear or random_blueprint")
		e.Mode = *in.Mode
	}
	if in.DurationMinutes.Set {
		e.DurationMinutes = in.DurationMinutes.Ptr()
		v.Check(e.DurationMinutes == nil || (*e.DurationMinutes >= 1 && *e.DurationMinutes <= 1440), "duration_minutes", "1-1440 or null")
	}
	if in.Navigation != nil {
		v.Check(*in.Navigation == modelExam.NavigationFree || *in.Navigation == modelExam.NavigationForwardOnly, "navigation", "must be free or forward_only")
		e.Navigation = *in.Navigation
	}
	if in.ShuffleItems != nil {
		e.ShuffleItems = *in.ShuffleItems
	}
	if in.ShuffleOptions != nil {
		e.ShuffleOptions = *in.ShuffleOptions
	}
	if in.ResultVisibility != nil {
		r := *in.ResultVisibility
		v.Check(r == modelExam.ResultImmediately || r == modelExam.ResultAfterGrading || r == modelExam.ResultAfterSession || r == modelExam.ResultNever,
			"result_visibility", "must be immediately, after_grading, after_session or never")
		e.ResultVisibility = r
	}
	if in.ShowExplanation != nil {
		e.ShowExplanation = *in.ShowExplanation
	}
	if in.MaxAttempts != nil {
		v.Check(*in.MaxAttempts >= 1 && *in.MaxAttempts <= 20, "max_attempts", "1-20")
		e.MaxAttempts = *in.MaxAttempts
	}
	if in.AttemptScoring != nil {
		a := *in.AttemptScoring
		v.Check(a == modelExam.ScoringHighest || a == modelExam.ScoringLatest || a == modelExam.ScoringAverage, "attempt_scoring", "must be highest, latest or average")
		e.AttemptScoring = a
	}
	if in.PassingScore.Set {
		e.PassingScore = roundPtr(in.PassingScore.Ptr())
		v.Check(e.PassingScore == nil || (*e.PassingScore >= 0 && *e.PassingScore <= 100), "passing_score", "0-100 or null")
	}
	if in.NegativeMarking != nil {
		n := round2(*in.NegativeMarking)
		v.Check(n >= 0 && n <= 100, "negative_marking", "0-100")
		e.NegativeMarking = n
	}
	if in.Security != nil {
		v.Check(in.Security.Profile == modelExam.SecurityLight || in.Security.Profile == modelExam.SecurityStandard, "security.profile", "must be light or standard")
		e.Security = mustJSON(in.Security)
	}
	if in.Settings != nil {
		var obj map[string]any
		ok := json.Unmarshal(in.Settings, &obj) == nil && obj != nil && len(in.Settings) <= modelExam.MaxSettingsBytes
		v.Check(ok, "settings", "must be a JSON object, max 4 KB")
		if ok {
			e.Settings = mustJSON(obj)
		}
	}
}

func (s *ExamService) checkSubject(ctx context.Context, id *string) error {
	if id == nil {
		return nil
	}
	ok, err := s.storer.SubjectExists(ctx, *id)
	if err != nil {
		return err
	}
	if !ok {
		return &errr.ValidationError{Fields: map[string]string{"subject_id": "subject not found"}}
	}
	return nil
}

// loadForEdit loads an exam (locked for update) and checks the caller may change it.
func (s *ExamService) loadForEdit(ctx context.Context, p modelAuth.Principal, id string) (modelExam.ExamEntity, error) {
	if !p.Has(modelAuth.PermExamWrite) {
		return modelExam.ExamEntity{}, errr.ErrForbidden
	}
	if uuid.Validate(id) != nil {
		return modelExam.ExamEntity{}, errExamNotFound
	}
	e, err := s.storer.FindExam(ctx, id, true)
	if err != nil {
		return modelExam.ExamEntity{}, err
	}
	if !canEdit(p, e) {
		return modelExam.ExamEntity{}, errr.WithMessage(errr.ErrForbidden, "only the creator of the exam can change it")
	}
	return e, nil
}

func canRead(p modelAuth.Principal) bool {
	return p.Has(modelAuth.PermExamRead) || p.Has(modelAuth.PermExamWrite)
}

func canEdit(p modelAuth.Principal, e modelExam.ExamEntity) bool {
	return p.Has(modelAuth.PermExamWrite) && (p.Has(modelAuth.PermTenantManage) || (e.CreatedBy != nil && *e.CreatedBy == p.UserID))
}

func (s *ExamService) audit(ctx context.Context, p modelAuth.Principal, client modelAudit.Client, action, id string, before, after any) error {
	return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
		TenantID: p.TenantID, ActorID: p.UserID, Action: action, EntityType: "exam", EntityID: id,
		Before: before, After: after, Client: client,
	}))
}

// examSettings strips the structure from an ExamDTO for audit entries.
func examSettings(d modelExam.ExamDTO) modelExam.ExamDTO {
	d.Sections = nil
	return d
}

// richBody sanitizes HTML into {"body": ...}; "" → nil.
func richBody(html, field string, v errr.Validation) []byte {
	out, err := richtext.Sanitize(html)
	if err != nil {
		v.Check(false, field, err.Error())
		return nil
	}
	if richtext.IsBlank(out) {
		return nil
	}
	return mustJSON(map[string]string{"body": out})
}

func bodyOf(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	var c struct {
		Body string `json:"body"`
	}
	if json.Unmarshal(b, &c) != nil || c.Body == "" {
		return nil
	}
	return &c.Body
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string { return uuid.Must(uuid.NewV7()).String() }
