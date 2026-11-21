CREATE TABLE beichen_group_price_observations (
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  group_id bigint NOT NULL CHECK (group_id BETWEEN 1 AND 9007199254740991),
  model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 256),
  observed_at timestamptz NOT NULL,
  unit text NOT NULL DEFAULT '元（北辰余额单位）' CHECK (unit = '元（北辰余额单位）'),
  group_rules jsonb NOT NULL CHECK (jsonb_typeof(group_rules) = 'object'),
  pricing jsonb CHECK (pricing IS NULL OR jsonb_typeof(pricing) = 'object'),
  PRIMARY KEY (provider_id, group_id, model, observed_at)
);

ALTER TABLE beichen_crawl_runs
  ADD COLUMN key_model_comparisons jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(key_model_comparisons) = 'array'),
  ADD COLUMN key_model_error text,
  ADD COLUMN account_rate_error text;
