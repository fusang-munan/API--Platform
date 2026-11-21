CREATE TABLE beichen_website_key_sync (
  provider_id uuid PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'unsynced' CHECK (status IN ('unsynced', 'current', 'session_expired', 'unavailable')),
  last_seen timestamptz,
  next_attempt timestamptz NOT NULL DEFAULT clock_timestamp(),
  lease_id uuid,
  lease_until timestamptz,
  CHECK ((lease_id IS NULL) = (lease_until IS NULL))
);

CREATE TABLE beichen_website_keys (
  provider_id uuid NOT NULL REFERENCES beichen_website_key_sync(provider_id) ON DELETE CASCADE,
  upstream_key_id bigint NOT NULL CHECK (upstream_key_id BETWEEN 1 AND 9007199254740991),
  name text NOT NULL,
  key_prefix varchar(10),
  group_id bigint CHECK (group_id BETWEEN 1 AND 9007199254740991),
  group_name text,
  rate numeric CHECK (rate BETWEEN 0 AND 1000),
  status text NOT NULL,
  enabled boolean,
  created_at timestamptz,
  credential_id uuid REFERENCES provider_credentials(id) ON DELETE SET NULL,
  ciphertext_digest bytea CHECK (octet_length(ciphertext_digest) = 32),
  match_status text NOT NULL CHECK (match_status IN ('matched', 'unmatched', 'ambiguous')),
  PRIMARY KEY (provider_id, upstream_key_id),
  UNIQUE (credential_id)
);
