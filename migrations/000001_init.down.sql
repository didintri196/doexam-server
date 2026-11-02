DROP FUNCTION IF EXISTS add_updated_at_trigger(regclass);
DROP FUNCTION IF EXISTS enable_tenant_rls(regclass);
DROP FUNCTION IF EXISTS current_tenant_id();
DROP FUNCTION IF EXISTS forbid_mutation();
DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS citext;
