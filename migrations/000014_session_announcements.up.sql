-- Pengumuman pengawas/admin ke peserta sesi (DOCS/PLAN/12-PENGUMUMAN-PESERTA.md).
-- Penerima ditulis per baris juga untuk target 'all', supaya peserta yang ditambahkan
-- belakangan tidak menerima pengumuman lama, dan status baca tercatat per peserta.

CREATE TABLE session_announcements (
  id           uuid PRIMARY KEY,                -- UUIDv7 dari aplikasi: urutan id = urutan waktu
  tenant_id    uuid NOT NULL REFERENCES tenants(id),
  session_id   uuid NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
  sender_id    uuid NOT NULL REFERENCES users(id),
  body         text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 500),
  priority     text NOT NULL DEFAULT 'info' CHECK (priority IN ('info', 'important')),
  target       text NOT NULL CHECK (target IN ('all', 'selected')),
  recipients   int  NOT NULL CHECK (recipients >= 0),
  retracted_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX session_announcements_session_idx ON session_announcements (session_id, id);

CREATE TABLE session_announcement_recipients (
  announcement_id uuid NOT NULL REFERENCES session_announcements(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id),
  user_id         uuid NOT NULL REFERENCES users(id),
  read_at         timestamptz,
  PRIMARY KEY (announcement_id, user_id)
);
CREATE INDEX session_announcement_recipients_user_idx ON session_announcement_recipients (user_id, announcement_id);

SELECT enable_tenant_rls('session_announcements');
SELECT enable_tenant_rls('session_announcement_recipients');
