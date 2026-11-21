CREATE TABLE beichen_website_key_rate_versions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider_id uuid NOT NULL REFERENCES beichen_website_key_sync(provider_id) ON DELETE CASCADE,
  upstream_key_id bigint NOT NULL CHECK (upstream_key_id BETWEEN 1 AND 9007199254740991),
  group_id bigint CHECK (group_id BETWEEN 1 AND 9007199254740991),
  group_name text,
  rate numeric CHECK (rate BETWEEN 0 AND 1000),
  peak_enabled boolean,
  peak_rate numeric CHECK (peak_rate BETWEEN 0 AND 1000),
  peak_start text,
  peak_end text,
  observed_at timestamptz NOT NULL,
  changed boolean NOT NULL
);

CREATE INDEX beichen_website_key_rate_versions_latest
  ON beichen_website_key_rate_versions (provider_id, upstream_key_id, id DESC);
