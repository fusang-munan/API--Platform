CREATE TABLE upstream_model_aliases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  credential_id uuid REFERENCES provider_credentials(id) ON DELETE CASCADE,
  upstream_model text NOT NULL,
  school_model text NOT NULL,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE NULLS NOT DISTINCT (provider_id, credential_id, upstream_model)
);

-- Retain checks for historical credentials. Unknown amounts remain NULL.
CREATE TABLE billing_checks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL,
  credential_id uuid NOT NULL,
  model text NOT NULL,
  day date NOT NULL,
  local_credits numeric,
  upstream_credits numeric,
  diff_credits numeric,
  status text NOT NULL CHECK (status IN ('ok', 'anomaly', 'missing')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (provider_id, credential_id, model, day)
);

CREATE INDEX billing_checks_provider_day ON billing_checks (provider_id, day);
