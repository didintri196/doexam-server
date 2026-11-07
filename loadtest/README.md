# Uji beban

Skrip [k6](https://k6.io) untuk target roadmap "500 peserta serentak di server 2 vCPU". Hasil, grafik, bukti, dan opsi spesifikasi infra ada di [`../LOADTEST.md`](../LOADTEST.md); catatan perencanaannya di `DOCS/PLAN/14-UJI-BEBAN.md` (repo utama).

**Hanya untuk server development/staging.** Skrip membuat akun peserta uji dan sesi ujian sungguhan di database.

## Yang disimulasikan (`exam.js`)

1. **Persiapan (`setup`):**
   - Membuat `VUS` akun peserta `<USER_PREFIX>0001…`. Akun yang sudah ada dipakai ulang.
   - Membuat sesi untuk `EXAM_ID`, menambahkan semua peserta, lalu membuka sesi. Membuka sesi sekaligus mengisi cache rencana ujian dan konten soal.
2. **Peserta:**
   - Datang tersebar dalam `RAMP_SECONDS`, lalu login, membuka daftar sesi, dan memulai attempt.
   - Selama `MINUTES` menit, mengirim sync tiap ±15 detik seperti exam client. Sekitar separuh sync membawa satu jawaban (soal berikutnya, sesuai jenisnya), sisanya heartbeat kosong.
3. **Proktor:** `PROCTORS` pengawas membuka monitor sesi tiap 5 detik.
4. **Penutup (`teardown`):** sesi ditutup, dan attempt yang masih berjalan dikumpulkan.

Ambang lulus:
- request gagal di bawah 1%
- tidak ada event yang ditolak
- p95 heartbeat di bawah 300 ms
- p95 sync berisi jawaban di bawah 500 ms
- p95 mulai ujian di bawah 2 detik

## Menjalankan

Akun staf yang menjalankan uji perlu izin `user.manage` dan pengelolaan sesi (peran Administrator). Kredensialnya diberikan lewat `ADMIN_TOKEN`, atau lewat `ADMIN_USERNAME` + `ADMIN_PASSWORD`.

Simpan kredensial di `loadtest/.env.local` (diabaikan git) bersama `EXAM_ID`:

```bash
set -a; . loadtest/.env.local; set +a
k6 run --summary-export /tmp/summary.json loadtest/exam.js
```

Jalankan API seperti server sungguhan, lalu bandingkan hasilnya dengan target:

```bash
APP_ENV=staging GOMAXPROCS=2 REDIS_URL=redis://localhost:6379/0 ./bin/doexam
```

- `GOMAXPROCS=2` meniru 2 vCPU.
- `APP_ENV=staging` mematikan log setiap query SQL. Query di atas 200 ms tetap tercatat.

| Variabel | Default | Arti |
|----------|---------|------|
| `BASE_URL` | `http://localhost:8080` | Alamat API |
| `EXAM_ID` | — | Ujian terbit yang dipakai |
| `VUS` | `500` | Jumlah peserta |
| `RAMP_SECONDS` | `60` | Rentang waktu kedatangan peserta |
| `MINUTES` | `5` | Lama mengerjakan setelah gelombang login |
| `PROCTORS` | `2` | Jumlah pengawas yang memantau |
| `MONITOR` | `views` | `views`: tampilan monitor berhalaman (10 kartu, bergantian per poll); `legacy`: monitor lama (semua peserta) sebagai pembanding |
| `USER_PREFIX` | `ujibeban` | Awalan username peserta uji |
| `PARTICIPANT_PASSWORD` | `UjiBeban-2026!` | Password akun peserta uji (khusus server uji) |

**Satu run memakai satu kesempatan mengerjakan per peserta.** Bila ujian hanya mengizinkan satu percobaan, run berikutnya akan gagal di `start` (HTTP 409 "no attempts left"). Untuk menghindarinya, pakai `USER_PREFIX` baru untuk setiap run, atau pakai ujian dengan batas percobaan lebih besar.

## Uji monitor saja (`monitor.js`)

Membebani monitor pengawas pada sesi yang **sudah ada**, tanpa peserta: `PROCTORS` pengawas me-refresh tiap 5 detik, bergantian antara halaman tampilan (10 kartu) dan daftar pantauan 25 peserta.

```bash
k6 run -e BASE_URL=http://localhost:8080 -e ADMIN_TOKEN=... -e SESSION_ID=... -e PROCTORS=50 -e DURATION=60s loadtest/monitor.js
```

`MONITOR=legacy` memakai monitor lama sebagai pembanding. Ambang: p95 < 200 ms.

## Memeriksa jawaban tidak hilang

`lt_answers_accepted` di ringkasan k6 harus sama dengan jumlah jawaban yang tersimpan untuk sesi itu:

```sql
SELECT count(*) FILTER (WHERE ai.response IS NOT NULL)
FROM attempts a JOIN attempt_items ai ON ai.attempt_id = a.id
WHERE a.session_id = '<id sesi dari log k6>';
```

## Membersihkan

`cleanup.js` menghapus (soft delete) akun peserta uji dengan awalan yang sama:

```bash
USER_PREFIX=ujibeban VUS=500 k6 run loadtest/cleanup.js
```

## Grafik

`charts.py` membuat grafik di `LOADTEST.md` dari ringkasan k6 (`--summary-export`), log request API (JSON, `APP_ENV=staging`), dan rekaman CPU/memori. Palet dan aturannya mengikuti panduan dataviz: palet tervalidasi untuk buta warna, satu sumbu per grafik, dan setiap grafik disertai tabel di dokumen.

```bash
python3 loadtest/charts.py loadtest/img runs.json
```

`runs.json` berisi daftar run: `name`, `label`, `participants`, `summary`, `resources`, `apilog`, `start`/`end` (HH:MM:SS), dan `after_fix`. Contohnya ada di docstring skrip.
