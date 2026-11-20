ALTER TABLE provider_model_catalog
  ADD COLUMN supported_protocols text[] NOT NULL DEFAULT '{}';

-- The newest observation of each current Key, model and protocol.
CREATE TABLE upstream_probes (
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  credential_id uuid NOT NULL REFERENCES provider_credentials(id) ON DELETE CASCADE,
  model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 256),
  protocol text NOT NULL CHECK (protocol IN ('openai_compatible','openai_responses','anthropic','google_gemini_v1beta')),
  status_code integer NOT NULL CHECK (status_code = 0 OR status_code BETWEEN 100 AND 599),
  error_code text CHECK (error_code IN ('credential_unreadable','timeout','network_error','invalid_response','http_error')),
  duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
  checked_at timestamptz NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object'),
  PRIMARY KEY (provider_id, credential_id, model, protocol),
  CHECK (error_code IS NOT NULL OR status_code BETWEEN 200 AND 299)
);
CREATE INDEX upstream_probes_recency ON upstream_probes (provider_id, checked_at DESC);

CREATE TABLE beichen_probe_jobs (
  provider_id uuid PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  requested_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  lease_id uuid,
  lease_until timestamptz,
  last_error text,
  CHECK ((lease_id IS NULL) = (lease_until IS NULL))
);

-- The website model names are captured independently of usable price terms.
CREATE TABLE beichen_website_catalogs (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  credential_revision timestamptz NOT NULL,
  group_id bigint NOT NULL CHECK (group_id > 0),
  checked_at timestamptz NOT NULL,
  models text[] NOT NULL
);
