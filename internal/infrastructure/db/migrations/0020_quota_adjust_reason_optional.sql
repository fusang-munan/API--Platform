-- Quota adjustment reason is optional.

ALTER TABLE quota_adjustment_audits DROP CONSTRAINT IF EXISTS quota_adjustment_audits_reason_check;
ALTER TABLE quota_adjustment_audits ALTER COLUMN reason DROP NOT NULL;
