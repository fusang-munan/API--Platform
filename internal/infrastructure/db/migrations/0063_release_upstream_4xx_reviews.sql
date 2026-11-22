-- Upstream 4xx responses reject the request before billable work. Older
-- gateway versions left them in pending_review, where they could exhaust a
-- student's review limit and block unrelated models. Release only calls with
-- no monetary hold and no settled credit.
UPDATE gateway_calls
SET beichen_state = 'released',
    billing_state = 'failed',
    beichen_review_reason = NULL,
    credit_milli = 0,
    outcome = 'upstream_failed',
    finalized_at = now(),
    lease_expires_at = NULL
WHERE beichen_state = 'pending_review'
  AND error_category = 'http'
  AND error_message LIKE 'HTTP 4%'
  AND credit_milli = 0
  AND beichen_credit_exact IS NULL;
