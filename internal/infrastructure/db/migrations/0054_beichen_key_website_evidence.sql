-- Existing public aggregates have no website Key identity. Keep their rows
-- for audit, but require the website evidence columns when presenting a Key.
ALTER TABLE beichen_key_observations
  ADD COLUMN website_key_id bigint CHECK (website_key_id > 0),
  ADD COLUMN website_group jsonb CHECK (jsonb_typeof(website_group) = 'object');

ALTER TABLE beichen_key_usage_days
  ADD COLUMN website_key_id bigint CHECK (website_key_id > 0),
  ADD COLUMN raw_website_pages bytea,
  ADD CHECK ((website_key_id IS NULL) = (raw_website_pages IS NULL));

ALTER TABLE beichen_crawl_runs
  ADD COLUMN raw_website_response bytea;

ALTER TABLE beichen_price_versions
  ADD COLUMN group_id bigint CHECK (group_id BETWEEN 1 AND 9007199254740991);

UPDATE beichen_price_versions price
SET group_id = snapshot.group_id
FROM beichen_panel_snapshots snapshot
WHERE price.source = 'panel' AND snapshot.credential_id = price.credential_id
  AND snapshot.effective_at = price.effective_at AND snapshot.group_id IS NOT NULL;
