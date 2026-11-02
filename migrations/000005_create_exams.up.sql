-- Definisi ujian: section, soal manual, aturan blueprint, penyesuaian regrade.

CREATE TABLE exams (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id),
  subject_id         uuid REFERENCES subjects(id),
  title              text NOT NULL,
  description        text,
  instructions       jsonb,                   -- petunjuk umum {"body": "<HTML tersanitasi>"}
  mode               text NOT NULL DEFAULT 'linear'
                     CHECK (mode IN ('linear', 'random_blueprint')),   -- fase 2: 'multi_section','cat','mst'
  status             text NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'published', 'archived')),
  duration_minutes   int CHECK (duration_minutes > 0),  -- NULL = jumlah durasi section
  navigation         text NOT NULL DEFAULT 'free'
                     CHECK (navigation IN ('free', 'forward_only')),
  shuffle_items      boolean NOT NULL DEFAULT true,
  shuffle_options    boolean NOT NULL DEFAULT true,
  result_visibility  text NOT NULL DEFAULT 'after_grading'
                     CHECK (result_visibility IN ('immediately', 'after_grading', 'after_session', 'never')),
  show_explanation   boolean NOT NULL DEFAULT false,
  max_attempts       smallint NOT NULL DEFAULT 1 CHECK (max_attempts >= 1),
  attempt_scoring    text NOT NULL DEFAULT 'highest'
                     CHECK (attempt_scoring IN ('highest', 'latest', 'average')),
  passing_score      numeric(6,2),            -- KKM / passing grade (skala 0–100)
  negative_marking   numeric(6,2) NOT NULL DEFAULT 0 CHECK (negative_marking >= 0),  -- pengurang per jawaban salah
  security           jsonb NOT NULL DEFAULT '{"profile":"light"}',  -- fullscreen, deteksi tab, 1 perangkat
  settings           jsonb NOT NULL DEFAULT '{}',  -- pengaturan UI lain (kalkulator, ukuran font, dll.)
  published_at       timestamptz,
  created_by         uuid REFERENCES users(id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  deleted_at         timestamptz
);
CREATE INDEX exams_tenant_status_idx ON exams (tenant_id, status) WHERE deleted_at IS NULL;

CREATE TABLE exam_sections (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id),
  exam_id           uuid NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
  sort_order        int  NOT NULL,
  title             text NOT NULL,
  instructions      jsonb,
  duration_minutes  int CHECK (duration_minutes > 0),  -- NULL = berbagi timer ujian
  selection         text NOT NULL DEFAULT 'fixed'
                    CHECK (selection IN ('fixed', 'random')),  -- fixed = pilih manual, random = blueprint
  allow_revisit     boolean NOT NULL DEFAULT true,  -- boleh kembali ke section ini setelah lanjut
  passing_score     numeric(6,2),
  UNIQUE (exam_id, sort_order) DEFERRABLE INITIALLY DEFERRED
);

-- Soal yang dipilih manual (selection = 'fixed')
CREATE TABLE exam_section_items (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  section_id       uuid NOT NULL REFERENCES exam_sections(id) ON DELETE CASCADE,
  item_id          uuid NOT NULL REFERENCES items(id),
  item_version_id  uuid REFERENCES item_versions(id),  -- dipatok saat ujian diterbitkan
  sort_order       int  NOT NULL,
  points           numeric(8,2),              -- NULL = pakai default_points
  lock_options     boolean NOT NULL DEFAULT false,  -- opsi tidak diacak (mis. "semua benar")
  UNIQUE (section_id, item_id),
  UNIQUE (section_id, sort_order) DEFERRABLE INITIALLY DEFERRED
);

-- Aturan blueprint (selection = 'random'): ambil N soal yang cocok filter
CREATE TABLE exam_section_rules (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id),
  section_id     uuid NOT NULL REFERENCES exam_sections(id) ON DELETE CASCADE,
  sort_order     int  NOT NULL DEFAULT 0,
  bank_id        uuid NOT NULL REFERENCES item_banks(id),
  competency_id  uuid REFERENCES competencies(id),
  item_type      text,
  difficulty     text CHECK (difficulty IN ('easy', 'medium', 'hard')),
  tags_any       text[] NOT NULL DEFAULT '{}',
  item_count     int NOT NULL CHECK (item_count > 0),
  points         numeric(8,2)                 -- NULL = pakai default_points
);

-- Penyesuaian setelah ujian berjalan: anulir soal / ganti kunci → regrade
CREATE TABLE exam_item_adjustments (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  exam_id          uuid NOT NULL REFERENCES exams(id),
  item_version_id  uuid NOT NULL REFERENCES item_versions(id),
  action           text NOT NULL CHECK (action IN ('annul_full_credit', 'annul_remove', 'override_key')),
  answer_key       jsonb,                     -- wajib untuk override_key
  reason           text NOT NULL,
  created_by       uuid REFERENCES users(id),
  created_at       timestamptz NOT NULL DEFAULT now(),
  revoked_at       timestamptz,
  CHECK (action <> 'override_key' OR answer_key IS NOT NULL)
);
CREATE UNIQUE INDEX exam_item_adjustments_active_uq
  ON exam_item_adjustments (exam_id, item_version_id) WHERE revoked_at IS NULL;

SELECT add_updated_at_trigger('exams');

SELECT enable_tenant_rls('exams');
SELECT enable_tenant_rls('exam_sections');
SELECT enable_tenant_rls('exam_section_items');
SELECT enable_tenant_rls('exam_section_rules');
SELECT enable_tenant_rls('exam_item_adjustments');
