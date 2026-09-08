-- Option A: student-level monthly quota; keys track usage only.

ALTER TABLE project_members
  ADD COLUMN IF NOT EXISTS monthly_credit_quota_milli bigint NOT NULL DEFAULT 0
  CHECK (monthly_credit_quota_milli >= 0);

CREATE TABLE IF NOT EXISTS project_member_month_credit_buckets (
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  month timestamptz NOT NULL,
  quota_milli bigint NOT NULL DEFAULT 0 CHECK (quota_milli >= 0),
  charged_milli bigint NOT NULL DEFAULT 0 CHECK (charged_milli >= 0),
  pending_milli bigint NOT NULL DEFAULT 0 CHECK (pending_milli >= 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (project_id, user_id, month)
);

CREATE INDEX IF NOT EXISTS project_member_month_credit_buckets_project_month
  ON project_member_month_credit_buckets (project_id, month);

-- Fold per-key quotas into one member quota per project.
UPDATE project_members pm
SET monthly_credit_quota_milli = sub.total
FROM (
  SELECT k.project_id, k.owner_user_id AS user_id,
         COALESCE(sum(k.monthly_credit_quota_milli) FILTER (WHERE k.status IN ('approved', 'active')), 0) AS total
  FROM api_keys k
  GROUP BY k.project_id, k.owner_user_id
  HAVING COALESCE(sum(k.monthly_credit_quota_milli) FILTER (WHERE k.status IN ('approved', 'active')), 0) > 0
) sub
WHERE pm.project_id = sub.project_id AND pm.user_id = sub.user_id;

INSERT INTO project_members (id, project_id, user_id, monthly_credit_quota_milli, created_at)
SELECT gen_random_uuid(), k.project_id, k.owner_user_id,
       COALESCE(sum(k.monthly_credit_quota_milli) FILTER (WHERE k.status IN ('approved', 'active')), 0),
       now()
FROM api_keys k
GROUP BY k.project_id, k.owner_user_id
HAVING COALESCE(sum(k.monthly_credit_quota_milli) FILTER (WHERE k.status IN ('approved', 'active')), 0) > 0
ON CONFLICT (project_id, user_id) DO UPDATE
SET monthly_credit_quota_milli = GREATEST(project_members.monthly_credit_quota_milli, EXCLUDED.monthly_credit_quota_milli);

INSERT INTO project_member_month_credit_buckets (id, project_id, user_id, month, quota_milli, charged_milli, pending_milli, created_at, updated_at)
SELECT gen_random_uuid(), agg.project_id, agg.user_id, agg.month, pm.monthly_credit_quota_milli,
       agg.charged_milli, agg.pending_milli, now(), now()
FROM (
  SELECT k.project_id, k.owner_user_id AS user_id, b.month,
         COALESCE(sum(b.charged_milli), 0) AS charged_milli,
         COALESCE(sum(b.pending_milli), 0) AS pending_milli
  FROM api_keys k
  JOIN api_key_month_credit_buckets b ON b.api_key_id = k.id
  GROUP BY k.project_id, k.owner_user_id, b.month
) agg
JOIN project_members pm ON pm.project_id = agg.project_id AND pm.user_id = agg.user_id
ON CONFLICT (project_id, user_id, month) DO UPDATE
SET quota_milli = EXCLUDED.quota_milli,
    charged_milli = EXCLUDED.charged_milli,
    pending_milli = EXCLUDED.pending_milli,
    updated_at = now();

UPDATE api_keys
SET monthly_credit_quota_milli = 0,
    mentor_monthly_credit_quota_milli = 0
WHERE status IN ('approved', 'active');

UPDATE api_key_month_credit_buckets SET quota_milli = 0;

UPDATE project_month_credit_buckets pb
SET allocated_milli = sub.total,
    updated_at = now()
FROM (
  SELECT pm.project_id,
         COALESCE(sum(pm.monthly_credit_quota_milli), 0) AS total
  FROM project_members pm
  GROUP BY pm.project_id
) sub
WHERE pb.project_id = sub.project_id;

ALTER TABLE quota_adjustment_audits DROP CONSTRAINT IF EXISTS quota_adjustment_audits_owner_type_check;
ALTER TABLE quota_adjustment_audits ADD CONSTRAINT quota_adjustment_audits_owner_type_check
  CHECK (owner_type IN ('project', 'api_key', 'member'));
