-- Sesi ujian: jadwal, token, proktor, peserta.

CREATE TABLE exam_sessions (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id),
  exam_id                 uuid NOT NULL REFERENCES exams(id),
  name                    text NOT NULL,       -- 'Sesi 1 - Lab A'
  room                    text,
  starts_at               timestamptz NOT NULL,  -- jendela mulai dibuka
  ends_at                 timestamptz NOT NULL,  -- batas akhir; attempt tidak melewati ini
  late_entry_minutes      int CHECK (late_entry_minutes >= 0),  -- NULL = boleh masuk kapan saja dalam jendela
  token_required          boolean NOT NULL DEFAULT true,
  token_secret            bytea,               -- secret TOTP; token tampil = f(secret, waktu)
  token_rotation_minutes  int NOT NULL DEFAULT 15 CHECK (token_rotation_minutes > 0),
  status                  text NOT NULL DEFAULT 'scheduled'
                          CHECK (status IN ('scheduled', 'open', 'paused', 'closed', 'cancelled')),
  is_makeup               boolean NOT NULL DEFAULT false,  -- sesi susulan
  created_by              uuid REFERENCES users(id),
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at),
  CHECK (NOT token_required OR token_secret IS NOT NULL)
);
CREATE INDEX exam_sessions_tenant_time_idx ON exam_sessions (tenant_id, starts_at);
CREATE INDEX exam_sessions_exam_idx ON exam_sessions (exam_id);

CREATE TABLE exam_session_proctors (
  session_id  uuid NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (session_id, user_id)
);
CREATE INDEX exam_session_proctors_user_idx ON exam_session_proctors (user_id);

-- Grup yang ditugaskan (catatan asal); sumber kebenaran tetap exam_session_participants
CREATE TABLE exam_session_groups (
  session_id  uuid NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
  group_id    uuid NOT NULL REFERENCES groups(id),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (session_id, group_id)
);

CREATE TABLE exam_session_participants (
  session_id          uuid NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
  user_id             uuid NOT NULL REFERENCES users(id),
  tenant_id           uuid NOT NULL REFERENCES tenants(id),
  extra_time_minutes  int NOT NULL DEFAULT 0 CHECK (extra_time_minutes >= 0),  -- akomodasi
  attendance          text NOT NULL DEFAULT 'expected'
                      CHECK (attendance IN ('expected', 'present', 'absent', 'excused')),
  seat                text,
  notes               text,
  added_at            timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, user_id)
);
CREATE INDEX exam_session_participants_user_idx ON exam_session_participants (user_id);

SELECT add_updated_at_trigger('exam_sessions');

SELECT enable_tenant_rls('exam_sessions');
SELECT enable_tenant_rls('exam_session_proctors');
SELECT enable_tenant_rls('exam_session_groups');
SELECT enable_tenant_rls('exam_session_participants');
