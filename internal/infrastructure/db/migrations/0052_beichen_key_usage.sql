ALTER TABLE beichen_datasets DROP CONSTRAINT beichen_datasets_kind_check,
  ADD CONSTRAINT beichen_datasets_kind_check CHECK (kind IN ('catalog','billing','prices','usage','key_usage'));

CREATE TABLE beichen_key_observations (
  credential_id uuid NOT NULL REFERENCES provider_credentials(id) ON DELETE CASCADE,
  key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
  models jsonb CHECK (jsonb_typeof(models) = 'array'),
  models_fetched_at timestamptz,
  models_error text,
  models_unchanged boolean NOT NULL DEFAULT false,
  baseline_models jsonb CHECK (jsonb_typeof(baseline_models) = 'array'),
  balance numeric,
  unit varchar(32),
  usage_fetched_at timestamptz,
  usage_error text,
  model_stats jsonb CHECK (jsonb_typeof(model_stats) = 'array'),
  model_day date,
  baseline_multiplier numeric,
  target_multiplier numeric CHECK (target_multiplier BETWEEN 0 AND 1000),
  expect_claude boolean NOT NULL DEFAULT false,
  PRIMARY KEY (credential_id, key_digest)
);

CREATE TABLE beichen_key_usage_days (
  credential_id uuid NOT NULL REFERENCES provider_credentials(id) ON DELETE CASCADE,
  key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
  day date NOT NULL,
  unit varchar(32) NOT NULL,
  daily_usage jsonb NOT NULL CHECK (jsonb_typeof(daily_usage) = 'object'),
  model_stats jsonb CHECK (jsonb_typeof(model_stats) = 'array'),
  model_fetched_at timestamptz,
  fetched_at timestamptz NOT NULL,
  PRIMARY KEY (credential_id, key_digest, day)
);
