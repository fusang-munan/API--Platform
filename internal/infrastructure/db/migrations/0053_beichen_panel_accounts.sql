CREATE TABLE beichen_panel_accounts (
  provider_id uuid PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  mode text NOT NULL CHECK (mode IN ('password', 'token')),
  email_ciphertext bytea,
  password_ciphertext bytea,
  token_ciphertext bytea,
  refresh_token_ciphertext bytea,
  expires_at timestamptz,
  last_verified_at timestamptz,
  status text NOT NULL CHECK (status IN ('none', 'active', 'expired', 'error')),
  last_error text,
  failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  local_grant_hash bytea CHECK (octet_length(local_grant_hash) = 32),
  local_granted_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  local_granted_at timestamptz,
  CHECK ((local_grant_hash IS NULL AND local_granted_by IS NULL AND local_granted_at IS NULL)
    OR (local_grant_hash IS NOT NULL AND local_granted_by IS NOT NULL AND local_granted_at IS NOT NULL)),
  CHECK ((mode = 'token' AND email_ciphertext IS NULL AND password_ciphertext IS NULL AND refresh_token_ciphertext IS NULL)
    OR (mode = 'password' AND (password_ciphertext IS NULL OR email_ciphertext IS NOT NULL))),
  CHECK (status <> 'active' OR token_ciphertext IS NOT NULL),
  CHECK (status <> 'none' OR (email_ciphertext IS NULL AND password_ciphertext IS NULL
    AND token_ciphertext IS NULL AND refresh_token_ciphertext IS NULL AND expires_at IS NULL
    AND last_verified_at IS NULL AND failure_count = 0))
);
