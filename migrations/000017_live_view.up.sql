-- Lihat layar & kamera peserta sesuai permintaan (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md).
-- Video tidak pernah lewat server: tabel ini hanya menyimpan tayangan yang dibuka pengawas
-- dan pesan sinyal WebRTC (offer/answer/ICE) yang dipertukarkan lewat long-poll.

ALTER TABLE exam_sessions
  ADD COLUMN require_camera boolean NOT NULL DEFAULT false,
  ADD COLUMN require_screen boolean NOT NULL DEFAULT false;

CREATE TABLE live_views (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  session_id  uuid NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
  attempt_id  uuid NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  proctor_id  uuid NOT NULL REFERENCES users(id),
  started_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,             -- putus otomatis (pengawas bisa membuka lagi)
  ended_at    timestamptz,
  end_reason  text CHECK (end_reason IN ('proctor', 'participant', 'expired', 'replaced'))
);
-- tayangan aktif per attempt (batas pengawas per peserta) dan per pengawas (batas tayangan)
CREATE INDEX live_views_attempt_active_idx ON live_views (attempt_id) WHERE ended_at IS NULL;
CREATE INDEX live_views_proctor_active_idx ON live_views (proctor_id) WHERE ended_at IS NULL;
CREATE INDEX live_views_session_idx ON live_views (session_id);

CREATE TABLE live_view_signals (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,  -- kursor long-poll
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  view_id     uuid NOT NULL REFERENCES live_views(id) ON DELETE CASCADE,
  recipient   text NOT NULL CHECK (recipient IN ('participant', 'proctor')),
  kind        text NOT NULL CHECK (kind IN ('view_request', 'offer', 'answer', 'ice', 'end')),
  payload     jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX live_view_signals_view_idx ON live_view_signals (view_id, recipient, id);

SELECT enable_tenant_rls('live_views');
SELECT enable_tenant_rls('live_view_signals');
