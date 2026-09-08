-- New projects no longer inherit a hidden 20k credit default; quota is set explicitly at creation or via project budgets.
ALTER TABLE projects ALTER COLUMN monthly_credit_quota_milli SET DEFAULT 0;
