-- Fondasi: extension & fungsi bersama yang dipakai semua migrasi berikutnya.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Dipakai tabel append-only (attempt_events, audit_logs)
CREATE OR REPLACE FUNCTION forbid_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'tabel % bersifat append-only', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

-- Tenant aktif dari `SET LOCAL app.tenant_id = '<uuid>'`.
-- NULL bila belum di-set atau kosong → policy menolak semua baris (fail closed).
CREATE OR REPLACE FUNCTION current_tenant_id() RETURNS uuid AS $$
  SELECT nullif(current_setting('app.tenant_id', true), '')::uuid
$$ LANGUAGE sql STABLE;

-- Helper migrasi: aktifkan RLS isolasi tenant pada sebuah tabel.
CREATE OR REPLACE FUNCTION enable_tenant_rls(t regclass) RETURNS void AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
  EXECUTE format(
    'CREATE POLICY tenant_isolation ON %s
       USING (tenant_id = current_tenant_id())
       WITH CHECK (tenant_id = current_tenant_id())', t);
END;
$$ LANGUAGE plpgsql;

-- Helper migrasi: pasang trigger updated_at pada sebuah tabel.
CREATE OR REPLACE FUNCTION add_updated_at_trigger(t regclass) RETURNS void AS $$
BEGIN
  EXECUTE format(
    'CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION set_updated_at()',
    t::text || '_updated_at', t);
END;
$$ LANGUAGE plpgsql;
