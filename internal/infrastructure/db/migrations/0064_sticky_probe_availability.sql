ALTER TABLE upstream_probes ADD COLUMN IF NOT EXISTS last_passed_at timestamptz;

-- Preserve the most recent successful probe so one failed sweep cannot erase
-- the evidence that a model was usable before the failure.
UPDATE upstream_probes r
SET last_passed_at = r.checked_at
FROM provider_credentials c
WHERE c.id = r.credential_id AND c.provider_id = r.provider_id AND r.checked_at >= c.updated_at
  AND r.last_passed_at IS NULL AND r.error_code IS NULL AND r.status_code BETWEEN 200 AND 299;
