ALTER TABLE provider_credentials ADD COLUMN IF NOT EXISTS retired_at timestamptz;

UPDATE provider_credentials
SET retired_at = updated_at
WHERE COALESCE(status, 'active') <> 'active' AND retired_at IS NULL;
