ALTER TABLE gateway_calls
  ADD COLUMN IF NOT EXISTS cache_hit_tokens bigint CHECK (cache_hit_tokens >= 0),
  ADD COLUMN IF NOT EXISTS cache_miss_tokens bigint CHECK (cache_miss_tokens >= 0),
  ADD COLUMN IF NOT EXISTS cache_write_tokens bigint CHECK (cache_write_tokens >= 0),
  ADD COLUMN IF NOT EXISTS reasoning_tokens bigint CHECK (reasoning_tokens >= 0),
  ADD COLUMN IF NOT EXISTS upstream_credit_micros bigint CHECK (upstream_credit_micros >= 0),
  ADD COLUMN IF NOT EXISTS provider_credential_id uuid,
  ADD COLUMN IF NOT EXISTS binding_id uuid,
  ADD COLUMN IF NOT EXISTS upstream_model_name text,
  ADD COLUMN IF NOT EXISTS usage_state text CHECK (usage_state IN ('reported', 'missing', 'interrupted'));
