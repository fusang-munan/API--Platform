-- Conditional pricing evidence, student/school split and quota truncation.
-- One migration per release: all 0.17.0 ledger columns live here.
ALTER TABLE gateway_calls
  ADD COLUMN IF NOT EXISTS reasoning_effort text,
  ADD COLUMN IF NOT EXISTS cache_ttl text,
  ADD COLUMN IF NOT EXISTS price_tier text,
  ADD COLUMN IF NOT EXISTS price_snapshot jsonb,
  ADD COLUMN IF NOT EXISTS pricing_detail jsonb,
  ADD COLUMN IF NOT EXISTS actual_credits numeric,
  ADD COLUMN IF NOT EXISTS student_credits numeric,
  ADD COLUMN IF NOT EXISTS school_absorbed_credits numeric NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS truncated_quota boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS quota_budget_tokens bigint;

COMMENT ON COLUMN gateway_calls.actual_credits IS 'Full computed charge before the student quota cap';
COMMENT ON COLUMN gateway_calls.student_credits IS 'Amount actually deducted from the student bucket';
COMMENT ON COLUMN gateway_calls.school_absorbed_credits IS 'Amount over the student quota cap, carried by the school';
COMMENT ON COLUMN gateway_calls.quota_budget_tokens IS 'max_output_tokens injected from the remaining quota; NULL when not capped';

