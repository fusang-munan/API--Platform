ALTER TABLE beichen_multiplier_versions
  ADD COLUMN account_rate_source text CHECK (
    account_rate_source IS NULL OR (account_rate_source = 'key_billing' AND peak_enabled IS FALSE));
