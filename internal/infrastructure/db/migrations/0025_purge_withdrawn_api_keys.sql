-- Student-withdrawn pending applications are deleted, not kept as rejected rows.

CREATE TEMP TABLE withdrawn_api_keys AS
SELECT k.id
FROM api_keys k
WHERE k.status = 'rejected'
  AND EXISTS (
    SELECT 1 FROM api_key_audits a
    WHERE a.api_key_id = k.id AND a.action = 'student_withdrawn'
  )
  AND NOT EXISTS (
    SELECT 1 FROM api_key_audits a
    WHERE a.api_key_id = k.id
      AND a.action IN ('mentor_approved', 'mentor_rejected', 'teacher_approved', 'teacher_rejected')
  );

DELETE FROM api_key_models WHERE api_key_id IN (SELECT id FROM withdrawn_api_keys);
DELETE FROM api_key_audits WHERE api_key_id IN (SELECT id FROM withdrawn_api_keys);
DELETE FROM api_key_month_credit_buckets WHERE api_key_id IN (SELECT id FROM withdrawn_api_keys);
DELETE FROM api_keys WHERE id IN (SELECT id FROM withdrawn_api_keys);
