-- Bank soal: mapel, kompetensi, bank, stimulus, soal & versinya.

CREATE TABLE subjects (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  code        text NOT NULL,
  name        text NOT NULL,                  -- 'Matematika', 'Tes Logika', 'Bahasa Inggris'
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE UNIQUE INDEX subjects_tenant_code_uq ON subjects (tenant_id, code) WHERE deleted_at IS NULL;

-- Pohon kompetensi / kisi-kisi (CP, TP, KD, atau kompetensi jabatan)
CREATE TABLE competencies (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  subject_id  uuid NOT NULL REFERENCES subjects(id),
  parent_id   uuid REFERENCES competencies(id),
  code        text NOT NULL,
  name        text NOT NULL,
  sort_order  int  NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE UNIQUE INDEX competencies_subject_code_uq ON competencies (subject_id, code) WHERE deleted_at IS NULL;
CREATE INDEX competencies_parent_idx ON competencies (parent_id);

CREATE TABLE item_banks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  subject_id  uuid NOT NULL REFERENCES subjects(id),
  name        text NOT NULL,
  description text,
  created_by  uuid REFERENCES users(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE INDEX item_banks_tenant_subject_idx ON item_banks (tenant_id, subject_id) WHERE deleted_at IS NULL;

-- Akses per bank soal (guru mapel hanya melihat bank miliknya)
CREATE TABLE item_bank_members (
  bank_id     uuid NOT NULL REFERENCES item_banks(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  access      text NOT NULL DEFAULT 'editor' CHECK (access IN ('owner', 'editor', 'viewer')),
  PRIMARY KEY (bank_id, user_id)
);
CREATE INDEX item_bank_members_user_idx ON item_bank_members (user_id);

-- Stimulus: bacaan/gambar/audio yang dipakai beberapa soal
CREATE TABLE stimuli (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id),
  bank_id             uuid NOT NULL REFERENCES item_banks(id),
  current_version_id  uuid,                   -- FK ditambahkan setelah stimulus_versions dibuat
  title               text NOT NULL,
  created_by          uuid REFERENCES users(id),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  deleted_at          timestamptz
);

CREATE TABLE stimulus_versions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id),
  stimulus_id  uuid NOT NULL REFERENCES stimuli(id),
  version_no   int  NOT NULL,
  content      jsonb NOT NULL,                -- {"body": "<HTML tersanitasi>"}; media via data-media-id
  locked_at    timestamptz,                   -- terisi saat pertama dipakai ujian → immutable
  created_by   uuid REFERENCES users(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (stimulus_id, version_no)
);
ALTER TABLE stimuli ADD CONSTRAINT stimuli_current_version_fk
  FOREIGN KEY (current_version_id) REFERENCES stimulus_versions(id) DEFERRABLE INITIALLY DEFERRED;

-- Item = identitas soal yang stabil; isi soal ada di item_versions
CREATE TABLE items (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id),
  bank_id             uuid NOT NULL REFERENCES item_banks(id),
  code                text,                   -- kode soal opsional, mis. 'MTK-7-001'
  current_version_id  uuid,                   -- FK ditambahkan di bawah
  status              text NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('draft', 'active', 'retired')),
  created_by          uuid REFERENCES users(id),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  deleted_at          timestamptz
);
CREATE INDEX items_bank_status_idx ON items (bank_id, status) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX items_bank_code_uq ON items (bank_id, code) WHERE deleted_at IS NULL AND code IS NOT NULL;

CREATE TABLE item_versions (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id),
  item_id              uuid NOT NULL REFERENCES items(id),
  version_no           int  NOT NULL,
  type                 text NOT NULL
                       CHECK (type IN ('mcq_single', 'mcq_multi', 'true_false', 'short_answer', 'essay')),
  stimulus_version_id  uuid REFERENCES stimulus_versions(id),
  competency_id        uuid REFERENCES competencies(id),
  content              jsonb NOT NULL,        -- stem + opsi (dikirim ke peserta, TANPA kunci)
  answer_key           jsonb,                 -- kunci jawaban (TIDAK pernah dikirim ke peserta)
  scoring              jsonb NOT NULL DEFAULT '{}',  -- metode parsial, toleransi, dll.
  explanation          jsonb,                 -- pembahasan
  default_points       numeric(8,2) NOT NULL DEFAULT 1 CHECK (default_points >= 0),
  difficulty           text CHECK (difficulty IN ('easy', 'medium', 'hard')),  -- estimasi ahli
  cognitive_level      text,                  -- 'C1'..'C6' / 'LOTS','MOTS','HOTS'
  tags                 text[] NOT NULL DEFAULT '{}',
  search_text          text,                  -- teks polos stem+opsi untuk pencarian, diisi aplikasi
  locked_at            timestamptz,           -- terisi saat dipakai ujian terbit → immutable
  created_by           uuid REFERENCES users(id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (item_id, version_no),
  CHECK (type = 'essay' OR answer_key IS NOT NULL)
);
CREATE INDEX item_versions_competency_idx ON item_versions (competency_id);
CREATE INDEX item_versions_tags_gin       ON item_versions USING gin (tags);
CREATE INDEX item_versions_search_idx     ON item_versions USING gin (to_tsvector('simple', coalesce(search_text, '')));

ALTER TABLE items ADD CONSTRAINT items_current_version_fk
  FOREIGN KEY (current_version_id) REFERENCES item_versions(id) DEFERRABLE INITIALLY DEFERRED;

-- Versi yang sudah dikunci tidak boleh diubah isinya
CREATE OR REPLACE FUNCTION prevent_locked_version_update() RETURNS trigger AS $$
BEGIN
  IF OLD.locked_at IS NOT NULL AND (
       NEW.content IS DISTINCT FROM OLD.content
    OR NEW.answer_key IS DISTINCT FROM OLD.answer_key
    OR NEW.scoring IS DISTINCT FROM OLD.scoring
    OR NEW.type IS DISTINCT FROM OLD.type) THEN
    RAISE EXCEPTION 'versi % sudah dipakai ujian dan tidak boleh diubah; buat versi baru', OLD.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER item_versions_immutable BEFORE UPDATE ON item_versions
  FOR EACH ROW EXECUTE FUNCTION prevent_locked_version_update();

CREATE OR REPLACE FUNCTION prevent_locked_stimulus_update() RETURNS trigger AS $$
BEGIN
  IF OLD.locked_at IS NOT NULL AND NEW.content IS DISTINCT FROM OLD.content THEN
    RAISE EXCEPTION 'stimulus % sudah dipakai ujian dan tidak boleh diubah; buat versi baru', OLD.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER stimulus_versions_immutable BEFORE UPDATE ON stimulus_versions
  FOR EACH ROW EXECUTE FUNCTION prevent_locked_stimulus_update();

SELECT add_updated_at_trigger('subjects');
SELECT add_updated_at_trigger('competencies');
SELECT add_updated_at_trigger('item_banks');
SELECT add_updated_at_trigger('stimuli');
SELECT add_updated_at_trigger('items');

SELECT enable_tenant_rls('subjects');
SELECT enable_tenant_rls('competencies');
SELECT enable_tenant_rls('item_banks');
SELECT enable_tenant_rls('item_bank_members');
SELECT enable_tenant_rls('stimuli');
SELECT enable_tenant_rls('stimulus_versions');
SELECT enable_tenant_rls('items');
SELECT enable_tenant_rls('item_versions');
