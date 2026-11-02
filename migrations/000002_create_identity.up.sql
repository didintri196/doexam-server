-- Tenant, pengguna, peran, sesi login, grup.

CREATE TABLE tenants (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug        citext NOT NULL UNIQUE,
  name        text   NOT NULL,
  preset      text   NOT NULL DEFAULT 'school'
              CHECK (preset IN ('school', 'company', 'custom')),
  settings    jsonb  NOT NULL DEFAULT '{}',     -- label, timezone, locale, branding
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid   NOT NULL REFERENCES tenants(id),
  username              citext NOT NULL,
  email                 citext,
  password_hash         text,                 -- Argon2id; NULL jika hanya SSO (fase berikutnya)
  full_name             text   NOT NULL,
  external_id           text,                 -- NIS / NISN / ID karyawan
  status                text   NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'suspended')),
  must_change_password  boolean NOT NULL DEFAULT false,
  profile               jsonb  NOT NULL DEFAULT '{}',  -- jenis kelamin, tgl lahir, dll. (opsional)
  last_login_at         timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  deleted_at            timestamptz
);
CREATE UNIQUE INDEX users_tenant_username_uq ON users (tenant_id, username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_tenant_email_uq    ON users (tenant_id, email)    WHERE deleted_at IS NULL AND email IS NOT NULL;
CREATE UNIQUE INDEX users_tenant_extid_uq    ON users (tenant_id, external_id) WHERE deleted_at IS NULL AND external_id IS NOT NULL;

-- Peran: permission disimpan sebagai array kode, mis. {'item.write','exam.publish'}
CREATE TABLE roles (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id),
  code         text NOT NULL,                 -- 'admin','author','proctor','grader','participant'
  name         text NOT NULL,                 -- label tampilan (bisa diganti per preset)
  permissions  text[] NOT NULL DEFAULT '{}',
  is_system    boolean NOT NULL DEFAULT false,  -- peran bawaan, tidak boleh dihapus
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

CREATE TABLE user_roles (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id    uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  tenant_id  uuid NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (user_id, role_id)
);
CREATE INDEX user_roles_role_idx ON user_roles (role_id);

-- Sesi login (bukan sesi ujian)
CREATE TABLE auth_sessions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id),
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE,         -- SHA-256 dari token; token mentah tidak disimpan
  ip           inet,
  user_agent   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  revoked_at   timestamptz
);
CREATE INDEX auth_sessions_user_idx ON auth_sessions (user_id) WHERE revoked_at IS NULL;

-- Grup hierarkis: tingkat → kelas, atau divisi → tim
CREATE TABLE groups (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  parent_id   uuid REFERENCES groups(id),
  kind        text NOT NULL DEFAULT 'class'
              CHECK (kind IN ('level', 'class', 'department', 'team', 'batch', 'other')),
  code        text,
  name        text NOT NULL,
  period      text,                           -- '2026/2027', 'Rekrutmen Q1 2027'
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE INDEX groups_tenant_parent_idx ON groups (tenant_id, parent_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX groups_tenant_code_period_uq ON groups (tenant_id, code, period)
  WHERE deleted_at IS NULL AND code IS NOT NULL;

CREATE TABLE group_members (
  group_id    uuid NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  member_role text NOT NULL DEFAULT 'member'
              CHECK (member_role IN ('member', 'leader')),  -- leader = wali kelas / atasan
  joined_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_idx ON group_members (user_id);

SELECT add_updated_at_trigger('tenants');
SELECT add_updated_at_trigger('users');
SELECT add_updated_at_trigger('roles');
SELECT add_updated_at_trigger('groups');

SELECT enable_tenant_rls('users');
SELECT enable_tenant_rls('roles');
SELECT enable_tenant_rls('user_roles');
SELECT enable_tenant_rls('auth_sessions');
SELECT enable_tenant_rls('groups');
SELECT enable_tenant_rls('group_members');
