DROP TABLE IF EXISTS live_view_signals;
DROP TABLE IF EXISTS live_views;
ALTER TABLE exam_sessions DROP COLUMN IF EXISTS require_screen, DROP COLUMN IF EXISTS require_camera;
