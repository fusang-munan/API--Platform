ALTER TABLE gateway_calls
  ADD COLUMN beichen_school_multiplier numeric CHECK (beichen_school_multiplier IN (1,1.10)),
  ADD COLUMN beichen_base_credits numeric CHECK (beichen_base_credits >= 0),
  ADD COLUMN beichen_base_source text CHECK (beichen_base_source IN ('gateway_usage_price','receipt_usage_price','receipt_actual_cost')),
  ADD CHECK ((beichen_base_credits IS NULL) = (beichen_base_source IS NULL)),
  ADD CHECK (beichen_base_credits IS NULL OR (beichen_credit_exact IS NOT NULL AND
    beichen_credit_exact = beichen_base_credits * COALESCE(beichen_school_multiplier,1)));

-- Keep the original invariant for historical rows. New bookings can use an
-- independently priced base, even when it differs from the provider's debit.
ALTER TABLE beichen_call_receipts
  DROP CONSTRAINT beichen_call_receipts_check,
  ADD COLUMN base_credits numeric CHECK (base_credits >= 0),
  ADD COLUMN school_multiplier numeric CHECK (school_multiplier IN (1,1.10)),
  ADD CHECK ((base_credits IS NULL) = (school_multiplier IS NULL)),
  ADD CHECK (CASE WHEN base_credits IS NULL THEN NOT applied OR credits_booked = actual_cost*100
                  ELSE credits_booked = base_credits*school_multiplier END);

ALTER TABLE quota_adjustment_audits ADD COLUMN project_id uuid REFERENCES projects(id);
-- A subsequent key approval must preserve an explicit personal quota, including zero.
ALTER TABLE project_members ADD COLUMN teacher_quota_assigned boolean NOT NULL DEFAULT false;
