-- Log audit append-only: siapa mengubah apa & kapan.

CREATE TABLE audit_logs (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id    uuid REFERENCES tenants(id),   -- NULL = aksi tingkat sistem
  actor_id     uuid REFERENCES users(id),
  action       text NOT NULL,                 -- 'item.update', 'exam.publish', 'grade.override', ...
  entity_type  text NOT NULL,
  entity_id    uuid,
  before       jsonb,
  after        jsonb,
  ip           inet,
  user_agent   text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_entity_idx ON audit_logs (entity_type, entity_id, created_at DESC);
CREATE INDEX audit_logs_tenant_time_idx ON audit_logs (tenant_id, created_at DESC);

CREATE TRIGGER audit_logs_append_only BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

SELECT enable_tenant_rls('audit_logs');
