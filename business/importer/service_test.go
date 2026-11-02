package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	modelMedia "doexam/models/media"
	"doexam/pkg/errr"
	"doexam/pkg/storage"
	"doexam/pkg/ttlcache"
)

const tenantID = "0192a000-0000-7000-8000-000000000001"

func ptr[T any](v T) *T { return &v }

// fakeStore is an in-memory ImportStorer.
type fakeStore struct {
	mu        sync.Mutex
	files     map[string]modelMedia.MediaFileEntity
	jobs      map[string]modelImport.ImportJobEntity
	usernames []string
	emails    []string
	extIDs    []string
	roles     []modelAuth.RoleEntity
	groups    []modelGroup.GroupEntity
	users     []modelAuth.UserEntity
	links     []modelAuth.UserRoleEntity
	members   []modelGroup.GroupMemberEntity
	audits    []string
}

func newStore() *fakeStore {
	return &fakeStore{
		files:     map[string]modelMedia.MediaFileEntity{},
		jobs:      map[string]modelImport.ImportJobEntity{},
		usernames: []string{"admin"},
		emails:    []string{"Admin@Sekolah.id"},
		extIDs:    []string{"999"},
		roles: []modelAuth.RoleEntity{
			{ID: "r-participant", Code: "participant"}, {ID: "r-author", Code: "author"},
		},
		groups: []modelGroup.GroupEntity{
			{ID: "g-x", Code: ptr("X"), Name: "Kelas X", Kind: "level", Period: ptr("2026/2027")},
			{ID: "g-ipa1", Code: ptr("X-IPA-1"), Name: "X IPA 1", Kind: "class", Period: ptr("2026/2027"), ParentID: ptr("g-x")},
			{ID: "g-old", Code: ptr("DUP"), Kind: "class", Period: ptr("2025/2026")},
			{ID: "g-new", Code: ptr("DUP"), Kind: "class", Period: ptr("2026/2027")},
		},
	}
}

func (f *fakeStore) CreateMediaFile(_ context.Context, m *modelMedia.MediaFileEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[m.ID] = *m
	return nil
}
func (f *fakeStore) CreateJob(_ context.Context, j *modelImport.ImportJobEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[j.ID] = *j
	return nil
}
func (f *fakeStore) FindJob(_ context.Context, id string) (modelImport.JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return modelImport.JobRow{}, errJobNotFound
	}
	m := f.files[j.SourceFileID]
	return modelImport.JobRow{ImportJobEntity: j, FileName: m.OriginalName, StorageKey: m.StorageKey}, nil
}
func (f *fakeStore) ListJobs(_ context.Context, kinds []modelImport.Kind, _ modelImport.ListFilter) ([]modelImport.JobRow, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []modelImport.JobRow
	for _, j := range f.jobs {
		if slices.Contains(kinds, j.Kind) {
			out = append(out, modelImport.JobRow{ImportJobEntity: j})
		}
	}
	return out, int64(len(out)), nil
}
func (f *fakeStore) UpdateJob(_ context.Context, j *modelImport.ImportJobEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[j.ID] = *j
	return nil
}
func (f *fakeStore) ClaimJob(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[id]
	if j.Status != modelImport.StatusReady {
		return false, nil
	}
	j.Status = modelImport.StatusImporting
	f.jobs[id] = j
	return true, nil
}
func existingIn(have, values []string, fold bool) []string {
	var out []string
	for _, v := range values {
		for _, h := range have {
			if h == v || (fold && strings.EqualFold(h, v)) {
				out = append(out, h)
			}
		}
	}
	return out
}
func (f *fakeStore) ExistingUsernames(_ context.Context, v []string) ([]string, error) {
	return existingIn(f.usernames, v, true), nil
}
func (f *fakeStore) ExistingEmails(_ context.Context, v []string) ([]string, error) {
	return existingIn(f.emails, v, true), nil
}
func (f *fakeStore) ExistingExternalIDs(_ context.Context, v []string) ([]string, error) {
	return existingIn(f.extIDs, v, false), nil
}
func (f *fakeStore) RolesByCodes(_ context.Context, codes []string) ([]modelAuth.RoleEntity, error) {
	var out []modelAuth.RoleEntity
	for _, r := range f.roles {
		if slices.Contains(codes, r.Code) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) GroupsByCodes(_ context.Context, codes []string) ([]modelGroup.GroupEntity, error) {
	var out []modelGroup.GroupEntity
	for _, g := range f.groups {
		if slices.Contains(codes, *g.Code) {
			out = append(out, g)
		}
	}
	return out, nil
}
func (f *fakeStore) CreateUsers(_ context.Context, u []modelAuth.UserEntity) error {
	f.users = append(f.users, u...)
	for _, x := range u {
		f.usernames = append(f.usernames, x.Username)
	}
	return nil
}
func (f *fakeStore) CreateUserRoles(_ context.Context, l []modelAuth.UserRoleEntity) error {
	f.links = append(f.links, l...)
	return nil
}
func (f *fakeStore) CreateMembers(_ context.Context, m []modelGroup.GroupMemberEntity) error {
	f.members = append(f.members, m...)
	return nil
}
func (f *fakeStore) CreateGroups(_ context.Context, g []modelGroup.GroupEntity) error {
	f.groups = append(f.groups, g...)
	return nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, l.Action)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }

var (
	adminP     = modelAuth.Principal{TenantID: tenantID, UserID: "0192a000-0000-7000-8000-0000000000a1", Permissions: []string{modelAuth.PermUserManage, modelAuth.PermGroupManage}}
	groupsOnly = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermGroupManage}}
)

func newService(t *testing.T, store *fakeStore) *ImportService {
	t.Helper()
	files, err := storage.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewImportService(store, fakeTx{}, files, fakeHasher{}, ttlcache.New[[]modelImport.Credential](time.Hour),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func upload(t *testing.T, svc *ImportService, kind modelImport.Kind, csv string) modelImport.JobDTO {
	t.Helper()
	dto, err := svc.Upload(context.Background(), adminP, kind, "data.csv", []byte(csv), modelAudit.Client{})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return dto
}

// waitDone commits and waits for the background import.
func commit(t *testing.T, svc *ImportService, id string) modelImport.JobDTO {
	t.Helper()
	if _, err := svc.Commit(context.Background(), adminP, id, modelAudit.Client{}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	dto, err := svc.Get(context.Background(), adminP, id)
	if err != nil {
		t.Fatal(err)
	}
	return dto
}

func hasError(errs []modelImport.RowError, row int, field, contains string) bool {
	return slices.ContainsFunc(errs, func(e modelImport.RowError) bool {
		return e.Row == row && e.Field == field && strings.Contains(e.Message, contains)
	})
}

func TestUserImportEndToEnd(t *testing.T) {
	store := newStore()
	svc := newService(t, store)
	csv := "username*;full_name*;email;external_id;password;roles;groups;must_change_password\n" +
		"0051234567;Budi;;0051234567;;;X-IPA-1;\n" +
		"bu.ani;Ani;ani@x.id;;guruguru1;author, participant;;tidak\n"

	job := upload(t, svc, modelImport.KindUsers, csv)
	if job.Status != modelImport.StatusReady || job.TotalRows != 2 || job.OkRows != 2 {
		t.Fatalf("after upload: %+v", job)
	}
	done := commit(t, svc, job.ID)
	if done.Status != modelImport.StatusDone || done.OkRows != 2 || !done.CredentialsAvailable {
		t.Fatalf("after commit: %+v", done)
	}
	if len(store.users) != 2 || len(store.links) != 3 || len(store.members) != 1 || store.members[0].GroupID != "g-ipa1" {
		t.Fatalf("users=%d links=%d members=%+v", len(store.users), len(store.links), store.members)
	}
	budi, ani := store.users[0], store.users[1]
	if !budi.MustChangePassword || ani.MustChangePassword || *ani.PasswordHash != "hash:guruguru1" {
		t.Fatalf("budi must=%v ani must=%v ani hash=%v", budi.MustChangePassword, ani.MustChangePassword, *ani.PasswordHash)
	}

	creds, err := svc.Credentials(context.Background(), adminP, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].Username != "0051234567" || *budi.PasswordHash != "hash:"+creds[0].Password {
		t.Fatalf("only the generated password is returned: %+v", creds)
	}
	if _, err := svc.Credentials(context.Background(), adminP, job.ID); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("second download: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Commit(context.Background(), adminP, job.ID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("commit twice: err = %v, want ErrConflict", err)
	}
	if !slices.Equal(store.audits, []string{modelAudit.ActionImportUpload, modelAudit.ActionImportCommit}) {
		t.Fatalf("audits = %v", store.audits)
	}
}

func TestUserImportValidation(t *testing.T) {
	store := newStore()
	svc := newService(t, store)
	csv := strings.Join([]string{
		"username,full_name,email,external_id,password,roles,groups,must_change_password",
		"ADMIN,Dup DB,,,,,,",                        // 2: username exists (case-insensitive)
		"siswa1,Siswa,admin@sekolah.ID,999,,,,",     // 3: email + external_id exist
		"siswa2,Siswa,,,123,superuser,NOPE,mungkin", // 4: short password, unknown role, unknown group, bad bool
		"Siswa2,Siswa,,,,,DUP,",                     // 5: duplicate username in file, ambiguous group
		"a b,,,,,,,",                                // 6: bad username, missing name
		"ok1,Valid,,,,,,",                           // 7: valid
	}, "\n")
	job := upload(t, svc, modelImport.KindUsers, csv)

	if job.Status != modelImport.StatusFailed || job.TotalRows != 6 || job.ErrorRows != 5 || job.OkRows != 1 {
		t.Fatalf("job = %+v", job)
	}
	for _, want := range []struct {
		row          int
		field, match string
	}{
		{2, colUsername, "already in use"},
		{3, colEmail, "already in use"},
		{3, colExternalID, "already in use"},
		{4, colPassword, "characters"},
		{4, colRoles, "unknown role: superuser"},
		{4, colGroups, "unknown group: NOPE"},
		{4, colMustChange, "ya or tidak"},
		{5, colUsername, "duplicate of row 4"},
		{5, colGroups, "ambiguous"},
		{6, colUsername, ""},
		{6, colFullName, "required"},
	} {
		if !hasError(job.Errors, want.row, want.field, want.match) {
			t.Errorf("missing error row %d %s %q in %+v", want.row, want.field, want.match, job.Errors)
		}
	}
	if _, err := svc.Commit(context.Background(), adminP, job.ID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("commit failed job: err = %v, want ErrConflict", err)
	}
}

func TestUploadRejectsBadFiles(t *testing.T) {
	svc := newService(t, newStore())
	ctx := context.Background()
	cases := map[string]struct {
		kind modelImport.Kind
		name string
		data string
		want error
	}{
		"xls":       {modelImport.KindUsers, "a.xls", "x", errr.ErrInvalidInput},
		"empty":     {modelImport.KindUsers, "a.csv", "", errr.ErrInvalidInput},
		"bad kind":  {"items", "a.csv", "a\n1", errr.ErrInvalidInput},
		"forbidden": {modelImport.KindUsers, "a.csv", "a\n1", errr.ErrForbidden},
	}
	for name, c := range cases {
		p := adminP
		if name == "forbidden" {
			p = groupsOnly
		}
		if _, err := svc.Upload(ctx, p, c.kind, c.name, []byte(c.data), modelAudit.Client{}); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}

	job := upload(t, svc, modelImport.KindUsers, "nama,kelas\nBudi,X\n")
	if job.Status != modelImport.StatusFailed || !hasError(job.Errors, 0, "", "missing columns: username, full_name") {
		t.Fatalf("missing columns: %+v", job)
	}
}

func TestCommitRevalidates(t *testing.T) {
	store := newStore()
	svc := newService(t, store)
	job := upload(t, svc, modelImport.KindUsers, "username,full_name\nbaru1,Baru\n")

	store.usernames = append(store.usernames, "BARU1") // created meanwhile by someone else
	done := commit(t, svc, job.ID)
	if done.Status != modelImport.StatusFailed || !hasError(done.Errors, 2, colUsername, "already in use") {
		t.Fatalf("job = %+v", done)
	}
	if len(store.users) != 0 || done.CredentialsAvailable {
		t.Fatal("nothing may be imported")
	}
}

func TestGroupImport(t *testing.T) {
	store := newStore()
	svc := newService(t, store)
	// child listed before its parent; parent of XI is an existing group
	csv := "code,name,kind,period,parent_code\n" +
		"XI-IPA-1,XI IPA 1,class,2026/2027,XI\n" +
		"XI,Kelas XI,level,2026/2027,\n" +
		"X-IPA-2,X IPA 2,class,2026/2027,X\n"
	job := upload(t, svc, modelImport.KindGroups, csv)
	if job.Status != modelImport.StatusReady {
		t.Fatalf("job = %+v", job)
	}
	done := commit(t, svc, job.ID)
	if done.Status != modelImport.StatusDone {
		t.Fatalf("done = %+v", done)
	}
	created := store.groups[4:]
	idx := func(code string) int {
		return slices.IndexFunc(created, func(g modelGroup.GroupEntity) bool { return *g.Code == code })
	}
	if idx("XI") > idx("XI-IPA-1") {
		t.Fatal("parents must be inserted before children")
	}
	if *created[idx("XI-IPA-1")].ParentID != created[idx("XI")].ID || *created[idx("X-IPA-2")].ParentID != "g-x" {
		t.Fatal("parent ids not resolved")
	}
}

func TestGroupImportValidation(t *testing.T) {
	svc := newService(t, newStore())
	csv := strings.Join([]string{
		"code,name,kind,period,parent_code",
		"X,Kelas X,level,2026/2027,", // 2: exists in DB with same period
		"A,A,class,,B",               // 3: cycle A→B→A
		"B,B,class,,A",               // 4: cycle
		"C,C,kelas,,",                // 5: bad kind
		"D,D,team,,NOPE",             // 6: unknown parent
		"E,E,team,,",                 // 7: ok
		"E,E2,team,,",                // 8: duplicate code+period in file
		"F,F,team,2024,DUP",          // 9: DUP in two other periods → ambiguous
	}, "\n")
	job := upload(t, svc, modelImport.KindGroups, csv)
	if job.Status != modelImport.StatusFailed {
		t.Fatalf("job = %+v", job)
	}
	for _, want := range []struct {
		row          int
		field, match string
	}{
		{2, colCode, "already exists"},
		{3, colParentCode, "cycle"},
		{4, colParentCode, "cycle"},
		{5, colKind, "must be"},
		{6, colParentCode, "not found"},
		{8, colCode, "duplicate of row 7"},
		{9, colParentCode, "ambiguous"},
	} {
		if !hasError(job.Errors, want.row, want.field, want.match) {
			t.Errorf("missing error row %d %s %q in %+v", want.row, want.field, want.match, job.Errors)
		}
	}
}

func TestJobsOfOtherKindAreHidden(t *testing.T) {
	store := newStore()
	svc := newService(t, store)
	job := upload(t, svc, modelImport.KindUsers, "username,full_name\nbaru1,Baru\n")

	if _, err := svc.Get(context.Background(), groupsOnly, job.ID); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	list, _, err := svc.List(context.Background(), groupsOnly, modelImport.ListFilter{Page: 1, PerPage: 20})
	if err != nil || len(list) != 0 {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func TestEncodeErrorsCaps(t *testing.T) {
	errs := make([]modelImport.RowError, modelImport.MaxStoredErrors+10)
	var decoded []modelImport.RowError
	_ = json.Unmarshal(modelImport.EncodeErrors(errs), &decoded)
	if len(decoded) != modelImport.MaxStoredErrors {
		t.Fatalf("stored %d errors, want %d", len(decoded), modelImport.MaxStoredErrors)
	}
}
