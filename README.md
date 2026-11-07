# DoExam API

Backend DoExam — platform ujian open source (CBT & CAT).

> **Aturan coding wajib:** [AGENTS.md](AGENTS.md)

## Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.25+ |
| HTTP Framework | Fiber v2 |
| Database | PostgreSQL 16 + GORM |
| Migration | golang-migrate (SQL) |
| Spreadsheet | excelize (`.xlsx`), `encoding/csv` |
| Sanitasi HTML | bluemonday (allowlist) + `golang.org/x/net/html` |
| Psikometri | `pkg/psychometrics` (CTT; IRT/CAT menyusul) |
| Tracing | OpenTelemetry (OTLP gRPC, opsional) |
| Logging | log/slog (structured) |
| Architecture | Hexagonal (Ports & Adapters) |

## Quick Start

```bash
cp .env.example .env
docker compose up -d postgres
make db/migrations/up    # butuh golang-migrate: brew install golang-migrate
make run/api
```

Server berjalan di `http://localhost:8080`.

### Monitoring (Prometheus + Grafana)

```bash
openssl rand -hex 16 > monitoring/.metrics-token          # sekali
docker compose up -d prometheus grafana
METRICS_ADDR=0.0.0.0:9464 METRICS_TOKEN=$(cat monitoring/.metrics-token) make run/api
```

Grafana di `http://localhost:3000` (dashboard **DoExam API**), Prometheus di `http://localhost:9090`. Panduan dan hasil uji: [MONITORING.md](MONITORING.md). Katalog metrik: [METRICS.md](METRICS.md).

## Database & Migrasi

Skema dikelola dengan [golang-migrate](https://github.com/golang-migrate/migrate) di `migrations/`. Rancangan awalnya ada di dokumen perencanaan proyek (`DOCS/PLAN/08-DATABASE-MVP.md`, di luar repo ini); file migrasi adalah sumber kebenarannya.

| # | Migrasi | Isi |
|---|---------|-----|
| 1 | `init` | Extension `citext`, fungsi bersama, helper `enable_tenant_rls()` & `add_updated_at_trigger()` |
| 2 | `create_identity` | tenants, users, roles, user_roles, auth_sessions, groups, group_members |
| 3 | `create_media_import` | media_files, import_jobs |
| 4 | `create_item_bank` | subjects, competencies, item_banks, item_bank_members, stimuli, stimulus_versions, items, item_versions |
| 5 | `create_exams` | exams, exam_sections, exam_section_items, exam_section_rules, exam_item_adjustments |
| 6 | `create_exam_sessions` | exam_sessions, exam_session_proctors, exam_session_groups, exam_session_participants |
| 7 | `create_attempts` | attempts, attempt_sections, attempt_items, attempt_events, manual_grades |
| 8 | `create_statistics` | item_statistics, exam_statistics |
| 9 | `create_audit_logs` | audit_logs |
| 10 | `create_exam_rule_pool` | exam_rule_pool (snapshot kandidat aturan blueprint saat terbit) |
| 11 | `add_attempt_item_response_seq` | attempt_items.response_seq (jawaban lama tidak menimpa yang baru) |
| 12 | `neutral_role_names` | Nama peran bawaan netral (Penyusun Soal, Peserta); default `tenants.preset` = `custom` |
| 13 | `media_dimensions` | media_files.width, media_files.height (ukuran gambar) |

> **Penting (RLS):** isolasi tenant memakai Row-Level Security, yang **tidak berlaku untuk pemilik tabel**. Di staging/production, jalankan migrasi dengan role pemilik, tapi koneksikan aplikasi dengan role terpisah:
> ```sql
> CREATE ROLE doexam_app LOGIN PASSWORD '...';
> GRANT USAGE ON SCHEMA public TO doexam_app;
> GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO doexam_app;
> ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO doexam_app;
> ```
> Setiap query ber-tenant berjalan di `dbpg.TxManager.WithTenant`, yang meng-set `app.tenant_id` per transaksi.

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/healthz` | Liveness — 200 selama proses hidup (tidak mengecek DB) |
| GET | `/readyz` | Readiness — 200 jika semua dependency (DB) up, 503 jika tidak |
| POST | `/api/v1/setup` | Setup awal: buat tenant (institusi), 5 peran bawaan, dan admin. Hanya bisa sekali (409 setelahnya). `tenant_slug` opsional, dibuat dari `tenant_name` |
| POST | `/api/v1/auth/login` | Login `{username, password}` → token sesi. `tenant` (slug) hanya wajib bila instalasi melayani lebih dari satu institusi |
| GET | `/api/v1/public/tenant` | Tanpa login, untuk layar login & header klien: nama, `tagline`, preset, locale, warna, `logo_url`. Query `tenant` opsional (wajib bila > 1 institusi). 404 `setup not completed` sebelum setup |
| GET | `/api/v1/public/tenant/logo` | Tanpa login: file logo (PNG/JPEG/WebP), dengan `ETag` + cache 1 hari. Query `tenant` opsional |
| GET | `/api/v1/public/templates/:name` | Tanpa login: templat impor sebagai file biasa (`template-impor-soal.docx`, `template-impor-users.xlsx`, `template-impor-groups.xlsx`, `template-staf.xlsx`, `template-peserta.xlsx`, `template-peserta-grup.xlsx`), dengan `Content-Disposition` lengkap supaya nama & ekstensi benar di semua browser |
| POST | `/api/v1/media` 🔒 `item.write` | Unggah gambar (PNG/JPEG/GIF/WebP ≤5 MB) atau audio (MP3/M4A/Ogg/WAV ≤20 MB) soal (multipart `file`; jenis dibaca dari isi; SVG dan video ditolak). File sama → id lama. → `{id, mime_type, size_bytes, width, height, url}`. Sisipkan di HTML sebagai `<img data-media-id="…">` |
| POST | `/api/v1/media/links` 🔒 `item.read`/`item.write`/`exam.read`/`grade.write`/`report.read` | `{ids[≤500]}` → `{<id>: {url, mime, w, h}}`, link bertanda tangan untuk media di konten yang sedang dibuka. Id tak dikenal dilewati |
| GET | `/api/v1/media/:id?t&e&s` | Tanpa login: isi file lewat link bertanda tangan (HMAC tenant+file+kedaluwarsa, ≥12 jam, berganti per jam). Link salah/kedaluwarsa → 404. `Cache-Control: private, immutable` |
| GET | `/api/v1/auth/me` 🔒 | Profil, tenant, peran, dan izin pengguna yang login |
| POST | `/api/v1/auth/logout` 🔒 | Cabut sesi saat ini |
| POST | `/api/v1/auth/change-password` 🔒 | Ganti password; sesi lain milik user ikut dicabut |
| GET | `/api/v1/tenant` 🔒 | Profil institusi + `settings` efektif (zona waktu, locale, warna, kop, `logo_url`) |
| PATCH | `/api/v1/tenant` 🔒 `tenant.manage` | Ubah `name`, `timezone` (IANA), `locale` (`id`/`en`), `primary_color` (`#RRGGBB`), `tagline` (sub judul header, maks 100), `letterhead` (teks kop, maks 500), `exam_rules` (tata tertib untuk lembar pengawas, teks maks 5000), `card` (setelan kartu peserta, objek lengkap: `title`, `id_label`, `note`, `signer_title`, `signer_name`, `signer_number`, `show_external_id`/`show_exam`/`show_session`/`show_seat`/`show_schedule`/`show_qr`). `""` = kembali ke default |
| PUT | `/api/v1/tenant/logo` 🔒 `tenant.manage` | Unggah logo (multipart `file`, PNG/JPEG/WebP maks 1 MiB, jenis dideteksi dari isi; SVG ditolak) |
| DELETE | `/api/v1/tenant/logo` 🔒 `tenant.manage` | Hapus logo |
| PUT | `/api/v1/tenant/card-signature` 🔒 `tenant.manage` | Unggah gambar tanda tangan/stempel untuk kartu peserta (aturan file sama dengan logo). Tidak publik: `settings.card.signature_url` dan data cetak sesi memakai link bertanda tangan |
| DELETE | `/api/v1/tenant/card-signature` 🔒 `tenant.manage` | Hapus gambar tanda tangan kartu |
| GET | `/api/v1/roles` 🔒 `user.manage` | Daftar peran tenant (untuk memilih peran) |
| GET | `/api/v1/users` 🔒 `user.manage` | Daftar pengguna. Query: `q`, `role`, `kind` (`staff` = punya izin selain `attempt.take`, `participant` = punya `attempt.take`), `group_id`, `status`, `page`, `per_page` |
| POST | `/api/v1/users` 🔒 `user.manage` | Buat pengguna. Tanpa `password` → `temporary_password` dibuat & ditampilkan **sekali**. Peran default `participant` |
| GET | `/api/v1/users/:id` 🔒 `user.manage` | Detail pengguna + peran + grup |
| PATCH | `/api/v1/users/:id` 🔒 `user.manage` | Ubah `full_name`, `email`, `external_id`, `status`, `role_codes` (ganti semua). `""` mengosongkan email/external_id |
| DELETE | `/api/v1/users/:id` 🔒 `user.manage` | Hapus (soft delete); username bisa dipakai lagi |
| POST | `/api/v1/users/:id/reset-password` 🔒 `user.manage` | Password sementara baru (tampil sekali), wajib ganti saat login |
| POST | `/api/v1/users/resolve` 🔒 `user.manage` / `group.manage` / `session.manage` | Cocokkan `{identifiers[≤5000]}` (username, ID peserta/`external_id`, atau email; tanpa beda huruf besar-kecil) → `{found[], not_found[], ambiguous[]}`. `found` tanpa email, satu entri per pengguna |
| POST | `/api/v1/users/resolve-file` 🔒 sama | Sama, dari file `.xlsx`/`.csv` (multipart `file`, ≤5 MB, ≤5000 baris). Kolom `username`/`external_id`/`id_peserta`/`email`, atau kolom pertama |
| GET | `/api/v1/groups` 🔒 | Daftar grup. Query: `parent_id` (UUID / `root`), `kind`, `period`, `q`, `page`, `per_page` |
| POST | `/api/v1/groups` 🔒 `group.manage` | Buat grup `{kind, name, code?, period?, parent_id?}` |
| GET | `/api/v1/groups/:id` 🔒 | Detail grup + jumlah anggota |
| PATCH | `/api/v1/groups/:id` 🔒 `group.manage` | Ubah grup / pindah induk (`parent_id: ""` = ke akar) |
| DELETE | `/api/v1/groups/:id` 🔒 `group.manage` | Hapus grup tanpa subgrup (409 bila masih punya subgrup) |
| GET | `/api/v1/groups/:id/members` 🔒 | Anggota grup (leader dulu) |
| POST | `/api/v1/groups/:id/members` 🔒 `group.manage` | Tambah anggota `{user_ids?, from_group_id?, member_role?}` (total ≤5000) → `{requested, added}` (`added` = yang sebelumnya belum jadi anggota). Anggota di `user_ids` yang sudah ada diperbarui perannya; anggota yang disalin dari `from_group_id` dan sudah ada tetap dengan perannya |
| POST | `/api/v1/groups/:id/members/remove` 🔒 `group.manage` | Keluarkan banyak anggota `{user_ids[≤5000]}` → `{removed}` |
| DELETE | `/api/v1/groups/:id/members/:user_id` 🔒 `group.manage` | Keluarkan anggota |
| GET/POST | `/api/v1/subjects` 🔒 | Daftar (`q`, `page`) / buat subjek `{code, name}` |
| GET/PATCH/DELETE | `/api/v1/subjects/:id` 🔒 | Detail / ubah / hapus subjek (409 bila masih punya bank soal) |
| GET/POST | `/api/v1/subjects/:id/competencies` 🔒 | Pohon kompetensi (flat, susun dari `parent_id`) / tambah `{code, name, parent_id?, sort_order?}` |
| PATCH/DELETE | `/api/v1/competencies/:id` 🔒 | Ubah / pindah induk / hapus (409 bila punya anak atau dipakai soal) |
| GET/POST | `/api/v1/item-banks` 🔒 | Bank yang bisa diakses (`subject_id`, `q`) / buat bank `{subject_id, name, description?}` (pembuat jadi owner) |
| GET/PATCH/DELETE | `/api/v1/item-banks/:id` 🔒 | Detail (+`my_access`, `item_count`) / ubah / hapus bank kosong — ubah & hapus: owner |
| GET/POST | `/api/v1/item-banks/:id/members` 🔒 | Anggota bank / set akses `{user_id, access: viewer\|editor\|owner}` (owner) |
| DELETE | `/api/v1/item-banks/:id/members/:user_id` 🔒 | Cabut akses (owner terakhir tidak bisa dicabut) |
| GET/POST | `/api/v1/item-banks/:id/stimuli` 🔒 | Daftar / buat stimulus `{title, body}` |
| GET/PATCH/DELETE | `/api/v1/stimuli/:id` 🔒 | Detail / ubah (versi baru bila terkunci) / hapus (409 bila dipakai soal) |
| GET | `/api/v1/item-banks/:id/items` 🔒 | Daftar soal. Query: `q` (teks penuh + kode), `status`, `type`, `competency_id`, `difficulty`, `tag`, `page`, `per_page` |
| POST | `/api/v1/item-banks/:id/items` 🔒 | Buat soal (lihat bentuk JSON di bawah) |
| POST | `/api/v1/item-banks/:id/import-word` 🔒 | Impor soal dari Word (multipart `file` `.docx`, ≤20 MB, ≤300 soal; akses penyunting bank). `?preview=1` hanya membaca: `{items[{no, type, stem, options, key, points, competency, stimulus, explanation, errors, warnings}], valid, invalid, stimuli, warnings, media}` (`media` = gambar sebagai data URL untuk pratinjau). Tanpa `preview`: gambar disimpan, soal valid dibuat sebagai **draf** beserta bacaannya, soal tidak valid dilewati → `{created, created_stimuli, committed: true, …}` |
| GET | `/api/v1/item-banks/import-word/template` 🔒 | Unduh templat `.docx` berisi petunjuk dan contoh tiap jenis soal |
| GET/PATCH/DELETE | `/api/v1/items/:id` 🔒 | Detail lengkap + kunci / ubah (versi baru bila terkunci) / hapus (409 bila pernah dipakai → `retired`) |
| GET | `/api/v1/items/:id/versions` 🔒 | Riwayat versi soal |
| GET/POST | `/api/v1/exams` 🔒 | Daftar (`status`, `subject_id`, `q`, `mine`) / buat ujian draft |
| GET/PATCH/DELETE | `/api/v1/exams/:id` 🔒 | Detail lengkap (section, soal, aturan, total soal/poin/menit) / ubah / hapus draft |
| GET | `/api/v1/exams/:id/check` 🔒 | Cek kesiapan terbit (daftar masalah), tanpa mengubah apa pun |
| POST | `/api/v1/exams/:id/publish` · `/unpublish` · `/archive` 🔒 `exam.publish` | Terbitkan (patok & kunci versi, snapshot blueprint) / kembali draft (bila belum ada sesi) / arsipkan |
| POST | `/api/v1/exams/:id/sections` · `/sections/order` 🔒 | Tambah section / urutkan ulang `{ids}` |
| PATCH/DELETE | `/api/v1/sections/:id` 🔒 | Ubah / hapus section |
| POST | `/api/v1/sections/:id/items` · `/items/order` 🔒 | Tambah soal manual `{items:[{item_id, points?, lock_options?}]}` / urutkan |
| PATCH/DELETE | `/api/v1/section-items/:id` 🔒 | Ubah poin & `lock_options` / keluarkan soal |
| POST | `/api/v1/sections/:id/rules` 🔒 | Aturan blueprint `{bank_id, item_count, competency_id?, item_type?, difficulty?, tags_any?, points?}` |
| PATCH/DELETE | `/api/v1/section-rules/:id` 🔒 | Ubah / hapus aturan |
| GET/POST | `/api/v1/sessions` 🔒 | Daftar (`exam_id`, `status`, `from`, `to` RFC 3339) — manager: semua, proktor: yang ditugaskan / buat sesi `{exam_id, name, starts_at, ends_at, room?, late_entry_minutes?, token_required?, token_rotation_minutes?, is_makeup?}` `session.manage` |
| GET/PATCH/DELETE | `/api/v1/sessions/:id` 🔒 | Detail (ujian, proktor, grup, rekap kehadiran) / ubah / hapus (hanya `scheduled` tanpa attempt) |
| POST | `/api/v1/sessions/:id/status` 🔒 | `{status: open\|paused\|closed\|cancelled}` — proktor boleh buka/jeda/tutup, batal hanya manager |
| GET | `/api/v1/sessions/:id/token` 🔒 | Token ruang saat ini `{code, expires_at, rotation_minutes}` (manager & proktor sesi) |
| GET | `/api/v1/sessions/:id/print` 🔒 | Data dokumen cetak sesi (manager & proktor sesi): `session`, `institution` `{name, letterhead, exam_rules, logo_url}`, `participants` (urut kursi lalu nama, dengan `attempt_status` terakhir), `stats` `{registered, present, absent, excused, started, submitted}` untuk berita acara. Peserta yang sudah mulai mengerjakan dihitung hadir |
| POST | `/api/v1/sessions/:id/card-passwords` 🔒 `session.manage` + `user.manage` | Password baru dari sistem untuk dicetak di kartu peserta `{only_new?: true}` (default hanya peserta yang belum pernah login; akun nonaktif dilewati) → `{passwords[{user_id, password}], skipped}`. **Tampil sekali** (`Cache-Control: no-store`), tidak disimpan/di-log/diaudit; peserta **tidak** wajib menggantinya. Password lama peserta itu tidak berlaku lagi |
| POST | `/api/v1/sessions/:id/token/rotate` 🔒 `session.manage` | Ganti secret: kode lama langsung tidak berlaku |
| PUT | `/api/v1/sessions/:id/proctors` 🔒 `session.manage` | Ganti daftar proktor `{user_ids}` (harus punya izin `session.proctor`) |
| GET/POST | `/api/v1/sessions/:id/participants` 🔒 | Daftar peserta (`q`, `attendance`) / tambah `{user_ids?, group_ids?, extra_time_minutes?}` (total ≤5000) `session.manage` |
| PATCH/DELETE | `/api/v1/sessions/:id/participants/:user_id` 🔒 | Kehadiran, kursi, catatan (proktor) & `extra_time_minutes` (manager) / keluarkan (belum mulai) |
| GET | `/api/v1/my/sessions` 🔒 | Sesi milik peserta yang login (tanpa token) |
| GET | `/api/v1/my/announcements?session_id=` 🔒 peserta | Pengumuman untuk peserta di sesi itu (terbaru dulu) + `cursor`. Yang ditarik tetap tampil tanpa teks |
| POST | `/api/v1/my/announcements/:aid/read` 🔒 peserta | Konfirmasi baca sebelum attempt dimulai (saat attempt berjalan lewat event `ANNOUNCEMENT_READ`) |
| POST | `/api/v1/attempts` 🔒 `attempt.take` | Mulai / lanjutkan ujian `{session_id, token}` + header `X-Device-ID` → soal tanpa kunci |
| GET | `/api/v1/attempts/:id` 🔒 | Tampilan ujian peserta (status, batas waktu, `server_time`, section & soal aktif, jawaban tersimpan, `media` = link bertanda tangan, `media_plays` = putaran audio tercatat per media, `extra_plays` = tambahan dari proktor) |
| POST | `/api/v1/attempts/:id/events` 🔒 | Sinkron batch event (≤200) — idempoten, aman dikirim ulang |
| POST | `/api/v1/attempts/:id/sections/:section_id/finish` 🔒 | Selesaikan section (mode berurutan → section berikutnya mulai) |
| POST | `/api/v1/attempts/:id/submit` 🔒 | Kumpulkan & nilai otomatis |
| GET | `/api/v1/attempts/:id/result` 🔒 | Hasil sesuai `result_visibility` (+ pembahasan bila `show_explanation`) |
| GET | `/api/v1/my/attempts` 🔒 | Riwayat ujian peserta |
| POST | `/api/v1/sessions/:id/announcements` 🔒 proktor sesi / `session.manage` | Kirim pengumuman `{body (teks ≤500), priority: info\|important, target: all\|selected, user_ids?}` → `{id, recipients, skipped}`. Sesi dijadwalkan/berlangsung saja, maks. 10 per menit per sesi (409) |
| GET | `/api/v1/sessions/:id/announcements` 🔒 proktor sesi / `session.manage` | Riwayat (terbaru dulu) dengan `recipients`/`read`, plus `unread_important` (peserta yang belum mengonfirmasi pengumuman penting > 1 menit) |
| GET | `/api/v1/sessions/:id/announcements/:aid/recipients` 🔒 proktor sesi / `session.manage` | Penerima dan `read_at` (belum membaca di atas) |
| POST | `/api/v1/sessions/:id/announcements/:aid/retract` 🔒 pengirim / `session.manage` | Tarik pengumuman |
| POST | `/api/v1/announcements` 🔒 `session.manage` | Satu pengumuman ke semua peserta beberapa sesi: `{session_ids \| running: true, body, priority}` |
| GET | `/api/v1/sessions/:id/monitor` 🔒 | Dashboard proktor: status, terjawab, batas waktu, terakhir aktif, pelanggaran tiap peserta |
| POST | `/api/v1/attempts/:id/extend` · `/lock` · `/unlock` · `/reset-device` · `/force-submit` 🔒 | Aksi proktor (proktor sesi atau `session.manage`) |
| POST | `/api/v1/attempts/:id/grant-plays` 🔒 | Aksi proktor: `{plays: 1-5}` tambahan putar untuk setiap audio berbatas (mis. speaker rusak). Dicatat sebagai event `MEDIA_PLAYS_GRANTED` |
| GET | `/api/v1/exams/:id/grading/summary` 🔒 | Soal esai ujian + jumlah menunggu/sudah dinilai |
| GET | `/api/v1/exams/:id/grading` 🔒 | Antrean koreksi esai. Query: `item_version_id`, `status` (pending\|graded\|all), `blind`, `page` |
| GET/POST | `/api/v1/grading/items/:id` 🔒 | Detail jawaban (soal, rubrik, model jawaban, riwayat) / beri nilai `{score}` atau `{rubric_scores:[{criterion, score}]}` + `comment` |
| GET/POST | `/api/v1/exams/:id/adjustments` 🔒 | Daftar / buat penyesuaian `{item_version_id, action: annul_full_credit\|annul_remove\|override_key, answer_key?, reason}` → regrade otomatis |
| DELETE | `/api/v1/adjustments/:id` 🔒 | Cabut penyesuaian → regrade otomatis |
| GET | `/api/v1/exams/:id/recap` 🔒 | Rekap nilai semua peserta (status, percobaan, nilai sesuai `attempt_scoring`, lulus). Query: `session_id`, `group_id` |
| GET | `/api/v1/exams/:id/recap/export` 🔒 | Rekap nilai `.xlsx` (filter sama) |
| GET/POST | `/api/v1/exams/:id/analysis` 🔒 | Analisis butir CTT (p, daya beda, distraktor, KR-20/α, statistik & histogram nilai). GET = tersimpan (dihitung bila belum ada), POST = hitung ulang. Query: `session_id` |
| GET | `/api/v1/attempts/:id/report` 🔒 | Laporan individu (staf): nilai per section & kompetensi, tiap soal dengan jawaban, kunci, waktu, kunjungan, pelanggaran |
| GET | `/api/v1/imports/templates/:kind` 🔒 | Unduh template `.xlsx` (`users` / `groups`) |
| POST | `/api/v1/imports` 🔒 | Unggah file (multipart `kind`, `file`, opsional `audience` = `staff`\|`participant` dan `group_id`) → divalidasi, status `ready`/`failed` + `errors` per baris (`warning: true` = peringatan, tidak menggagalkan). `staff`: kolom `roles` wajib, hanya peran staf. `participant`: semua baris berperan peserta. `group_id`: semua baris masuk grup itu; akun yang sudah ada (username/`external_id`) cukup ditambahkan ke grup, butuh juga `group.manage`. Hasil per jenis baris di `summary` (`created`, `added`, `already_member`) |
| GET | `/api/v1/imports` 🔒 | Riwayat impor. Query: `kind`, `audience`, `group_id`, `page`, `per_page` (dengan `audience` tanpa `group_id`, impor ke grup tidak ikut) |
| GET | `/api/v1/imports/:id` 🔒 | Status & laporan error impor |
| POST | `/api/v1/imports/:id/commit` 🔒 | Jalankan impor (202, berjalan di latar belakang; pantau dengan GET) |
| GET | `/api/v1/imports/:id/credentials` 🔒 | Unduh `.xlsx` password sementara. **Sekali saja**, maks 1 jam setelah selesai |

🔒 = butuh header `Authorization: Bearer <token>`; kode setelahnya = izin yang dibutuhkan. Membaca grup cukup salah satu dari `group.manage`, `user.manage`, `session.manage`.

Impor `users` butuh `user.manage`, impor `groups` butuh `group.manage`. Alur: unduh template → isi → unggah → periksa `errors` → commit → unduh kredensial. Impor grup dulu bila kolom `groups` di file pengguna merujuk grup baru. File diproses di latar belakang dalam satu transaksi (semua baris masuk atau tidak sama sekali); password sementara hanya disimpan di memori selama 1 jam.

Akses bank soal = izin peran + keanggotaan bank (`viewer` baca, `editor` tulis soal, `owner` kelola bank). Admin (`tenant.manage`) owner di semua bank. Bank tanpa akses → 404.

Contoh membuat soal (rich text = HTML, disanitasi server; rumus `<span data-math="...">`, gambar `<img data-media-id="<uuid>">`):
```json
{
  "code": "MTK-7-001", "type": "mcq_single",
  "content": {"stem": "<p>Hasil 1/2 + 1/4?</p>", "options": [{"id": "o1", "body": "3/4"}, {"id": "o2", "body": "1/6"}]},
  "answer_key": {"correct": "o1"},
  "default_points": 1, "difficulty": "easy", "tags": ["pecahan"],
  "competency_id": "<uuid>", "stimulus_id": "<uuid>", "explanation": "<p>Samakan penyebut.</p>"
}
```
Tipe lain: `mcq_multi` (`answer_key.correct: ["a","c"]`, `scoring.method: all_or_nothing|partial`), `true_false` (`answer_key.value`), `short_answer` (`content.input: text|number`, `answer_key.accepted: [{value, match: exact|case_insensitive|numeric, tolerance?}]`), `essay` (`content.max_words?`, `answer_key.model_answer?`, `scoring.rubric: [{criterion, max}]`). Bentuk JSON lengkap per tipe ada di `models/itembank/content.go`.

Ujian: semua perubahan struktur menjawab dengan **ujian lengkap terbaru** (editor bisa langsung render ulang). `available` pada aturan = jumlah soal aktif yang cocok sekarang (draft) atau ukuran snapshot (terbit). Kompetensi pada aturan mencakup sub-kompetensinya.

**Format impor Word**: soal bernomor `1.`/`1)` (atau penomoran otomatis Word), pilihan `A.`/`a)` (atau huruf otomatis), `*` di depan huruf = jawaban benar. Baris berlabel (tanpa beda huruf besar-kecil): `Jenis: pg|pgk|bs|isian|esai` (berlaku untuk soal sesudahnya), `Kunci: B` / `A, C` / `Benar` / `jawaban1 | jawaban2`, `Skor: 2`, `Kompetensi: <kode>`, `Pembahasan: …`, `Bacaan: <judul>` … `Akhir bacaan`. Tanpa `Jenis:`: ada pilihan → `pg` (dua kunci → `pgk`), ada `Kunci` tanpa pilihan → `isian`, selain itu `esai`. Tanpa `Kunci` dan `*`, pilihan yang seluruhnya tebal dianggap benar. Gambar PNG/JPEG/GIF/WebP ikut tersimpan (EMF/WMF dilewati dengan peringatan); rumus Word dikonversi ke LaTeX untuk pecahan, pangkat, indeks, akar, kurung, Σ/∫, sisanya diberi peringatan. Validasi isi sama persis dengan editor soal.

**Event dari exam client** (`POST /attempts/:id/events`): tiap event `{client_event_id (UUID), client_seq, type, attempt_item_id?, payload, client_ts}`. Tipe: `RESPONSE_SAVED` `{"response": {...}|null}`, `FLAG_SET` `{"flagged": true}`, `ITEM_VIEWED` `{"time_spent_ms": 3000}`, `FOCUS_LOST`, `FOCUS_RETURNED`, `FULLSCREEN_EXIT`, `MEDIA_PLAYED` `{"media_id": "…", "play_no": 1}` (audio mulai diputar; tetap dicatat walau melewati batas, supaya proktor melihatnya), `ANNOUNCEMENT_READ` `{"announcement_id": "…"}` (konfirmasi pengumuman; diterima juga setelah batas waktu). Respons sinkron membawa `announcement_cursor`: bila berubah, klien memuat ulang `GET /my/announcements`. Bentuk jawaban: `{"selected":"o2"}`, `{"selected":["a","c"]}`, `{"value":true}`, `{"text":"..."}`. Event ditolak per baris dengan alasan (`locked`, `section_closed`, `forward_only`, `after_deadline`, `attempt_closed`, ...), sisanya tetap diterapkan.

Menilai & regrade butuh `grade.write`, `tenant.manage`, atau menjadi pembuat ujian. Hasil peserta berstatus `graded` setelah semua esai dinilai (atau dianulir).

Laporan butuh `tenant.manage`, atau `report.read` bersama pembuat ujian / `grade.write`.

Pengaman manajemen pengguna: tidak bisa men-suspend/menghapus/me-reset diri sendiri, dan tenant harus selalu punya minimal satu admin aktif. Suspend, hapus, dan reset password mencabut semua sesi login pengguna itu. Rate limit: login 10×/menit per IP+tenant+username, setup 5×/menit per IP (penyimpanan in-memory, per instance).

### Mulai pakai

```bash
curl -X POST localhost:8080/api/v1/setup -H 'Content-Type: application/json' -d '{
  "tenant_name": "SMAN 1", "preset": "school",
  "admin_username": "admin", "admin_full_name": "Administrator", "admin_password": "ganti-saya-123"
}'

curl -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username": "admin", "password": "ganti-saya-123"}'
# → data.token, pakai sebagai: Authorization: Bearer <token>
```

`preset`: opsional, `school`, `company`, atau `custom` (default). Tidak mengubah nama peran: semua institusi mendapat peran bernama netral (Administrator, Penyusun Soal, Pengawas, Penilai, Peserta) yang bisa diganti admin.

Satu instalasi = satu institusi: pengguna tidak perlu tahu slug tenant. Nama, logo, zona waktu, dan kop diatur admin lewat `PATCH /api/v1/tenant` dan `PUT /api/v1/tenant/logo`.

Contoh respons `/readyz`:
```json
{
  "success": true,
  "message": "service is ready",
  "trace_id": "",
  "data": {
    "status": "up",
    "version": "v0.1.0",
    "uptime_seconds": 42,
    "checks": { "database": { "status": "up", "latency_ms": 1 } }
  }
}
```

## Development

```bash
make run/api     # jalankan server
make test        # jalankan test
make audit       # tidy + fmt + vet + test -race
make build       # build binary ke bin/doexam
make db/migrations/new name=add_xxx   # buat migrasi baru
make db/migrations/up                  # jalankan semua migrasi
make db/migrations/down                # rollback 1 migrasi terakhir
make db/migrations/version             # versi migrasi saat ini
```

Target `db/migrations/*` membaca `DATABASE_URL` dari `.env` (atau environment).

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ENV` | `development` | `development` / `staging` / `production` |
| `PORT` | `8080` | Port server |
| `DATABASE_URL` | `postgres://doexam:secret@localhost:5433/doexam?sslmode=disable` | DSN PostgreSQL (wajib di-set di production) |
| `STORAGE_DIR` | `./data/storage` | Folder file upload (impor, logo, media soal). Di Docker: volume `storage` |
| `MEDIA_SIGNING_KEY` | _(dibuat otomatis)_ | Kunci HMAC link media (hex/base64, ≥16 byte). Kosong = dibuat sekali lalu disimpan di `STORAGE_DIR/system/media-signing.key`. Wajib sama di semua instance bila server lebih dari satu |
| `REDIS_URL` | _(kosong)_ | Redis untuk cache konten soal (`redis://host:6379/0`, `rediss://` untuk TLS), dipakai bersama antar-instance. Kosong = cache in-memory. Redis mati = baca dari PostgreSQL, ujian tetap jalan |
| `CONTENT_CACHE_MB` | `64` | Batas cache konten soal in-memory (MB), dipakai bila `REDIS_URL` kosong |
| `AUTH_SESSION_TTL` | `12h` | Masa berlaku sesi login (format durasi Go: `12h`, `90m`) |
| `AUTH_CACHE_TTL` | `30s` | Lama hasil validasi token login dipercaya tanpa membaca database (`0` = mati, maks. `5m`). Logout, suspend, ganti peran, dan reset password berlaku seketika di server yang sama; di server lain paling lambat sebesar nilai ini |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(kosong)_ | Endpoint OTLP gRPC; kosong = tracing mati |
| `OTEL_SERVICE_NAME` | `doexam-api` | Nama service di tracing |
| `METRICS_ADDR` | `127.0.0.1:9464` | Listener `/metrics` (Prometheus), terpisah dari port API. `off` = mati. Katalog metrik: [METRICS.md](METRICS.md) |
| `METRICS_TOKEN` | _(kosong)_ | Bila diisi, scrape wajib `Authorization: Bearer <token>`. Wajib di production bila `METRICS_ADDR` bukan loopback |
| `OTEL_METRICS_EXPORTER` | `none` | `otlp` = metrik juga dikirim ke `OTEL_EXPORTER_OTLP_ENDPOINT` |
