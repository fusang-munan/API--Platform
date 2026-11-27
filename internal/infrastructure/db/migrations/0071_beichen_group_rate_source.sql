DO $migration$
DECLARE
  source_check record;
BEGIN
  FOR source_check IN
    SELECT c.conname
    FROM pg_constraint c
    WHERE c.conrelid='beichen_multiplier_versions'::regclass AND c.contype='c'
      AND pg_get_constraintdef(c.oid) = $old$CHECK (((account_rate_source IS NULL) OR ((account_rate_source = 'key_billing'::text) AND (peak_enabled IS FALSE))))$old$
  LOOP
    EXECUTE format('ALTER TABLE beichen_multiplier_versions DROP CONSTRAINT %I', source_check.conname);
  END LOOP;
END
$migration$;

ALTER TABLE beichen_multiplier_versions
  DROP CONSTRAINT IF EXISTS beichen_multiplier_versions_rate_source_peak_check,
  ADD CONSTRAINT beichen_multiplier_versions_rate_source_peak_check CHECK (
    account_rate_source IS NULL
    OR (account_rate_source = 'key_billing' AND peak_enabled IS FALSE)
    OR (account_rate_source = 'group_rate' AND peak_enabled IS NOT NULL)
  );
