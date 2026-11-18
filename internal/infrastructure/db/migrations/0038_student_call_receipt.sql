CREATE INDEX gateway_calls_student_receipt_lookup
  ON gateway_calls (user_id, request_id);
