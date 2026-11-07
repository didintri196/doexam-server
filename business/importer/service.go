package importer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	modelMedia "doexam/models/media"
	"doexam/pkg/errr"
	"doexam/pkg/securetoken"
	"doexam/pkg/spreadsheet"
)

const (
	// temporaryPasswordLen matches the user module (≈ 57 bits of entropy).
	temporaryPasswordLen = 10
	// hashWorkers bounds goroutines feeding the hasher (the hasher has its own limit).
	hashWorkers = 8
)

var (
	errJobNotFound    = errr.WithMessage(errr.ErrNotFound, "import not found")
	errNoCredentials  = errr.WithMessage(errr.ErrNotFound, "credentials expired or already downloaded")
	errUnsupportedKnd = &errr.ValidationError{Fields: map[string]string{"kind": "must be users or groups"}}
)

// ImportService imports users and groups from .xlsx/.csv files.
//
// Upload stores the file and validates every row (status ready or failed, with a
// per-row error report). Commit claims a ready job and imports it in the
// background — hashing hundreds of passwords takes seconds — inside ONE
// transaction: either every row is imported or none. Generated passwords are kept
// only in memory (CredentialVault) and can be downloaded once.
type ImportService struct {
	storer  ImportStorer
	tx      TxManager
	files   FileStore
	hasher  PasswordHasher
	vault   CredentialVault
	slot    chan struct{} // one import at a time per instance
	wg      sync.WaitGroup
	now     func() time.Time
	tracer  trace.Tracer
	logger  *slog.Logger
	metrics *importMetrics // nil in tests that build the struct directly
}

// NewImportService returns a concrete ImportService.
func NewImportService(storer ImportStorer, tx TxManager, files FileStore, hasher PasswordHasher, vault CredentialVault, logger *slog.Logger) *ImportService {
	return &ImportService{
		storer:  storer,
		tx:      tx,
		files:   files,
		hasher:  hasher,
		vault:   vault,
		slot:    make(chan struct{}, 1),
		now:     time.Now,
		tracer:  otel.Tracer("doexam.service.importer"),
		logger:  logger,
		metrics: newImportMetrics(),
	}
}

// Template returns the template for kind after checking permission.
func (s *ImportService) Template(p modelAuth.Principal, kind modelImport.Kind) (spreadsheet.Template, error) {
	if err := authorize(p, kind); err != nil {
		return spreadsheet.Template{}, err
	}
	return Template(kind), nil
}

// Upload stores the file, validates all rows and returns the job (ready or failed).
// A file that cannot be read at all is rejected with a validation error and no job.
// target narrows a user import to staff, participants, or participants of one group.
func (s *ImportService) Upload(ctx context.Context, p modelAuth.Principal, kind modelImport.Kind, target modelImport.Target, filename string, data []byte, client modelAudit.Client) (modelImport.JobDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ImportService.Upload", trace.WithAttributes(attribute.String("import.kind", string(kind))))
	defer span.End()

	target, err := normalizeTarget(kind, target)
	if err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	if err := authorizeTarget(p, kind, target); err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	if len(data) == 0 || len(data) > modelImport.MaxFileBytes {
		return modelImport.JobDTO{}, fail(span, &errr.ValidationError{Fields: map[string]string{"file": "required, max 5 MB"}})
	}
	format, err := spreadsheet.DetectFormat(filename, data)
	if err != nil {
		return modelImport.JobDTO{}, fail(span, &errr.ValidationError{Fields: map[string]string{"file": err.Error()}})
	}
	sheet, err := spreadsheet.Read(data, format, modelImport.MaxRows)
	if err != nil {
		return modelImport.JobDTO{}, fail(span, &errr.ValidationError{Fields: map[string]string{"file": err.Error()}})
	}
	if target.GroupID != "" {
		if err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
			_, err := s.storer.FindGroup(ctx, target.GroupID)
			return err
		}); err != nil {
			return modelImport.JobDTO{}, fail(span, err)
		}
	}

	now := s.now().UTC()
	file := modelMedia.MediaFileEntity{
		ID: newID(), TenantID: p.TenantID, OriginalName: modelAudit.Truncate(path.Base(filename), 255),
		MimeType: mimeOf(format), SizeBytes: int64(len(data)), UploadedBy: &p.UserID, CreatedAt: now,
	}
	sum := sha256.Sum256(data)
	file.SHA256 = sum[:]
	file.StorageKey = fmt.Sprintf("%s/imports/%s.%s", p.TenantID, file.ID, format)
	if err := s.files.Put(ctx, file.StorageKey, data); err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}

	job := modelImport.ImportJobEntity{
		ID: newID(), TenantID: p.TenantID, Kind: kind, SourceFileID: file.ID,
		Status: modelImport.StatusValidating, Errors: modelImport.EncodeErrors(nil),
		Target: target.Encode(), Summary: modelImport.Summary{}.Encode(),
		CreatedBy: &p.UserID, CreatedAt: now,
	}
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.storer.CreateMediaFile(ctx, &file); err != nil {
			return err
		}
		if err := s.storer.CreateJob(ctx, &job); err != nil {
			return err
		}
		valid, errs, warns, err := s.validate(ctx, p.TenantID, kind, target, sheet)
		if err != nil {
			return err
		}
		job.TotalRows, job.OkRows = len(sheet.Rows), valid
		job.ErrorRows = len(sheet.Rows) - valid
		job.Errors = modelImport.EncodeErrors(sortErrors(append(errs, warns...)))
		job.Status = modelImport.StatusReady
		if len(errs) > 0 || len(sheet.Rows) == 0 {
			job.Status = modelImport.StatusFailed
			job.FinishedAt = &now
		}
		if err := s.storer.UpdateJob(ctx, &job); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionImportUpload,
			EntityType: "import_job", EntityID: job.ID, Client: client,
			After: map[string]any{"kind": kind, "target": target, "file": file.OriginalName, "rows": job.TotalRows, "status": job.Status},
		}))
	})
	if err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	return modelImport.ToDTO(modelImport.JobRow{ImportJobEntity: job, FileName: file.OriginalName}, false), nil
}

// normalizeTarget validates a target; a group target is always a participant import.
func normalizeTarget(kind modelImport.Kind, t modelImport.Target) (modelImport.Target, error) {
	t.GroupID = strings.ToLower(strings.TrimSpace(t.GroupID))
	if kind != modelImport.KindUsers {
		if t != (modelImport.Target{}) {
			return t, &errr.ValidationError{Fields: map[string]string{"audience": "only for user imports"}}
		}
		return t, nil
	}
	if t.GroupID != "" {
		if uuid.Validate(t.GroupID) != nil {
			return t, errr.WithMessage(errr.ErrNotFound, "group not found")
		}
		if t.Audience == "" {
			t.Audience = modelImport.AudienceParticipant
		}
	}
	v := errr.Validation{}
	v.Check(t.Audience == "" || t.Audience == modelImport.AudienceStaff || t.Audience == modelImport.AudienceParticipant, "audience", "must be staff or participant")
	v.Check(t.GroupID == "" || t.Audience == modelImport.AudienceParticipant, "group_id", "only for participant imports")
	return t, v.Err()
}

// Get returns one job.
func (s *ImportService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelImport.JobDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ImportService.Get", trace.WithAttributes(attribute.String("import.id", id)))
	defer span.End()

	job, err := s.findJob(ctx, p, id)
	if err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	return modelImport.ToDTO(job, s.vault.Has(vaultKey(p.TenantID, id))), nil
}

// List returns one page of jobs of the kinds the caller may see, newest first.
func (s *ImportService) List(ctx context.Context, p modelAuth.Principal, f modelImport.ListFilter) ([]modelImport.JobDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "ImportService.List")
	defer span.End()

	var kinds []modelImport.Kind
	for _, k := range []modelImport.Kind{modelImport.KindUsers, modelImport.KindGroups} {
		if (f.Kind == "" || f.Kind == k) && authorize(p, k) == nil {
			kinds = append(kinds, k)
		}
	}
	if f.Kind != "" && f.Kind != modelImport.KindUsers && f.Kind != modelImport.KindGroups {
		return nil, 0, errUnsupportedKnd
	}
	if f.Audience != "" || f.GroupID != "" {
		t, err := normalizeTarget(modelImport.KindUsers, modelImport.Target{Audience: f.Audience, GroupID: f.GroupID})
		if err != nil {
			return nil, 0, fail(span, err)
		}
		if err := authorizeTarget(p, modelImport.KindUsers, t); err != nil {
			return nil, 0, fail(span, err)
		}
		f.Audience, f.GroupID, kinds = t.Audience, t.GroupID, []modelImport.Kind{modelImport.KindUsers}
	}
	if len(kinds) == 0 {
		return nil, 0, fail(span, errr.ErrForbidden)
	}

	var (
		rows  []modelImport.JobRow
		total int64
	)
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		rows, total, err = s.storer.ListJobs(ctx, kinds, f)
		return err
	})
	if err != nil {
		return nil, 0, fail(span, err)
	}
	out := make([]modelImport.JobDTO, 0, len(rows))
	for _, j := range rows {
		dto := modelImport.ToDTO(j, s.vault.Has(vaultKey(p.TenantID, j.ID)))
		dto.Errors = nil // keep the list light; Get returns the report
		out = append(out, dto)
	}
	return out, total, nil
}

// Commit starts importing a ready job in the background and returns it with status importing.
func (s *ImportService) Commit(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) (modelImport.JobDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ImportService.Commit", trace.WithAttributes(attribute.String("import.id", id)))
	defer span.End()

	job, err := s.findJob(ctx, p, id)
	if err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	var claimed bool
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		claimed, err = s.storer.ClaimJob(ctx, id)
		return err
	})
	if err != nil {
		return modelImport.JobDTO{}, fail(span, err)
	}
	if !claimed {
		return modelImport.JobDTO{}, fail(span, errr.WithMessage(errr.ErrConflict,
			fmt.Sprintf("import is not ready (status: %s)", job.Status)))
	}
	job.Status = modelImport.StatusImporting

	// Detach from the request: the work outlives it, but keeps the trace.
	bg := context.WithoutCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(bg, p, job, client)
	}()
	return modelImport.ToDTO(job, false), nil
}

// Credentials returns (once) the passwords of users created by a finished import.
func (s *ImportService) Credentials(ctx context.Context, p modelAuth.Principal, id string) ([]modelImport.Credential, error) {
	ctx, span := s.tracer.Start(ctx, "ImportService.Credentials", trace.WithAttributes(attribute.String("import.id", id)))
	defer span.End()

	if _, err := s.findJob(ctx, p, id); err != nil {
		return nil, fail(span, err)
	}
	creds, ok := s.vault.Take(vaultKey(p.TenantID, id))
	if !ok {
		return nil, fail(span, errNoCredentials)
	}
	s.logger.InfoContext(ctx, "import credentials downloaded",
		slog.String("tenant_id", p.TenantID), slog.String("import_id", id), slog.String("user_id", p.UserID))
	return creds, nil
}

// Shutdown waits for running imports to finish, or for ctx to end.
func (s *ImportService) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("imports still running: %w", ctx.Err())
	}
}

// run performs a claimed import. Every outcome is written back to the job.
func (s *ImportService) run(ctx context.Context, p modelAuth.Principal, job modelImport.JobRow, client modelAudit.Client) {
	ctx, span := s.tracer.Start(ctx, "ImportService.run", trace.WithAttributes(attribute.String("import.id", job.ID)))
	defer span.End()

	kind := string(job.Kind)
	queued := time.Now()
	s.slot <- struct{}{}
	defer func() { <-s.slot }()
	s.metrics.queued(ctx, kind, queued)

	logger := s.logger.With(slog.String("tenant_id", p.TenantID), slog.String("import_id", job.ID))
	start := time.Now()
	imported, creds, rowErrs, err := s.importFile(ctx, p, job, client)
	result := "done"
	defer func() { s.metrics.done(ctx, kind, result, start, imported) }()
	now := s.now().UTC()
	job.FinishedAt = &now
	switch {
	case err != nil:
		result, imported = "failed", 0
		_ = fail(span, err)
		logger.ErrorContext(ctx, "import failed", slog.String("error", err.Error()))
		msg := "import failed, please try again"
		if errors.Is(err, errr.ErrConflict) {
			msg = "data changed since validation (duplicate found), please upload the file again"
		}
		job.Status, job.Errors = modelImport.StatusFailed, modelImport.EncodeErrors([]modelImport.RowError{{Message: msg}})
	case len(rowErrs) > 0:
		result, imported = "invalid", 0
		job.Status, job.Errors = modelImport.StatusFailed, modelImport.EncodeErrors(rowErrs)
		job.ErrorRows = countRows(rowErrs)
		logger.WarnContext(ctx, "import failed revalidation", slog.Int("error_rows", job.ErrorRows))
	default:
		// the job row was already marked done inside the import transaction
		if len(creds) > 0 {
			s.vault.Put(vaultKey(p.TenantID, job.ID), creds)
		}
		logger.InfoContext(ctx, "import done", slog.Int("rows", imported))
		return
	}
	if err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		return s.storer.UpdateJob(ctx, &job.ImportJobEntity)
	}); err != nil {
		logger.ErrorContext(ctx, "failed to record import failure", slog.String("error", err.Error()))
	}
}

// importFile re-reads and re-validates the stored file (the database may have changed
// since upload), then writes everything and marks the job done in one transaction.
func (s *ImportService) importFile(ctx context.Context, p modelAuth.Principal, job modelImport.JobRow, client modelAudit.Client) (int, []modelImport.Credential, []modelImport.RowError, error) {
	data, err := s.files.Get(ctx, job.StorageKey)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read source file: %w", err)
	}
	format, err := spreadsheet.DetectFormat(job.FileName, data)
	if err != nil {
		format = spreadsheet.FormatXLSX
		if strings.HasSuffix(job.StorageKey, ".csv") {
			format = spreadsheet.FormatCSV
		}
	}
	sheet, err := spreadsheet.Read(data, format, modelImport.MaxRows)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("parse source file: %w", err)
	}

	// Validation outside the write transaction so the slow hashing in between holds no connection.
	target := modelImport.DecodeTarget(job.Target)
	var (
		batch  userBatch
		groups []modelGroup.GroupEntity
		errs   []modelImport.RowError
	)
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		if job.Kind == modelImport.KindUsers {
			batch, errs, err = s.planUsers(ctx, p.TenantID, target, sheet.Rows)
		} else {
			groups, errs, err = s.planGroups(ctx, p.TenantID, sheet.Rows)
		}
		return err
	})
	if err != nil || len(errs) > 0 {
		return 0, nil, sortErrors(errs), err
	}

	var creds []modelImport.Credential
	if job.Kind == modelImport.KindUsers {
		if creds, err = s.prepareUsers(ctx, batch.create); err != nil {
			return 0, nil, nil, err
		}
	}

	imported := batch.valid() + len(groups)
	summary := modelImport.Summary{Created: len(batch.create) + len(groups)}
	now := s.now().UTC()
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var after map[string]any
		if job.Kind == modelImport.KindUsers {
			ids, err := s.writeUsers(ctx, p.TenantID, batch.create, now)
			if err != nil {
				return err
			}
			summary.AlreadyMember = batch.alreadyMember
			if target.GroupID != "" && len(batch.addExisting) > 0 {
				members := make([]modelGroup.GroupMemberEntity, len(batch.addExisting))
				for i, id := range batch.addExisting {
					members[i] = modelGroup.GroupMemberEntity{GroupID: target.GroupID, UserID: id, TenantID: p.TenantID, MemberRole: modelGroup.MemberRoleMember, JoinedAt: now}
				}
				n, err := s.storer.AddMembers(ctx, members)
				if err != nil {
					return err
				}
				// someone added by hand since validation counts as already a member
				summary.Added, summary.AlreadyMember = int(n), summary.AlreadyMember+len(members)-int(n)
			}
			after = map[string]any{"kind": job.Kind, "target": target, "created": len(ids), "user_ids": ids,
				"added": summary.Added, "added_user_ids": batch.addExisting, "already_member": summary.AlreadyMember}
		} else {
			if err := s.storer.CreateGroups(ctx, groups); err != nil {
				return err
			}
			ids := make([]string, len(groups))
			for i, g := range groups {
				ids[i] = g.ID
			}
			after = map[string]any{"kind": job.Kind, "created": len(ids), "group_ids": ids}
		}
		job.Status, job.OkRows, job.ErrorRows, job.FinishedAt = modelImport.StatusDone, imported, 0, &now
		job.Errors = modelImport.EncodeErrors(sortErrors(batch.warnings))
		job.Summary = summary.Encode()
		if err := s.storer.UpdateJob(ctx, &job.ImportJobEntity); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionImportCommit,
			EntityType: "import_job", EntityID: job.ID, After: after, Client: client,
		}))
	})
	if err != nil {
		return 0, nil, nil, err
	}
	return imported, creds, nil, nil
}

// prepareUsers generates missing passwords and hashes all of them in parallel.
// Only generated passwords are returned as credentials.
func (s *ImportService) prepareUsers(ctx context.Context, plans []userPlan) ([]modelImport.Credential, error) {
	var creds []modelImport.Credential
	for i := range plans {
		if plans[i].password == "" {
			pw, err := securetoken.ReadablePassword(temporaryPasswordLen)
			if err != nil {
				return nil, err
			}
			plans[i].password = pw
			plans[i].mustChange = true
			creds = append(creds, modelImport.Credential{
				Row: plans[i].line, Username: plans[i].user.Username, FullName: plans[i].user.FullName,
				Password: pw, Generated: true,
			})
		}
	}

	jobs := make(chan int)
	errCh := make(chan error, hashWorkers)
	var wg sync.WaitGroup
	for range hashWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				hash, err := s.hasher.Hash(ctx, plans[i].password)
				if err != nil {
					errCh <- err
					return
				}
				plans[i].user.PasswordHash = &hash
				plans[i].password = "" // drop the plaintext as soon as possible
			}
		}()
	}
	var hashErr error
feed:
	for i := range plans {
		select {
		case jobs <- i:
		case hashErr = <-errCh:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if hashErr == nil && len(errCh) > 0 {
		hashErr = <-errCh
	}
	if hashErr != nil {
		return nil, fmt.Errorf("hash passwords: %w", hashErr)
	}
	return creds, nil
}

func (s *ImportService) writeUsers(ctx context.Context, tenantID string, plans []userPlan, now time.Time) ([]string, error) {
	users := make([]modelAuth.UserEntity, len(plans))
	var links []modelAuth.UserRoleEntity
	var members []modelGroup.GroupMemberEntity
	ids := make([]string, len(plans))
	for i, pl := range plans {
		u := pl.user
		u.ID, u.MustChangePassword, u.CreatedAt = newID(), pl.mustChange, now
		users[i], ids[i] = u, u.ID
		for _, rid := range pl.roleIDs {
			links = append(links, modelAuth.UserRoleEntity{UserID: u.ID, RoleID: rid, TenantID: tenantID})
		}
		for _, gid := range pl.groupIDs {
			members = append(members, modelGroup.GroupMemberEntity{
				GroupID: gid, UserID: u.ID, TenantID: tenantID, MemberRole: modelGroup.MemberRoleMember, JoinedAt: now,
			})
		}
	}
	if err := s.storer.CreateUsers(ctx, users); err != nil {
		return nil, err
	}
	if len(links) > 0 {
		if err := s.storer.CreateUserRoles(ctx, links); err != nil {
			return nil, err
		}
	}
	if len(members) > 0 {
		if err := s.storer.CreateMembers(ctx, members); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// validate checks headers and rows; it returns the number of valid rows, the errors
// and the warnings.
func (s *ImportService) validate(ctx context.Context, tenantID string, kind modelImport.Kind, target modelImport.Target, sheet spreadsheet.Sheet) (int, []modelImport.RowError, []modelImport.RowError, error) {
	required := []string{colUsername, colFullName}
	if kind == modelImport.KindGroups {
		required = []string{colCode, colName, colKind}
	}
	var missing []string
	for _, c := range required {
		if !slices.Contains(sheet.Headers, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return 0, []modelImport.RowError{{Row: 0, Message: "missing columns: " + strings.Join(missing, ", ")}}, nil, nil
	}

	var (
		valid int
		errs  []modelImport.RowError
		warns []modelImport.RowError
		err   error
	)
	if kind == modelImport.KindUsers {
		var batch userBatch
		batch, errs, err = s.planUsers(ctx, tenantID, target, sheet.Rows)
		valid, warns = batch.valid(), batch.warnings
	} else {
		var groups []modelGroup.GroupEntity
		groups, errs, err = s.planGroups(ctx, tenantID, sheet.Rows)
		valid = len(sheet.Rows) - countRows(errs)
		if errs == nil {
			valid = len(groups)
		}
	}
	return valid, sortErrors(errs), warns, err
}

func (s *ImportService) findJob(ctx context.Context, p modelAuth.Principal, id string) (modelImport.JobRow, error) {
	if uuid.Validate(id) != nil {
		return modelImport.JobRow{}, errJobNotFound
	}
	var job modelImport.JobRow
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		job, err = s.storer.FindJob(ctx, id)
		return err
	})
	if err != nil {
		return modelImport.JobRow{}, err
	}
	if authorizeTarget(p, job.Kind, modelImport.DecodeTarget(job.Target)) != nil {
		// do not reveal jobs of a kind the caller cannot manage
		return modelImport.JobRow{}, errJobNotFound
	}
	return job, nil
}

// authorize: user imports need user.manage, group imports need group.manage.
func authorize(p modelAuth.Principal, kind modelImport.Kind) error {
	switch kind {
	case modelImport.KindUsers:
		if p.Has(modelAuth.PermUserManage) {
			return nil
		}
	case modelImport.KindGroups:
		if p.Has(modelAuth.PermGroupManage) {
			return nil
		}
	default:
		return errUnsupportedKnd
	}
	return errr.ErrForbidden
}

// authorizeTarget adds group.manage for imports into a group (they change members).
func authorizeTarget(p modelAuth.Principal, kind modelImport.Kind, t modelImport.Target) error {
	if err := authorize(p, kind); err != nil {
		return err
	}
	if t.GroupID != "" && !p.Has(modelAuth.PermGroupManage) {
		return errr.ErrForbidden
	}
	return nil
}

// rowErrors collects RowError values.
type rowErrors []modelImport.RowError

func (e *rowErrors) add(row int, field, msg string) {
	*e = append(*e, modelImport.RowError{Row: row, Field: field, Message: msg})
}

func (e *rowErrors) check(cond bool, row int, field, msg string) {
	if !cond {
		e.add(row, field, msg)
	}
}

func sortErrors(errs []modelImport.RowError) []modelImport.RowError {
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Row < errs[j].Row })
	return errs
}

// countRows counts distinct rows with at least one error.
func countRows(errs []modelImport.RowError) int {
	rows := map[int]bool{}
	for _, e := range errs {
		rows[e.Row] = true
	}
	return len(rows)
}

func vaultKey(tenantID, jobID string) string { return tenantID + ":" + jobID }

func mimeOf(f spreadsheet.Format) string {
	if f == spreadsheet.FormatCSV {
		return "text/csv"
	}
	return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
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
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}
