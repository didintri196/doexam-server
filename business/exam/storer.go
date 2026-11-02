package exam

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelExam "doexam/models/exam"
)

// ExamRow is an exam with its section count (list).
type ExamRow struct {
	modelExam.ExamEntity
	SectionCount int64
}

// SectionItemRow is a section item joined with its item and version
// (the pinned version once published, the current version before).
type SectionItemRow struct {
	modelExam.SectionItemEntity
	BankID        string
	Code          *string
	Type          string
	ItemStatus    string
	VersionID     string
	VersionNo     int
	SearchText    *string
	DefaultPoints float64
}

// ItemRef is the minimum needed to add an item to an exam.
type ItemRef struct {
	ID     string
	BankID string
	Status string
}

// Candidate is an item version a blueprint rule may draw.
type Candidate struct {
	ItemID    string
	VersionID string
}

// ExamStorer is the persistence port for exams. Every method must run inside TxManager.WithTenant.
type ExamStorer interface {
	ListExams(ctx context.Context, f modelExam.ListFilter, userID string) ([]ExamRow, int64, error)
	FindExam(ctx context.Context, id string, forUpdate bool) (modelExam.ExamEntity, error)
	CreateExam(ctx context.Context, e *modelExam.ExamEntity) error
	UpdateExam(ctx context.Context, e *modelExam.ExamEntity) error
	SoftDeleteExam(ctx context.Context, id string) error
	SubjectExists(ctx context.Context, id string) (bool, error)
	// CountLiveSessions counts sessions of the exam that are not cancelled.
	CountLiveSessions(ctx context.Context, examID string) (int64, error)

	ListSections(ctx context.Context, examID string) ([]modelExam.SectionEntity, error)
	FindSection(ctx context.Context, id string) (modelExam.SectionEntity, error)
	CreateSection(ctx context.Context, s *modelExam.SectionEntity) error
	UpdateSection(ctx context.Context, s *modelExam.SectionEntity) error
	DeleteSection(ctx context.Context, id string) error
	SetSectionOrder(ctx context.Context, id string, order int) error

	ListSectionItems(ctx context.Context, examID string) ([]SectionItemRow, error)
	FindSectionItem(ctx context.Context, id string) (modelExam.SectionItemEntity, error)
	FindItems(ctx context.Context, ids []string) ([]ItemRef, error)
	CreateSectionItems(ctx context.Context, items []modelExam.SectionItemEntity) error
	UpdateSectionItem(ctx context.Context, it *modelExam.SectionItemEntity) error
	DeleteSectionItem(ctx context.Context, id string) error
	SetSectionItemOrder(ctx context.Context, id string, order int) error

	ListRules(ctx context.Context, examID string) ([]modelExam.RuleEntity, error)
	FindRule(ctx context.Context, id string) (modelExam.RuleEntity, error)
	CreateRule(ctx context.Context, r *modelExam.RuleEntity) error
	UpdateRule(ctx context.Context, r *modelExam.RuleEntity) error
	DeleteRule(ctx context.Context, id string) error
	// RuleCandidates returns active items (current versions) matching r, excluding excludeItemIDs.
	RuleCandidates(ctx context.Context, r modelExam.RuleEntity, excludeItemIDs []string) ([]Candidate, error)
	PoolSize(ctx context.Context, ruleID string) (int64, error)
	BankSubject(ctx context.Context, bankID string) (string, error) // "" = bank not found
	CompetencyInSubject(ctx context.Context, competencyID, subjectID string) (bool, error)
	BankMemberAccess(ctx context.Context, bankID, userID string) (string, error) // "" = none

	// PinSectionItems sets item_version_id = current version for every hand-picked item of the exam.
	PinSectionItems(ctx context.Context, examID string) error
	InsertPool(ctx context.Context, rows []modelExam.RulePoolEntity) error
	// LockVersions sets locked_at on every pinned/pooled item version of the exam and their stimulus versions.
	LockVersions(ctx context.Context, examID string, at time.Time) error
	// ClearPins undoes PinSectionItems and InsertPool (versions stay locked).
	ClearPins(ctx context.Context, examID string) error

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
