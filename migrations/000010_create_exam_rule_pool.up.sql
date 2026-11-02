-- Snapshot kandidat soal per aturan blueprint, dibuat saat ujian diterbitkan.
-- Attempt menarik soal acak dari sini (bukan dari bank yang masih bisa berubah),
-- sehingga semua peserta mendapat soal dari kumpulan versi yang sama.

CREATE TABLE exam_rule_pool (
  rule_id          uuid NOT NULL REFERENCES exam_section_rules(id) ON DELETE CASCADE,
  item_version_id  uuid NOT NULL REFERENCES item_versions(id),
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (rule_id, item_version_id)
);
CREATE INDEX exam_rule_pool_version_idx ON exam_rule_pool (item_version_id);

SELECT enable_tenant_rls('exam_rule_pool');
