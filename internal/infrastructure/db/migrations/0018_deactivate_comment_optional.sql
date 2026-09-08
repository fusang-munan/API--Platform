-- Deactivation no longer requires an audit comment.

ALTER TABLE api_key_audits DROP CONSTRAINT IF EXISTS api_key_audit_reason_required;
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audit_reason_required CHECK (
  action NOT IN ('mentor_rejected', 'teacher_rejected', 'mentor_revoked')
  OR (comment IS NOT NULL AND btrim(comment) <> '')
);
