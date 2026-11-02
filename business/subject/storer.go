package subject

import (
	"context"

	modelAudit "doexam/models/audit"
	modelSubject "doexam/models/subject"
)

// SubjectStorer is the persistence port for subjects and competencies.
// Every method must run inside TxManager.WithTenant.
type SubjectStorer interface {
	ListSubjects(ctx context.Context, f modelSubject.ListFilter) ([]modelSubject.SubjectEntity, int64, error)
	FindSubject(ctx context.Context, id string) (modelSubject.SubjectEntity, error)
	CreateSubject(ctx context.Context, s *modelSubject.SubjectEntity) error
	UpdateSubject(ctx context.Context, s *modelSubject.SubjectEntity) error
	SoftDeleteSubject(ctx context.Context, id string) error
	CountBanks(ctx context.Context, subjectID string) (int64, error)

	ListCompetencies(ctx context.Context, subjectID string) ([]modelSubject.CompetencyEntity, error)
	FindCompetency(ctx context.Context, id string) (modelSubject.CompetencyEntity, error)
	CreateCompetency(ctx context.Context, c *modelSubject.CompetencyEntity) error
	UpdateCompetency(ctx context.Context, c *modelSubject.CompetencyEntity) error
	SoftDeleteCompetency(ctx context.Context, id string) error
	CountChildCompetencies(ctx context.Context, id string) (int64, error)
	// CountItemVersionsUsing counts item versions tagged with the competency.
	CountItemVersionsUsing(ctx context.Context, competencyID string) (int64, error)
	// CompetencyAncestorIDs returns parent, grandparent, ... of id.
	CompetencyAncestorIDs(ctx context.Context, id string) ([]string, error)

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
