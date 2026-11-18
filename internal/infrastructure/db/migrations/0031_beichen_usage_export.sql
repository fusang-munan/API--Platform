ALTER TABLE beichen_daily_usage
  DROP CONSTRAINT beichen_daily_usage_source_check,
  ADD CONSTRAINT beichen_daily_usage_source_check CHECK (source IN ('usage','csv','export'));
