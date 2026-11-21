CREATE TABLE beichen_crawl_runs (
  provider_id uuid PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  started_at timestamptz NOT NULL,
  duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
  platform_count integer NOT NULL CHECK (platform_count >= 0),
  group_count integer NOT NULL CHECK (group_count >= 0),
  model_count integer NOT NULL CHECK (model_count >= 0),
  expected_model_count integer NOT NULL CHECK (expected_model_count >= model_count),
  failures jsonb NOT NULL CHECK (jsonb_typeof(failures) = 'array')
);
