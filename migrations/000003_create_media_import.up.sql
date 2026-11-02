-- File media & job impor Excel/Word.

CREATE TABLE media_files (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid   NOT NULL REFERENCES tenants(id),
  storage_key    text   NOT NULL UNIQUE,      -- path di filesystem / S3
  original_name  text   NOT NULL,
  mime_type      text   NOT NULL,
  size_bytes     bigint NOT NULL CHECK (size_bytes >= 0),
  sha256         bytea  NOT NULL,
  uploaded_by    uuid REFERENCES users(id),
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX media_files_tenant_sha_idx ON media_files (tenant_id, sha256);  -- dedup

CREATE TABLE import_jobs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  kind          text NOT NULL CHECK (kind IN ('users', 'groups', 'items')),
  source_file_id uuid NOT NULL REFERENCES media_files(id),
  target        jsonb NOT NULL DEFAULT '{}',  -- mis. {"bank_id": "..."} atau {"group_id": "..."}
  status        text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'validating', 'ready', 'importing', 'done', 'failed')),
  total_rows    int NOT NULL DEFAULT 0,
  ok_rows       int NOT NULL DEFAULT 0,
  error_rows    int NOT NULL DEFAULT 0,
  errors        jsonb NOT NULL DEFAULT '[]',  -- [{"row": 12, "field": "nis", "message": "..."}]
  created_by    uuid REFERENCES users(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz
);

SELECT enable_tenant_rls('media_files');
SELECT enable_tenant_rls('import_jobs');
