-- Allow students to withdraw pending API key applications.

ALTER TABLE api_key_audits DROP CONSTRAINT IF EXISTS api_key_audits_action_check;
ALTER TABLE api_key_audits ADD CONSTRAINT api_key_audits_action_check
  CHECK (action IN (
    'mentor_approved', 'mentor_rejected', 'teacher_approved', 'teacher_rejected',
    'mentor_revoked', 'mentor_deactivated', 'mentor_activated', 'student_withdrawn'
  ));
