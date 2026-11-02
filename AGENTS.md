# DoExam Server — Aturan Coding (WAJIB)

Dokumen ini adalah **aturan baku** untuk siapa pun (manusia maupun AI) yang menulis kode di `apps/server`. Jika ada konflik antara kebiasaan pribadi dan dokumen ini, **ikuti dokumen ini**. Jika aturan perlu diubah, ubah dokumen ini dulu, baru kodenya.

Rujukan pola: Hexagonal Architecture (Ports & Adapters) ala [muchlist/templaterepo](https://github.com/muchlist/templaterepo). Contoh lengkap yang sudah ada di repo: domain **`health`** (paling sederhana), **`tenant`** dan **`auth`** (transaksi tenant, validasi, audit log), **`user`** dan **`group`** (CRUD lengkap: list berhalaman + filter, PATCH parsial, soft delete, otorisasi per izin). Tiru strukturnya.

---

## 1. Stack (tetap — jangan ganti/tambah tanpa diskusi)

| Layer | Teknologi |
|-------|-----------|
| Bahasa | Go 1.25+ |
| HTTP | Fiber v2 (`github.com/gofiber/fiber/v2`) |
| Database | PostgreSQL 16 + GORM (`gorm.io/gorm`, driver `gorm.io/driver/postgres`) |
| Migrasi | golang-migrate, file SQL di `migrations/` |
| Logging | `log/slog` lewat `pkg/mlog` |
| Tracing | OpenTelemetry lewat `pkg/otelutil` |
| Spreadsheet | `github.com/xuri/excelize/v2` lewat `pkg/spreadsheet` (jangan import excelize langsung di business) |
| Sanitasi HTML | `github.com/microcosm-cc/bluemonday` lewat `pkg/richtext` (jangan buat policy sendiri) |
| Test | `testing` standar (table-driven), `fiber.App.Test` untuk handler |

Menambah dependency baru = harus ada alasan kuat dan disebutkan di PR. Utamakan standard library.

## 2. Struktur Folder & Arah Dependency

```
apps/server/
├── app/server/              # Entry point (tidak di-reuse)
│   ├── main.go              # Load config, logger, OTEL, DB, server, graceful shutdown
│   ├── route.go             # SATU-SATUNYA tempat wiring DI + registrasi route
│   └── handler/             # HTTP handler (Fiber). <domain>_handler.go, response.go, error.go
├── business/<domain>/       # CORE logic per domain
│   ├── storer.go            # PORT: interface yang dibutuhkan service
│   ├── repo.go              # ADAPTER: implementasi GORM dari interface
│   ├── service.go           # CORE: logika bisnis
│   └── service_test.go      # Test service dengan fake storer
├── models/<domain>/         # Entity (GORM) & DTO/Input (JSON)
│   ├── <domain>_entity.go
│   └── <domain>_dto.go
├── config/                  # Config loader (env) — hanya dipakai main.go
├── pkg/                     # Utilitas reusable, TIDAK tahu domain bisnis
│   ├── db-pg/  errr/  mlog/  middleware/  otelutil/  password/  securetoken/
│   ├── spreadsheet/  storage/  ttlcache/  richtext/  psychometrics/
└── migrations/              # 000001_xxx.up.sql / .down.sql
```

**Arah dependency (dilarang melanggar):**
```
app/  ──►  business/  ──►  models/
  │            │
  └──►  pkg/  ◄┘          config/ hanya di-import oleh app/server/main.go & route.go
```
- `business/` **tidak boleh** import `app/`, `config/`, atau `github.com/gofiber/fiber`.
- `models/` **tidak boleh** import `business/`, `app/`, atau `pkg/`.
- `pkg/` **tidak boleh** import `business/`, `models/`, atau `app/`.
- Antar domain di `business/`: service boleh bergantung pada **interface** milik domain lain yang ditulis di `storer.go` domainnya sendiri, bukan pada struct konkret domain lain.

## 3. Cara Menambah Fitur / Domain Baru (urutan wajib)

1. **Migrasi** — cek dulu apakah tabelnya sudah ada di `migrations/` (seluruh skema MVP Fase 1 sudah dibuat). Jika perlu perubahan: `make db/migrations/new name=xxx`, isi `up` & `down`.
2. **Model** — `models/<domain>/<domain>_entity.go` (struct GORM + `TableName()`), `<domain>_dto.go` (DTO response, Input request, fungsi `ToDTO`).
3. **Port** — `business/<domain>/storer.go`: interface hanya berisi method yang benar-benar dipakai service.
4. **Adapter** — `business/<domain>/repo.go`: implementasi GORM + compile-time check.
5. **Service** — `business/<domain>/service.go`: logika bisnis + span OTEL.
6. **Test service** — `business/<domain>/service_test.go` dengan fake storer.
7. **Handler** — `app/server/handler/<domain>_handler.go`.
8. **Test handler** — `<domain>_handler_test.go` dengan `fiber.App.Test`.
9. **Wiring** — daftarkan repo → service → handler → route di `route.go` (sesuai urutan blok yang sudah ada).
10. **Verifikasi** — `make audit` harus hijau.

## 4. Aturan per Layer

### 4.1 `models/`
```go
// <domain>_entity.go
type ExamEntity struct {
    ID        string    `gorm:"type:uuid;primaryKey"`
    TenantID  string    `gorm:"type:uuid;not null"`
    Title     string    `gorm:"not null"`
    CreatedAt time.Time
    UpdatedAt time.Time
}

func (ExamEntity) TableName() string { return "exams" }
```
- Entity = cermin tabel. **Tanpa** tag `json` (entity tidak pernah dikirim langsung ke client).
- DTO = bentuk response. Selalu konversi lewat `ToDTO(entity)`.
- Input = bentuk request, dengan tag `json`. Validasi ditulis di **service** memakai `errr.Validation` (tanpa library validator):
  ```go
  v := errr.Validation{}
  v.Check(in.Title != "", "title", "required")
  if err := v.Err(); err != nil { return ExamDTO{}, err } // → 400 + detail per field
  ```
- Konstanta enum ditulis sebagai tipe bernama (`type ExamStatus string` + `const`), nilainya **sama persis** dengan CHECK constraint di SQL.
- Field rahasia (`password_hash`, `answer_key`, `token_secret`) **tidak boleh** ada di DTO yang dikirim ke peserta.

### 4.2 `business/<domain>/storer.go` (PORT)
```go
type ExamStorer interface {
    Create(ctx context.Context, exam *modelExam.ExamEntity) error
    FindByID(ctx context.Context, id string) (modelExam.ExamEntity, error)
}
```
- Interface ditulis di package **yang membutuhkannya** (service), bukan di package implementasi.
- Semua method menerima `ctx context.Context` sebagai parameter pertama.
- Kecil dan spesifik. Jangan buat interface "generic repository".

### 4.3 `business/<domain>/repo.go` (ADAPTER)
```go
var _ ExamStorer = (*ExamRepo)(nil) // compile-time check — WAJIB

type ExamRepo struct{ db *gorm.DB }

func NewExamRepo(db *gorm.DB) *ExamRepo { return &ExamRepo{db: db} } // return struct konkret

func (r *ExamRepo) FindByID(ctx context.Context, id string) (modelExam.ExamEntity, error) {
    var e modelExam.ExamEntity
    if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&e).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return modelExam.ExamEntity{}, errr.ErrNotFound
        }
        return modelExam.ExamEntity{}, fmt.Errorf("find exam %s: %w", id, err)
    }
    return e, nil
}
```
- Koneksi **selalu** lewat `dbpg.Conn(ctx, r.db)`: otomatis memakai transaksi tenant dari `ctx` bila ada, dan membawa `ctx`. Jangan pakai `r.db` langsung.
- Terjemahkan error GORM/Postgres ke sentinel `errr` (`gorm.ErrRecordNotFound` → `errr.ErrNotFound`, unique violation → `errr.ErrAlreadyExists`).
- Query **selalu** berparameter (`?`). Dilarang menyusun SQL dengan konkatenasi string dari input.
- Repo tidak berisi logika bisnis. Isinya hanya baca/tulis data.

### 4.4 `business/<domain>/service.go` (CORE)
```go
type ExamService struct {
    storer ExamStorer
    tracer trace.Tracer
    logger *slog.Logger
}

func NewExamService(storer ExamStorer, logger *slog.Logger) *ExamService {
    return &ExamService{storer: storer, tracer: otel.Tracer("doexam.service.exam"), logger: logger}
}

func (s *ExamService) GetByID(ctx context.Context, id string) (modelExam.ExamDTO, error) {
    ctx, span := s.tracer.Start(ctx, "ExamService.GetByID",
        trace.WithAttributes(attribute.String("exam.id", id)))
    defer span.End()

    e, err := s.storer.FindByID(ctx, id)
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        return modelExam.ExamDTO{}, err
    }
    return modelExam.ToDTO(e), nil
}
```
- Constructor menerima **interface**, mengembalikan **struct konkret** (`*ExamService`).
- Setiap method publik: buka span `"<Service>.<Method>"`, `defer span.End()`, catat error ke span.
- Service **tidak tahu** HTTP, Fiber, GORM, atau nama tabel.
- Kembalikan sentinel error dari `pkg/errr` (boleh dibungkus `fmt.Errorf("...: %w", errr.ErrX)`), jangan string error baru untuk kasus yang sudah ada sentinelnya. Bila client perlu pesan spesifik, pakai `errr.WithMessage(errr.ErrX, "pesan aman untuk client")`.
- Akses tabel ber-`tenant_id` **wajib** di dalam `s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error { ... })` (port `TxManager` di `storer.go`, implementasi `dbpg.TxManager`). Semua tulis yang saling terkait dalam satu `WithTenant`. Pekerjaan lambat (Argon2, panggilan eksternal) dilakukan **di luar** transaksi.
- Method yang butuh otorisasi menerima `p modelAuth.Principal` dan memeriksa `p.Has(modelAuth.PermX)` → `errr.ErrForbidden` bila tidak punya izin. Tenant diambil dari `p.TenantID`, **bukan** dari input request.
- Nilai konfigurasi (timeout, secret) diterima lewat parameter constructor, **bukan** membaca env.

### 4.5 `app/server/handler/`
```go
func (h *ExamHandler) GetByID(c *fiber.Ctx) error {
    logger := middleware.GetLogger(c)
    id := c.Params("id")

    dto, err := h.examSvc.GetByID(c.UserContext(), principal(c), id)
    if err != nil {
        return handleError(c, logger, "failed to get exam", err)
    }
    return SuccessResponse(c, fiber.StatusOK, "exam retrieved", dto)
}
```
- Handler hanya: parse & validasi input → panggil service → petakan error → kirim response.
- **Tidak ada logika bisnis** dan **tidak ada akses DB** di handler.
- Selalu teruskan `c.UserContext()` ke service (membawa trace).
- Logger request: `middleware.GetLogger(c)`.
- Response **wajib** lewat `SuccessResponse` / `ErrorResponse` / `PaginatedResponse`. Error dari service **wajib** lewat `handleError` (`handler/error.go`). Body JSON rusak → `badBody(c)`.
- Route yang butuh login dipasangi `requireAuth` di `route.go`; handler mengambil pemanggil dengan `principal(c)`.

### 4.6 `app/server/route.go`
- Satu-satunya tempat implementasi konkret dipasangkan ke interface.
- Urutan blok: Repositories → External Providers → Services → Handlers → Middleware → Routes. Jangan diacak.

### 4.7 `config/` & `main.go`
- Semua env dibaca di `config.Load()`; default aman untuk development.
- Wajib-di-production divalidasi di `Validate()`.
- Setiap env baru: tambahkan ke `config/config.go`, `.env.example`, dan tabel env di `README.md`.

## 5. Error → HTTP Status

| Sentinel (`pkg/errr`) | HTTP | Pesan ke client |
|-----------------------|------|-----------------|
| `ErrInvalidInput` (`*errr.ValidationError`) | 400 | "invalid input" + detail per field di `errors` |
| `ErrUnauthorized` | 401 | "unauthorized" |
| `ErrForbidden` | 403 | "forbidden" |
| `ErrNotFound` | 404 | "not found" |
| `ErrAlreadyExists` | 409 | "already exists" |
| `ErrConflict` | 409 | "conflict" (keadaan saat ini menolak aksi, mis. grup masih punya subgrup) |
| `ErrUnavailable` | 503 | "service not ready" |
| lainnya / `ErrInternal` | 500 | "internal server error" — **detail error tidak boleh bocor** ke client, cukup di log |

Untuk 4xx, pesan dari `errr.WithMessage` (mis. "setup already completed", "invalid credentials") menggantikan pesan default. Pemetaan ini sudah ada di `handleError`; jangan tulis ulang. Gunakan `errors.Is`/`errors.As`, jangan pernah membandingkan string error.

## 6. Format Response (envelope baku)

```json
{
  "success": true,
  "message": "exam retrieved",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "data": { },
  "errors": null,
  "meta": { "total": 120, "page": 1, "per_page": 20 }
}
```

## 7. Konvensi API

- Prefix versi: `/api/v1`. Probe di root: `GET /healthz` (liveness), `GET /readyz` (readiness).
- Resource jamak, kebab-case: `/api/v1/item-banks/:id/items`.
- JSON field: `snake_case`.
- ID: string UUID. Waktu: RFC 3339 UTC.
- Pagination: query `page` (mulai 1) & `per_page` (default 20, maks 100). Parse dengan `parsePage(c)` (`handler/pagination.go`), response pakai `PaginatedResponse`.
- PATCH = parsial: field Input bertipe pointer, `nil` = tidak diubah. Untuk field teks opsional, `""` = kosongkan. Untuk angka yang boleh kosong, pakai `opt.Nullable[T]` (`models/opt`): absen = tidak diubah, `null` = kosongkan.
- Download file: set `Content-Type` + `Content-Disposition: attachment` (lihat `sendXLSX`); data rahasia tambah `Cache-Control: no-store`.
- Pekerjaan lambat (> ~2 detik, mis. impor massal) = **job**: endpoint mengembalikan 202 + status, klien memantau dengan GET. Goroutine latar belakang memakai `context.WithoutCancel(ctx)`, tercatat di `WaitGroup`, dan ditunggu saat shutdown (fungsi yang dikembalikan `PrepareRoute`). Contoh: `business/importer`.
- Hapus data master (user, grup) = **soft delete** (`deleted_at`, lewat `gorm.DeletedAt`).
- Path param ID yang bukan UUID dijawab 404 (validasi `uuid.Validate` di service), jangan sampai jadi error SQL 500.
- Pencarian teks memakai `ILIKE` dengan `dbpg.ContainsPattern(q)` (meng-escape `%` `_` `\`).
- Unique violation diterjemahkan di repo ke `*errr.ValidationError` per field (mis. `{"username": "already in use"}`) memakai `dbpg.IsUniqueViolation` + `dbpg.ConstraintName`.
- Pesan `message` dalam bahasa Inggris, singkat, huruf kecil. Teks untuk pengguna akhir diterjemahkan di client.

## 8. Database

- **Sumber kebenaran skema = `migrations/`** (golang-migrate). `DOCS/PLAN/08-schema-mvp.sql` hanya rujukan desain awal. **Dilarang** `db.AutoMigrate`.
- Setiap migrasi punya `up` dan `down`, dan `down` harus benar-benar membalik `up` (uji `up` → `down` → `up`). Migrasi yang sudah di-merge **tidak boleh diedit**. Buat migrasi baru.
- Tabel baru yang punya `tenant_id` **wajib** memanggil `SELECT enable_tenant_rls('nama_tabel');`. Tabel yang punya `updated_at` **wajib** memanggil `SELECT add_updated_at_trigger('nama_tabel');`. Kedua helper ada di `000001_init`.
- PK: UUID (dibuat aplikasi, UUIDv7). Waktu: `timestamptz`. Enum: `text` + `CHECK`.
- **Multi-tenant**: setiap query ke tabel ber-`tenant_id` harus berjalan di transaksi `dbpg.TxManager.WithTenant` (meng-`set_config('app.tenant_id', ..., true)` = `SET LOCAL`). RLS di DB menolak sisanya. Jangan menulis ulang per repo. Transaksi tenant tidak boleh bersarang (`dbpg.ErrNestedTx`). Tabel `tenants` sendiri tidak ber-RLS.
- **RLS tidak berlaku untuk pemilik tabel.** Migrasi dijalankan dengan role pemilik, sedangkan aplikasi di staging/production **wajib** konek dengan role terpisah yang bukan pemilik tabel (hanya `SELECT/INSERT/UPDATE/DELETE`). Di development, role pemilik masih boleh dipakai.
- Operasi yang mengubah beberapa tabel sekaligus wajib dalam **satu transaksi**.
- Hormati invariant skema ([08-DATABASE-MVP](../../DOCS/PLAN/08-DATABASE-MVP.md)):
  - `item_versions`/`stimulus_versions` yang `locked_at` terisi **tidak boleh** diubah isinya. Buat versi baru.
  - `attempt_events` & `audit_logs` **append-only**.
  - Sinkron event jawaban harus idempoten (`ON CONFLICT (attempt_id, client_event_id) DO NOTHING`).
  - Waktu ujian otoritatif dari server (`attempts.deadline_at`), bukan dari jam klien.

## 9. Keamanan (khusus platform ujian)

- **`answer_key` tidak pernah dikirim ke peserta.** Endpoint peserta memakai DTO terpisah yang tidak punya field kunci.
- Rich text (soal, opsi, pembahasan, stimulus) **disanitasi di server** dengan `richtext.Sanitize` sebelum disimpan (allowlist 08-DATABASE-MVP §4.1). Jangan percaya HTML dari klien. Field wajib dicek dengan `richtext.IsBlank` (HTML kosong / hanya `<script>` = kosong). `search_text` diisi `richtext.PlainText`.
- JSON berisi HTML di-marshal tanpa escape HTML (`SetEscapeHTML(false)`, lihat `itembank.mustJSON`) — isinya sudah aman karena sudah disanitasi.
- Bank soal: periksa akses lewat `ItemBankService.bankAccess(ctx, p, bankID, level)`, jangan cek izin sendiri. Resource di bank tanpa akses dijawab 404, bukan 403.
- Ujian: cek hak ubah lewat `ExamService.loadForEdit` (pembuat atau `tenant.manage`) dan status draft lewat `ExamService.structure`. Jangan mengubah struktur ujian yang sudah terbit. Soal yang dipakai ujian dipatok (`item_version_id`) dan dikunci saat terbit; modul attempt **wajib** memakai versi terpatok / `exam_rule_pool`, bukan `items.current_version_id`.
- Sesi ujian: akses lewat `SessionService.load` (manager atau proktor yang ditugaskan; sesi lain 404). Token ruang **hanya** lewat `pkg/sessiontoken` (`Code`/`Verify`); `token_secret` tidak boleh masuk DTO, log, atau audit, dan respons token diberi `Cache-Control: no-store`.
- Attempt: **setiap** jalur yang menyentuh attempt memanggil `AttemptService.tick` dulu (menutup attempt/section yang lewat waktu) — tidak ada sweeper. Tampilan peserta hanya lewat `itemView` (tanpa `answer_key`, `scoring`, `explanation`); jangan kirim `ItemRow` mentah. Waktu otoritatif = jam server (`deadline_at`), bukan `client_ts`. Penilaian hanya lewat `scoreItem` (fungsi murni, ber-test); setiap perubahan nilai (submit, koreksi esai, regrade) lewat `AttemptService.recompute` — jangan mengubah skor/total attempt dengan cara lain, dan jangan mengubah `attempt_items.max_points`.
- Alur lintas domain lewat interface milik domain pemakai, dan dipanggil **di dalam transaksi pemanggil** bila perlu (contoh: `session.AttemptFinalizer` dipanggil saat sesi ditutup; jangan membuka `WithTenant` bersarang).
- Rumus statistik/psikometri hanya di `pkg/psychometrics` (murni, tanpa model domain) dan **wajib** diuji terhadap nilai rujukan yang dihitung secara independen (mis. Python `statistics`, R `psych`/`mirt`). Interpretasi (ambang mudah/sukar, rekomendasi) ada di service domain, bukan di `pkg`.
- Waktu yang disimpan lalu dibaca ulang dikembalikan dalam UTC dan dipotong ke mikrodetik (presisi Postgres), supaya nilai yang sama tidak tampil berbeda antar respons.
- Versi soal/stimulus yang `locked_at`-nya terisi **tidak boleh** diubah isinya: pakai logika di `UpdateItem`/`UpdateStimulus` (buat versi baru). Trigger DB adalah jaring pengaman terakhir, bukan alur normal.
- Password: Argon2id lewat `pkg/password.Hasher` (membatasi hash paralel supaya lonjakan login saat ujian dimulai tidak menghabiskan memori). Token: buat dengan `pkg/securetoken`, simpan SHA-256-nya saja.
- Password sementara (`securetoken.ReadablePassword`) hanya ditampilkan sekali di response (atau sekali unduh lewat `ttlcache` untuk impor), tidak pernah masuk database, log, `audit_logs`, atau file storage, dan selalu disertai `must_change_password = true`.
- File upload: batasi ukuran & jumlah baris, deteksi format dari isi file (bukan hanya ekstensi), simpan lewat port `FileStore` dengan key `<tenant_id>/<jenis>/<uuid>.<ext>`, bukan nama file dari user.
- Kegagalan login selalu dijawab sama ("invalid credentials"), termasuk untuk tenant/username yang tidak ada (`VerifyDummy` menyamakan waktu respons).
- Token sesi login berformat `<tenant_id>.<acak>`, dikirim di header `Authorization: Bearer`. Validasi lewat `AuthService.Authenticate` (middleware `RequireAuth`), jangan diparse sendiri.
- Log SQL GORM memakai `ParameterizedQueries`, jadi nilai parameter tidak pernah tercetak. Jangan matikan.
- Jangan log: password, token, secret, `answer_key`, isi jawaban peserta, data pribadi (NIK/NISN, tanggal lahir). Log ID-nya saja.
- Setiap perubahan soal, kunci, nilai, atau konfigurasi ujian ditulis ke `audit_logs` di transaksi yang sama dengan perubahannya (konstanta aksi di `models/audit`, buat entri dengan `modelAudit.New(modelAudit.Entry{...})`; isi `Before`/`After` dengan DTO, bukan entity). Login, logout, dan ganti password juga diaudit.
- Validasi otorisasi (peran + tenant + kepemilikan resource) di **service**, bukan hanya di middleware.

## 10. Logging & Tracing

- Gunakan `slog` terstruktur: `logger.InfoContext(ctx, "pesan", slog.String("exam_id", id))`. **Tanpa** `fmt.Println` / `log.Printf`.
- Selalu varian `...Context(ctx, ...)` supaya `trace_id` ikut.
- Level: `Info` untuk kejadian bisnis penting, `Warn` untuk input buruk/dependency terganggu, `Error` untuk kegagalan yang perlu ditindaklanjuti.
- Nama tracer: `doexam.service.<domain>`. Nama span: `<Service>.<Method>`.

## 11. Penamaan & Gaya

- `gofmt` wajib. Package: huruf kecil satu kata (`itembank`, bukan `item_bank`).
- File: `snake_case.go` dengan sufiks peran: `_handler.go`, `_entity.go`, `_dto.go`, `_test.go`.
- Alias import model: `modelExam "doexam/models/exam"`.
- Struct: `<Domain>Service`, `<Domain>Repo`, `<Domain>Handler`, `<Domain>Storer`. Constructor: `New<Struct>`.
- Gunakan `any`, bukan `interface{}`.
- Komentar dokumentasi (`// Name ...`) untuk setiap tipe & fungsi yang diekspor.
- Identifier dalam bahasa Inggris. Komentar boleh Inggris atau Indonesia, konsisten dalam satu file.

## 12. Testing

- **Service**: table-driven test dengan fake storer (struct kecil di file test), tanpa DB. Untuk storer besar, fake boleh **meng-embed interface-nya** dan hanya meng-override method yang dipakai test (method lain panic bila terpanggil — itu justru menandai test yang salah). Contoh: `business/itembank/service_test.go`.
- Fungsi murni yang kritis (validasi isi soal, sanitasi) di-test langsung dengan tabel kasus valid & tidak valid, termasuk **idempotensi** (menormalisasi ulang hasil simpanan = no-op).
- **Handler**: `fiber.New()` + `app.Test(httptest.NewRequest(...))`, cek status code & envelope.
- **Repo**: test integrasi ke Postgres sungguhan (build tag `integration`) untuk query yang tidak trivial.
- Setiap bug fix disertai test yang gagal sebelum diperbaiki.
- Tes tidak boleh bergantung pada urutan atau jam dinding. Waktu disuntikkan bila perlu.

## 13. Definition of Done

- [ ] Mengikuti struktur & urutan di §3.
- [ ] `make audit` hijau (tidy, fmt, vet, test `-race`).
- [ ] Endpoint baru tercantum di tabel API pada `README.md`.
- [ ] Env baru tercantum di `.env.example` & `README.md`.
- [ ] Tidak ada data rahasia di response maupun log.
- [ ] Perubahan skema lewat migrasi baru (bukan edit migrasi lama).

## 14. Yang DILARANG

- Import Fiber/GORM di `service.go`.
- Logika bisnis di handler atau repo.
- Membaca `os.Getenv` di luar `config/`.
- `db.AutoMigrate`, SQL dari konkatenasi string, `panic` untuk alur error biasa.
- Mengembalikan entity GORM langsung sebagai response.
- Menelan error (`_ = err`, `if err != nil { return nil }` tanpa alasan).
- Variabel global yang bisa diubah (kecuali `version` yang diisi ldflags).
- Mengubah file di `DOCS/PLAN/` tanpa diminta, kecuali menambah keputusan yang sudah disepakati.
