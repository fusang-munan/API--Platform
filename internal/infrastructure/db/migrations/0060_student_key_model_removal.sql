ALTER TABLE api_key_audits DROP CONSTRAINT api_key_audits_action_check;
ALTER TABLE api_key_audits DROP CONSTRAINT api_key_audit_model_required;
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audits_action_check
  CHECK (action IN (
    'mentor_approved', 'mentor_rejected', 'teacher_approved', 'teacher_rejected',
    'mentor_revoked', 'mentor_deactivated', 'mentor_activated', 'student_withdrawn',
    'student_model_added', 'student_model_removed'
  ));
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audit_model_required
  CHECK (action NOT IN ('student_model_added', 'student_model_removed') OR (comment IS NOT NULL AND btrim(comment) <> ''));
