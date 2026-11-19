-- Only explicitly provisioned school credentials participate in account discovery/archiving.
CREATE TABLE beichen_provision_channels (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('Gemini','GM1','G3','CC1')),
  key_fingerprint bytea NOT NULL CHECK (octet_length(key_fingerprint)=32),
  discover_ids boolean NOT NULL,
  upstream_user_id bigint CHECK (upstream_user_id > 0),
  checked_at timestamptz,
  discovery_error text
);

-- No school student, call or project is implied by an upstream transaction.
-- Keep financial evidence even if a local credential is later deleted.
CREATE TABLE beichen_upstream_usage_rows (
  upstream_user_id bigint NOT NULL CHECK (upstream_user_id > 0),
  row_id bigint NOT NULL CHECK (row_id > 0),
  upstream_key_id bigint NOT NULL CHECK (upstream_key_id > 0),
  provider_id uuid NOT NULL,
  credential_id uuid NOT NULL,
  channel text NOT NULL,
  request_id text,
  model text NOT NULL,
  occurred_at timestamptz NOT NULL,
  actual_cost numeric,
  currency text,
  billing_type int,
  cost_details jsonb NOT NULL,
  subscription_id bigint,
  input_tokens bigint CHECK (input_tokens >= 0),
  output_tokens bigint CHECK (output_tokens >= 0),
  cache_read_tokens bigint CHECK (cache_read_tokens >= 0),
  cache_write_tokens bigint CHECK (cache_write_tokens >= 0),
  PRIMARY KEY (upstream_user_id,row_id)
);
CREATE INDEX beichen_upstream_usage_key_time ON beichen_upstream_usage_rows(credential_id,occurred_at,row_id);
CREATE INDEX beichen_upstream_usage_provider_time ON beichen_upstream_usage_rows(provider_id,occurred_at);

CREATE TABLE beichen_usage_checkpoints (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  revision timestamptz NOT NULL,
  upstream_key_id bigint NOT NULL,
  upstream_user_id bigint,
  page integer NOT NULL DEFAULT 1 CHECK (page >= 1),
  previous_last_id bigint NOT NULL DEFAULT 0,
  generation bigint NOT NULL DEFAULT 0,
  scan_started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  caught_up boolean NOT NULL DEFAULT false,
  last_success timestamptz,
  last_error text
);
