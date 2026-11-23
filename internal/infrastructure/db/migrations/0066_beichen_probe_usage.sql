-- A sweep keeps its ID when a new worker takes over its lease.
ALTER TABLE beichen_probe_jobs ADD COLUMN job_id uuid NOT NULL DEFAULT gen_random_uuid();

CREATE TABLE beichen_probe_usage (
  id uuid PRIMARY KEY,
  provider_id uuid NOT NULL REFERENCES providers(id),
  credential_id uuid NOT NULL REFERENCES provider_credentials(id),
  model text NOT NULL,
  protocol text NOT NULL,
  job_id uuid,
  status_code integer CHECK (status_code BETWEEN 100 AND 599),
  error_code text,
  input_tokens bigint CHECK (input_tokens >= 0),
  output_tokens bigint CHECK (output_tokens >= 0),
  cache_read_tokens bigint CHECK (cache_read_tokens >= 0),
  cache_write_tokens bigint CHECK (cache_write_tokens >= 0),
  checked_at timestamptz NOT NULL
);
CREATE INDEX beichen_probe_usage_provider_time ON beichen_probe_usage(provider_id, checked_at);
CREATE INDEX beichen_probe_usage_credential_time ON beichen_probe_usage(credential_id, checked_at);
CREATE INDEX beichen_probe_usage_day ON beichen_probe_usage(checked_at);
CREATE INDEX beichen_probe_usage_job ON beichen_probe_usage(job_id);
