-- One live API key application per student per project.
CREATE UNIQUE INDEX api_keys_owner_project_live
  ON api_keys(owner_user_id, project_id)
  WHERE status IN ('pending_mentor', 'pending_teacher', 'approved', 'active');
