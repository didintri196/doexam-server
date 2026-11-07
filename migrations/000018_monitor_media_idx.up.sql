-- Monitor proktor (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md §6.2): status kamera/layar/monitor tambahan
-- per attempt dibaca dari event ini saja, terpisah dari attempt_events_monitor_idx (000016).
-- CONCURRENTLY: harus satu-satunya pernyataan di file ini (tidak boleh di dalam transaksi).
CREATE INDEX CONCURRENTLY IF NOT EXISTS attempt_events_media_idx ON attempt_events (attempt_id, type, server_ts)
  WHERE type IN ('CAMERA_STOPPED', 'CAMERA_RESUMED', 'SCREEN_SHARE_STOPPED', 'SCREEN_SHARE_RESUMED', 'EXTRA_DISPLAY_DETECTED', 'EXTRA_DISPLAY_REMOVED');
