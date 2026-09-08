-- Key FSM v2: mentor final approval; migrate pending_teacher back to pending_mentor.
UPDATE api_keys SET status='pending_mentor', updated_at=now() WHERE status='pending_teacher';
