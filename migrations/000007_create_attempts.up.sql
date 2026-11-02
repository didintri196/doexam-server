-- Pengerjaan ujian: attempt, section, soal+jawaban, log event, koreksi manual.

CREATE TABLE attempts (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id),
  session_id            uuid NOT NULL REFERENCES exam_sessions(id),
  exam_id               uuid NOT NULL REFERENCES exams(id),
  user_id               uuid NOT NULL REFERENCES users(id),
  attempt_no            smallint NOT NULL DEFAULT 1,
  status                text NOT NULL DEFAULT 'in_progress'
                        CHECK (status IN ('in_progress', 'locked', 'submitted', 'graded', 'void')),
  config_snapshot       jsonb NOT NULL,      -- salinan pengaturan ujian+section saat mulai
  seed                  bigint NOT NULL,     -- seed acak → urutan bisa direproduksi
  started_at            timestamptz NOT NULL DEFAULT now(),
  deadline_at           timestamptz NOT NULL,  -- otoritatif; diperpanjang saat proktor tambah waktu
  submitted_at          timestamptz,
  submit_reason         text CHECK (submit_reason IN ('participant', 'timeout', 'proctor', 'session_closed')),
  active_device_id      text,                -- 1 perangkat aktif; ganti perangkat butuh izin proktor
  last_seen_at          timestamptz,
  last_event_seq        int NOT NULL DEFAULT 0,
  raw_score             numeric(10,2),
  max_score             numeric(10,2),
  final_score           numeric(6,2),        -- skala 0–100
  passed                boolean,
  pending_manual_count  int NOT NULL DEFAULT 0,  -- jumlah esai belum dinilai
  graded_at             timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (session_id, user_id, attempt_no),
  FOREIGN KEY (session_id, user_id) REFERENCES exam_session_participants (session_id, user_id)
);
CREATE INDEX attempts_session_status_idx ON attempts (session_id, status);
CREATE INDEX attempts_user_idx ON attempts (user_id, exam_id);
CREATE INDEX attempts_exam_idx ON attempts (exam_id) WHERE status IN ('submitted', 'graded');
-- satu attempt aktif per peserta per ujian
CREATE UNIQUE INDEX attempts_one_active_uq ON attempts (exam_id, user_id) WHERE status IN ('in_progress', 'locked');

CREATE TABLE attempt_sections (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  attempt_id    uuid NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  section_id    uuid NOT NULL REFERENCES exam_sections(id),
  sort_order    int  NOT NULL,
  started_at    timestamptz,
  deadline_at   timestamptz,                 -- NULL = mengikuti attempt.deadline_at
  closed_at     timestamptz,
  raw_score     numeric(10,2),
  max_score     numeric(10,2),
  passed        boolean,
  UNIQUE (attempt_id, section_id),
  UNIQUE (attempt_id, sort_order)
);

-- Satu baris per soal yang diberikan ke peserta + jawaban terakhirnya
CREATE TABLE attempt_items (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id),
  attempt_id         uuid NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  attempt_section_id uuid NOT NULL REFERENCES attempt_sections(id) ON DELETE CASCADE,
  position           int  NOT NULL,          -- nomor soal yang dilihat peserta (1..n)
  item_version_id    uuid NOT NULL REFERENCES item_versions(id),
  option_order       text[],                 -- urutan id opsi yang ditampilkan (hasil acak)
  max_points         numeric(8,2) NOT NULL,
  response           jsonb,                  -- jawaban terakhir (bentuk per tipe soal)
  answered_at        timestamptz,
  flagged            boolean NOT NULL DEFAULT false,  -- ragu-ragu
  time_spent_ms      int NOT NULL DEFAULT 0,
  visit_count        int NOT NULL DEFAULT 0,
  score              numeric(8,2),           -- NULL = belum dinilai
  is_correct         boolean,                -- untuk analisis butir (NULL utk esai/parsial)
  score_source       text CHECK (score_source IN ('auto', 'manual', 'adjustment')),
  scored_at          timestamptz,
  UNIQUE (attempt_id, position),
  UNIQUE (attempt_id, item_version_id)
);
CREATE INDEX attempt_items_item_version_idx ON attempt_items (item_version_id);  -- analisis butir
CREATE INDEX attempt_items_pending_manual_idx ON attempt_items (tenant_id, item_version_id)
  WHERE score IS NULL AND response IS NOT NULL;  -- antrian koreksi esai

-- Log kejadian append-only: sumber kebenaran untuk audit, sinkron offline, & replay
CREATE TABLE attempt_events (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  attempt_id       uuid NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  client_event_id  uuid,                     -- dari klien; kunci idempotensi
  client_seq       int,                      -- urutan di klien
  type             text NOT NULL,            -- ATTEMPT_STARTED, ITEM_VIEWED, RESPONSE_SAVED, FLAG_SET,
                                             -- FOCUS_LOST, FULLSCREEN_EXIT, DEVICE_CHANGED, TIME_EXTENDED,
                                             -- LOCKED, UNLOCKED, SECTION_CLOSED, SUBMITTED, ...
  attempt_item_id  uuid REFERENCES attempt_items(id) ON DELETE CASCADE,
  payload          jsonb NOT NULL DEFAULT '{}',
  actor_id         uuid REFERENCES users(id),  -- NULL = peserta/sistem; terisi untuk aksi proktor
  client_ts        timestamptz,
  server_ts        timestamptz NOT NULL DEFAULT now(),
  ip               inet
);
CREATE UNIQUE INDEX attempt_events_idem_uq ON attempt_events (attempt_id, client_event_id)
  WHERE client_event_id IS NOT NULL;
CREATE INDEX attempt_events_attempt_idx ON attempt_events (attempt_id, id);
CREATE INDEX attempt_events_type_idx ON attempt_events (tenant_id, type, server_ts)
  WHERE type IN ('FOCUS_LOST', 'FULLSCREEN_EXIT', 'DEVICE_CHANGED');  -- dashboard pelanggaran

CREATE TRIGGER attempt_events_append_only BEFORE UPDATE ON attempt_events
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Riwayat koreksi manual (esai); nilai terakhir disalin ke attempt_items.score
CREATE TABLE manual_grades (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  attempt_item_id  uuid NOT NULL REFERENCES attempt_items(id) ON DELETE CASCADE,
  grader_id        uuid NOT NULL REFERENCES users(id),
  score            numeric(8,2) NOT NULL CHECK (score >= 0),
  rubric_scores    jsonb,                    -- [{"criterion": "...", "score": 2}]
  comment          text,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX manual_grades_item_idx ON manual_grades (attempt_item_id, created_at DESC);

SELECT add_updated_at_trigger('attempts');

SELECT enable_tenant_rls('attempts');
SELECT enable_tenant_rls('attempt_sections');
SELECT enable_tenant_rls('attempt_items');
SELECT enable_tenant_rls('attempt_events');
SELECT enable_tenant_rls('manual_grades');
