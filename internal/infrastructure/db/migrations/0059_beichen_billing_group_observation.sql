ALTER TABLE beichen_datasets ADD COLUMN website_group_observation jsonb
  CHECK (website_group_observation IS NULL OR
    (kind = 'billing' AND jsonb_typeof(website_group_observation) = 'object'));
