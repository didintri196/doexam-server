package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	"doexam/pkg/errr"
	"doexam/pkg/mediaurl"
)

const tenantID = "0192a000-0000-7000-8000-000000000001"

type fakeStore struct {
	files map[string]modelMedia.MediaFileEntity
}

func (f *fakeStore) Create(_ context.Context, m *modelMedia.MediaFileEntity) error {
	f.files[m.ID] = *m
	return nil
}
func (f *fakeStore) FindByID(_ context.Context, id string) (modelMedia.MediaFileEntity, error) {
	if m, ok := f.files[id]; ok {
		return m, nil
	}
	return modelMedia.MediaFileEntity{}, errr.ErrNotFound
}
func (f *fakeStore) FindBySHA(_ context.Context, sha []byte) (modelMedia.MediaFileEntity, error) {
	for _, m := range f.files {
		if bytes.Equal(m.SHA256, sha) {
			return m, nil
		}
	}
	return modelMedia.MediaFileEntity{}, errr.ErrNotFound
}
func (f *fakeStore) FindByIDs(_ context.Context, ids []string) ([]modelMedia.MediaFileEntity, error) {
	var out []modelMedia.MediaFileEntity
	for _, id := range ids {
		if m, ok := f.files[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

type memFiles map[string][]byte

func (m memFiles) Put(_ context.Context, k string, d []byte) error { m[k] = d; return nil }
func (m memFiles) Get(_ context.Context, k string) ([]byte, error) {
	if d, ok := m[k]; ok {
		return d, nil
	}
	return nil, errors.New("missing")
}

func newSvc() (*MediaService, *fakeStore, memFiles) {
	store, files := &fakeStore{files: map[string]modelMedia.MediaFileEntity{}}, memFiles{}
	signer := mediaurl.NewSigner([]byte("0123456789abcdef0123456789abcdef"), 12*time.Hour, time.Hour)
	svc := NewMediaService(store, fakeTx{}, files, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC) }
	return svc, store, files
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var (
	author = modelAuth.Principal{TenantID: tenantID, UserID: "0192a000-0000-7000-8000-0000000000a1", Permissions: []string{modelAuth.PermItemWrite}}
	grader = modelAuth.Principal{TenantID: tenantID, UserID: "0192a000-0000-7000-8000-0000000000a2", Permissions: []string{modelAuth.PermGradeWrite}}
	taker  = modelAuth.Principal{TenantID: tenantID, UserID: "0192a000-0000-7000-8000-0000000000a3", Permissions: []string{modelAuth.PermAttemptTake}}
)

func TestUploadServeAndDedup(t *testing.T) {
	svc, store, files := newSvc()
	ctx := context.Background()
	data := pngBytes(t, 40, 30)

	dto, err := svc.Upload(ctx, author, "grafik.png", data, modelAudit.Client{})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if dto.MimeType != "image/png" || *dto.Width != 40 || *dto.Height != 30 || !strings.HasPrefix(dto.URL, modelMedia.URLPrefix+dto.ID+"?") {
		t.Fatalf("dto = %+v", dto)
	}
	key := store.files[dto.ID].StorageKey
	if key != tenantID+"/media/"+dto.ID+".png" || !bytes.Equal(files[key], data) {
		t.Fatalf("storage key %q / content not stored", key)
	}

	again, err := svc.Upload(ctx, author, "copy.png", data, modelAudit.Client{})
	if err != nil || again.ID != dto.ID || len(store.files) != 1 {
		t.Fatalf("same content must reuse the file: %+v err=%v files=%d", again, err, len(store.files))
	}

	u, _ := url.Parse(dto.URL)
	q := u.Query()
	f, err := svc.Serve(ctx, dto.ID, q.Get("t"), q.Get("e"), q.Get("s"))
	if err != nil || !bytes.Equal(f.Data, data) || f.MimeType != "image/png" {
		t.Fatalf("serve: err=%v mime=%s", err, f.MimeType)
	}
	for name, args := range map[string][4]string{
		"bad signature": {dto.ID, q.Get("t"), q.Get("e"), "AAAA"},
		"other tenant":  {dto.ID, "0192a000-0000-7000-8000-000000000002", q.Get("e"), q.Get("s")},
		"not a uuid":    {"x", q.Get("t"), q.Get("e"), q.Get("s")},
	} {
		if _, err := svc.Serve(ctx, args[0], args[1], args[2], args[3]); !errors.Is(err, errr.ErrNotFound) {
			t.Errorf("%s: err = %v, want not found", name, err)
		}
	}
	svc.now = func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) }
	if _, err := svc.Serve(ctx, dto.ID, q.Get("t"), q.Get("e"), q.Get("s")); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("expired link: err = %v", err)
	}
}

func TestUploadRejects(t *testing.T) {
	svc, _, _ := newSvc()
	ctx := context.Background()
	big := append(pngBytes(t, 2, 2), make([]byte, modelMedia.MaxImageBytes)...)
	cases := map[string][]byte{
		"empty":     {},
		"too big":   big,
		"svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"text":      []byte("hello"),
		"bad image": append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...),
	}
	for name, data := range cases {
		var ve *errr.ValidationError
		if _, err := svc.Upload(ctx, author, "x.png", data, modelAudit.Client{}); !errors.As(err, &ve) || ve.Fields["file"] == "" {
			t.Errorf("%s: err = %v, want file error", name, err)
		}
	}
	if _, err := svc.Upload(ctx, grader, "x.png", pngBytes(t, 2, 2), modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("grader upload: err = %v", err)
	}
}

func TestLinks(t *testing.T) {
	svc, _, _ := newSvc()
	ctx := context.Background()
	dto, _ := svc.Upload(ctx, author, "a.png", pngBytes(t, 3, 3), modelAudit.Client{})

	refs, err := svc.Links(ctx, grader, modelMedia.LinksInput{IDs: []string{dto.ID, strings.ToUpper(dto.ID), "nope", "0192a000-0000-7000-8000-0000000000ff"}})
	if err != nil || len(refs) != 1 || refs[dto.ID].Mime != "image/png" || *refs[dto.ID].W != 3 {
		t.Fatalf("refs = %+v err = %v", refs, err)
	}
	if _, err := svc.Links(ctx, taker, modelMedia.LinksInput{IDs: []string{dto.ID}}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("participant links: err = %v", err)
	}
}

func TestUploadAudio(t *testing.T) {
	svc, _, _ := newSvc()
	ctx := context.Background()
	pad := func(b []byte) []byte { return append(b, make([]byte, 64)...) }
	mp4 := func(brand string) []byte {
		b := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p'}
		b = append(b, brand...)
		b = append(b, 0, 0, 0, 0)
		b = append(b, "mp42isom"...)
		return pad(b)
	}
	ok := []struct {
		name, file, mime string
		data             []byte
	}{
		{"mp3 with ID3", "a.mp3", "audio/mpeg", pad([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"))},
		{"mp3 frame", "a.mp3", "audio/mpeg", pad([]byte{0xFF, 0xFB, 0x90, 0x64})},
		{"ogg", "a.ogg", "audio/ogg", pad([]byte("OggS\x00\x02\x00\x00"))},
		{"wav", "a.wav", "audio/wav", pad([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))},
		{"m4a", "listening.m4a", "audio/mp4", mp4("M4A ")},
	}
	for _, tc := range ok {
		dto, err := svc.Upload(ctx, author, tc.file, tc.data, modelAudit.Client{})
		if err != nil || dto.MimeType != tc.mime || dto.Width != nil {
			t.Errorf("%s: dto = %+v err = %v", tc.name, dto, err)
		}
	}
	bad := map[string]struct {
		file string
		data []byte
	}{
		"mp4 video":       {"clip.mp4", mp4("isom")},
		"audio too large": {"a.mp3", append([]byte("ID3\x03"), make([]byte, modelMedia.MaxAudioBytes)...)},
	}
	for name, tc := range bad {
		var ve *errr.ValidationError
		if _, err := svc.Upload(ctx, author, tc.file, tc.data, modelAudit.Client{}); !errors.As(err, &ve) || ve.Fields["file"] == "" {
			t.Errorf("%s: err = %v, want file error", name, err)
		}
	}
}
