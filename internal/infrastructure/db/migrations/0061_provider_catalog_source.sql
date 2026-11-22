ALTER TABLE provider_model_catalog ADD COLUMN catalog_source text NOT NULL DEFAULT 'key';

UPDATE provider_model_catalog
SET catalog_source = 'website'
WHERE raw_entry->>'source' = 'beichen_website_group';
