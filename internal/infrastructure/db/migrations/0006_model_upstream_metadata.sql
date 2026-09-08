ALTER TABLE models
  ADD COLUMN IF NOT EXISTS upstream_metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS models_upstream_metadata_gin
  ON models USING gin (upstream_metadata);
