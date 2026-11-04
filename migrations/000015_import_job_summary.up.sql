-- Ringkasan hasil impor per jenis baris (DOCS/PLAN/13-IMPOR-PER-MENU.md), mis.
-- {"created": 85, "added": 30, "already_member": 5} untuk impor peserta ke grup.
ALTER TABLE import_jobs ADD COLUMN summary jsonb NOT NULL DEFAULT '{}';
