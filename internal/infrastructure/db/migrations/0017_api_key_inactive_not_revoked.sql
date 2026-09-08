-- Replace API key "revoked" with "inactive": one slot per student×project, deactivate blocks re-apply.

DROP INDEX IF EXISTS api_keys_owner_project_live;
DROP INDEX IF EXISTS api_key_owner_user_id_project_id;

CREATE TEMP TABLE doomed_api_keys AS
WITH ranked AS (
  SELECT id,
         ROW_NUMBER() OVER (
           PARTITION BY owner_user_id, project_id
           ORDER BY CASE status
             WHEN 'active' THEN 1
             WHEN 'approved' THEN 2
             WHEN 'pending_mentor' THEN 3
             WHEN 'pending_teacher' THEN 4
             WHEN 'inactive' THEN 5
             WHEN 'revoked' THEN 6
             ELSE 7
           END,
           created_at DESC
         ) AS rn
  FROM api_keys
  WHERE status IN ('pending_mentor', 'pending_teacher', 'approved', 'active', 'inactive', 'revoked')
)
SELECT id FROM ranked WHERE rn > 1;

DELETE FROM monitored_inputs
WHERE call_id IN (SELECT id FROM gateway_calls WHERE api_key_id IN (SELECT id FROM doomed_api_keys));

DELETE FROM gateway_call_attempts
WHERE call_id IN (SELECT id FROM gateway_calls WHERE api_key_id IN (SELECT id FROM doomed_api_keys));

DELETE FROM gateway_calls WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM gateway_call_turns WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM monthly_usage_cube WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM api_key_models WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM api_key_audits WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM api_key_month_credit_buckets WHERE api_key_id IN (SELECT id FROM doomed_api_keys);
DELETE FROM api_keys WHERE id IN (SELECT id FROM doomed_api_keys);

UPDATE api_keys SET status = 'inactive', updated_at = now() WHERE status = 'revoked';

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_status_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_status_check
  CHECK (status IN ('pending_mentor', 'pending_teacher', 'approved', 'active', 'rejected', 'inactive'));

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_key_material_matches_status;
ALTER TABLE api_keys ADD CONSTRAINT api_key_material_matches_status CHECK (
  (status IN ('pending_mentor', 'pending_teacher', 'approved', 'rejected')
    AND key_hash IS NULL AND key_prefix IS NULL AND claimed_at IS NULL AND revoked_at IS NULL)
  OR (status = 'active'
    AND key_hash IS NOT NULL AND key_prefix IS NOT NULL AND claimed_at IS NOT NULL AND revoked_at IS NULL)
  OR (status = 'inactive'
    AND key_hash IS NOT NULL AND key_prefix IS NOT NULL AND claimed_at IS NOT NULL AND revoked_at IS NOT NULL)
);

CREATE UNIQUE INDEX api_keys_owner_project_live
  ON api_keys(owner_user_id, project_id)
  WHERE status IN ('pending_mentor', 'pending_teacher', 'approved', 'active', 'inactive');

CREATE UNIQUE INDEX api_key_owner_user_id_project_id
  ON api_keys(owner_user_id, project_id)
  WHERE status IN ('pending_mentor', 'pending_teacher', 'approved', 'active', 'inactive');

ALTER TABLE api_key_audits DROP CONSTRAINT IF EXISTS api_key_audits_action_check;
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audits_action_check
  CHECK (action IN (
    'mentor_approved', 'mentor_rejected', 'teacher_approved', 'teacher_rejected',
    'mentor_revoked', 'mentor_deactivated'
  ));

ALTER TABLE api_key_audits DROP CONSTRAINT IF EXISTS api_key_audit_reason_required;
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audit_reason_required CHECK (
  action NOT IN ('mentor_rejected', 'teacher_rejected', 'mentor_revoked', 'mentor_deactivated')
  OR (comment IS NOT NULL AND btrim(comment) <> '')
);
