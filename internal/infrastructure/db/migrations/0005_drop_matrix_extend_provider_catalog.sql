DROP TABLE IF EXISTS matrix_model_catalog;

ALTER TABLE provider_model_catalog
  ADD COLUMN IF NOT EXISTS display_name varchar(256),
  ADD COLUMN IF NOT EXISTS upstream_object varchar(64),
  ADD COLUMN IF NOT EXISTS upstream_created_at varchar(64);

CREATE INDEX IF NOT EXISTS provider_model_catalog_raw_entry_gin
  ON provider_model_catalog USING gin (raw_entry);
