ALTER TABLE gateway_calls
  ADD COLUMN unresolved_credit_milli bigint NOT NULL DEFAULT 0 CHECK (unresolved_credit_milli >= 0);

-- The exposure budget uses whole student credits. It is independent of the billing hold.
UPDATE gateway_calls SET unresolved_credit_milli = 10 WHERE beichen_state = 'pending_review';

CREATE INDEX gateway_calls_user_beichen_state ON gateway_calls (user_id, beichen_state)
  WHERE beichen_state = 'pending_review';
