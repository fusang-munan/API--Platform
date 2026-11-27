DO $$
DECLARE
  source_check record;
BEGIN
  FOR source_check IN
    SELECT c.conname
    FROM pg_constraint c
    JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
    WHERE c.conrelid='beichen_multiplier_versions'::regclass AND c.contype='c'
      AND a.attname='account_rate_source'
  LOOP
    EXECUTE format('ALTER TABLE beichen_multiplier_versions DROP CONSTRAINT %I', source_check.conname);
  END LOOP;
END
$$;

ALTER TABLE beichen_multiplier_versions
  ADD CONSTRAINT beichen_multiplier_versions_rate_source_peak_check CHECK (
    account_rate_source IS NULL
    OR (account_rate_source = 'key_billing' AND peak_enabled IS FALSE)
    OR (account_rate_source = 'group_rate' AND peak_enabled IS NOT NULL)
  );
