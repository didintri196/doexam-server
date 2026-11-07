-- Monitor proktor (DOCS/PLAN/15-MONITOR-KARTU.md): agregat pelanggaran dan waktu kunci per
-- attempt hanya menyentuh event pelanggaran/kunci, bukan heartbeat dan jawaban.
-- CONCURRENTLY: attempt_events adalah tabel terbesar; jangan menahan penulisan selama ujian.
-- Harus satu-satunya pernyataan di file ini (tidak boleh di dalam transaksi).
CREATE INDEX CONCURRENTLY IF NOT EXISTS attempt_events_monitor_idx ON attempt_events (attempt_id, type, server_ts)
  WHERE type IN ('FOCUS_LOST', 'FULLSCREEN_EXIT', 'DEVICE_CHANGED', 'LOCKED');
