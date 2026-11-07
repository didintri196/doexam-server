# Metrik DoExam API

Rencana dan alasannya ada di `DOCS/PLAN/16-MONITORING-METRIK.md` (repo utama). Dokumen ini adalah katalog metrik yang benar-benar diekspos binary.

## Cara mengaktifkan

| Env | Default | Arti |
|-----|---------|------|
| `METRICS_ADDR` | `127.0.0.1:9464` | Listener `/metrics` (format Prometheus), terpisah dari port API. `off` = mati |
| `METRICS_TOKEN` | _(kosong)_ | Bila diisi, scrape wajib membawa `Authorization: Bearer <token>`. Wajib di production bila `METRICS_ADDR` bukan loopback |
| `OTEL_METRICS_EXPORTER` | `none` | `otlp` = metrik juga dikirim ke `OTEL_EXPORTER_OTLP_ENDPOINT` tiap 30 detik (tanpa metrik runtime Go/proses) |

```bash
curl -s localhost:9464/metrics | grep ^doexam_
```

Di dalam container, `127.0.0.1` hanya terjangkau dari container itu sendiri. Untuk di-scrape dari container Prometheus, pakai `METRICS_ADDR=0.0.0.0:9464` + `METRICS_TOKEN`, dan jangan publish port 9464 ke internet.

Contoh scrape Prometheus:

```yaml
scrape_configs:
  - job_name: doexam
    scrape_interval: 15s
    authorization: { credentials: "<METRICS_TOKEN>" }   # hapus bila token kosong
    static_configs: [{ targets: ["127.0.0.1:9464"] }]
```

## Aturan label

- `http_route` memakai template route (`/api/v1/attempts/:id/events`), tidak pernah path mentah. Path yang tidak cocok dengan route mana pun = `unmatched`. Request yang dihentikan middleware grup sebelum sampai handler (mis. 401 dari `requireAuth`) diberi label `<prefix grup>/*`.
- Tidak ada label berisi ID (attempt, user, sesi, tenant), IP, username, atau pesan error.
- `/healthz` dan `/readyz` tidak dicatat.

## Katalog

### HTTP

| Metrik | Tipe | Label |
|--------|------|-------|
| `http_server_request_duration_seconds` | histogram | `http_request_method`, `http_route`, `http_response_status_code` |
| `http_server_active_requests` | gauge | `http_request_method` |
| `http_server_request_body_size_bytes` | histogram | `http_route` (dari `Content-Length`) |
| `http_server_response_body_size_bytes` | histogram | `http_route` (dari `Content-Length`) |
| `doexam_http_panics_total` | counter | — |
| `doexam_rate_limit_hits_total` | counter | `limiter` (`login`, `setup`) |

### Pelaksanaan ujian

| Metrik | Tipe | Label |
|--------|------|-------|
| `doexam_attempt_starts_total` | counter | `result`: `started`, `resumed`, `bad_token`, `not_participant`, `device_locked`, `conflict` (sesi belum/sudah tutup, telat masuk, kesempatan habis), `not_found`, `forbidden`, `invalid`, `error` |
| `doexam_attempt_finished_total` | counter | `reason`: `participant`, `timeout`, `proctor`, `session_closed` |
| `doexam_attempt_events_total` | counter | `type` (jenis event klien, lainnya `other`), `outcome`: `accepted`, `duplicate`, `rejected` |
| `doexam_attempt_events_rejected_total` | counter | `type`, `reason`: `attempt_closed`, `after_deadline`, `section_closed`, `locked`, `forward_only`, `unknown_type`, `unknown_item`, `invalid_id`, `invalid_payload` |
| `doexam_attempt_sync_batch_size` | histogram | — (0 = heartbeat) |
| `doexam_attempt_sync_client_lag_seconds` | histogram | — (waktu event dibuat di klien → diterima server) |
| `doexam_attempt_violations_total` | counter | `type`: `FOCUS_LOST`, `FULLSCREEN_EXIT`, `DEVICE_CHANGED` |
| `doexam_attempt_sections_timed_out_total` | counter | — |
| `doexam_attempt_proctor_actions_total` | counter | `action`: `Extend`, `Lock`, `Unlock`, `ResetDevice`, `ForceSubmit`, `GrantPlays` |
| `doexam_attempts_connected` | gauge | — (attempt berjalan yang sync ke node ini ≤ 60 detik terakhir) |
| `doexam_attempts_stale` | gauge | — (attempt berjalan yang sync terakhirnya ke node ini > 60 detik) |

Jawaban yang tersimpan = `doexam_attempt_events_total{type="RESPONSE_SAVED",outcome="accepted"}`.

`connected`/`stale` dihitung di memori per node, karena RLS membuat role aplikasi tidak bisa menghitung attempt lintas tenant. Di cluster tanpa *sticky session*, jumlahkan `connected` antar node; `stale` hanya akurat per node.

### Autentikasi

| Metrik | Tipe | Label |
|--------|------|-------|
| `doexam_auth_logins_total` | counter | `result`: `ok`, `invalid_input`, `unknown_account`, `wrong_password`, `inactive_user`, `error` |
| `doexam_auth_cache_requests_total` | counter | `result`: `hit`, `miss` (hanya bila `AUTH_CACHE_TTL` > 0) |
| `doexam_auth_cache_entries` | gauge | — |
| `doexam_password_hash_duration_seconds` | histogram | `pool` (`login`, `import`), `op` (`verify`, `hash`) |
| `doexam_password_wait_duration_seconds` | histogram | `pool` — antre slot Argon2; naik = CPU login jenuh |

### Database (sisi aplikasi)

| Metrik | Tipe | Label |
|--------|------|-------|
| `doexam_db_query_duration_seconds` | histogram | `db_operation` (`select`, `insert`, `update`, `delete`, `row`, `raw`), `db_table` |
| `doexam_db_slow_queries_total` | counter | `db_table` (> 200 ms) |
| `doexam_db_errors_total` | counter | `kind`: `deadlock`, `serialization`, `lock_timeout`, `statement_timeout`, `unique_violation`, `foreign_key_violation`, `constraint_violation`, `connection`, `permission`, `canceled`, `timeout`, `other` |
| `doexam_db_tx_duration_seconds` | histogram | `name` (method service pembuka transaksi, mis. `AttemptService.Sync`), `outcome` (`commit`, `rollback`) |
| `go_sql_open_connections`, `go_sql_in_use_connections`, `go_sql_idle_connections`, `go_sql_max_open_connections` | gauge | `db_name` |
| `go_sql_wait_count_total`, `go_sql_wait_duration_seconds_total` | counter | `db_name` — naik = pool (25 koneksi) kurang |

### PostgreSQL (sisi server, dibaca maks. tiap 30 detik)

| Metrik | Tipe | Label |
|--------|------|-------|
| `doexam_pg_connections` | gauge | `state` (`active`, `idle`, `idle in transaction`, …) |
| `doexam_pg_lock_waiting` | gauge | — |
| `doexam_pg_xact_commit_total`, `doexam_pg_xact_rollback_total`, `doexam_pg_deadlocks_total` | counter | — |
| `doexam_pg_blocks_hit_total`, `doexam_pg_blocks_read_total` | counter | — (cache hit ratio) |
| `doexam_pg_temp_bytes_total` | counter | — |
| `doexam_pg_database_size_bytes` | gauge | — |
| `doexam_pg_table_live_tuples`, `doexam_pg_table_dead_tuples` | gauge | `table` |
| `doexam_pg_table_autovacuum_total`, `doexam_pg_table_autoanalyze_total` | counter | `table` |
| `doexam_pg_table_last_autovacuum_seconds`, `doexam_pg_table_last_autoanalyze_seconds` | gauge | `table` (unix time) |

Tabel yang dipantau: `attempts`, `attempt_items`, `attempt_sections`, `attempt_events`, `auth_sessions`, `audit_logs`. Untuk statistik per query, pasang `pg_stat_statements` + `postgres_exporter`.

### Cache, storage, impor

| Metrik | Tipe | Label |
|--------|------|-------|
| `doexam_cache_requests_total` | counter | `backend` (`memory`, `redis`), `cache` (`item-content`, `exam-plan`), `result` (`hit`, `miss`) |
| `doexam_cache_operation_duration_seconds` | histogram | `backend`, `op` (`get`, `set`) |
| `doexam_cache_errors_total` | counter | `backend`, `op` |
| `doexam_cache_entries`, `doexam_cache_size_bytes`, `doexam_cache_limit_bytes` | gauge | `backend="memory"` |
| `doexam_storage_operation_duration_seconds` | histogram | `op` (`put`, `get`) |
| `doexam_storage_transferred_bytes_total` | counter | `op` |
| `doexam_storage_errors_total` | counter | `op` (file tidak ada bukan error) |
| `doexam_storage_free_bytes`, `doexam_storage_size_bytes` | gauge | `storage="files"` |
| `doexam_import_duration_seconds` | histogram | `kind`, `result` (`done`, `invalid`, `failed`) |
| `doexam_import_queue_duration_seconds` | histogram | `kind` |
| `doexam_import_rows_total` | counter | `kind` |

### Proses & runtime

| Metrik | Arti |
|--------|------|
| `doexam_build_info{version, go_version}` | Selalu 1 |
| `doexam_dependency_up{dependency}` | `postgres`, `redis` (bila dipakai): 1 hidup, 0 mati. Dicek maks. tiap 15 detik |
| `go_goroutines`, `go_memstats_*`, `go_gc_*`, `go_sched_latencies_seconds` | Runtime Go. `go_sched_latencies_seconds` = jeda scheduler |
| `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds`, `process_start_time_seconds` | Proses |

## Contoh alert (PromQL)

```yaml
groups:
  - name: doexam
    rules:
      - alert: DoExamDependencyDown
        expr: doexam_dependency_up == 0
        for: 1m
        labels: { severity: critical }
      - alert: DoExamParticipant5xx
        expr: |
          sum(rate(http_server_request_duration_seconds_count{http_route=~"/api/v1/(auth/login|attempts.*)",http_response_status_code=~"5.."}[5m]))
            / sum(rate(http_server_request_duration_seconds_count{http_route=~"/api/v1/(auth/login|attempts.*)"}[5m])) > 0.01
        for: 5m
        labels: { severity: critical }
      - alert: DoExamEventsRejected
        expr: sum(increase(doexam_attempt_events_rejected_total{reason!~"after_deadline|forward_only"}[5m])) > 0
        labels: { severity: critical }
      - alert: DoExamSyncSlow
        expr: |
          histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket{http_route="/api/v1/attempts/:id/events"}[5m]))) > 0.3
        for: 5m
        labels: { severity: warning }
      - alert: DoExamDBPoolWaiting
        expr: rate(go_sql_wait_count_total[1m]) > 0
        for: 3m
        labels: { severity: warning }
      - alert: DoExamParticipantsStale
        expr: sum(doexam_attempts_stale) / clamp_min(sum(doexam_attempts_connected) + sum(doexam_attempts_stale), 1) > 0.1
        for: 2m
        labels: { severity: warning }   # biasanya jaringan lab, bukan server
      - alert: DoExamLoginFailuresSpike
        expr: sum(rate(doexam_auth_logins_total{result=~"wrong_password|unknown_account"}[5m])) > 5 * sum(rate(doexam_auth_logins_total{result=~"wrong_password|unknown_account"}[1h] offset 5m))
        for: 5m
        labels: { severity: warning }
      - alert: DoExamDiskLow
        expr: doexam_storage_free_bytes / doexam_storage_size_bytes < 0.1
        for: 10m
        labels: { severity: warning }
      - alert: DoExamPanics
        expr: increase(doexam_http_panics_total[5m]) > 0
        labels: { severity: warning }
```
