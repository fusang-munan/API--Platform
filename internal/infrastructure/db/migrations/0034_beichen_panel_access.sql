CREATE TABLE beichen_panel_access (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  group_id bigint CHECK (group_id > 0),
  upstream_key_id bigint CHECK (upstream_key_id BETWEEN 1 AND 9007199254740991),
  token_ciphertext bytea,
  updated_by uuid NOT NULL REFERENCES users(id),
  updated_at timestamptz NOT NULL,
  CHECK ((group_id IS NULL) = (token_ciphertext IS NULL)),
  CHECK (upstream_key_id IS NULL OR group_id IS NOT NULL)
);
