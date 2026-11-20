package usage

import (
	"context"
	"time"
)

// BeichenSentCallsSQL selects confirmed gateway sends in the timestamp
// range [$1, $2). Use the recorded dispatch for new calls and the final
// route for historical calls. The caller supplies the provider scope.
const BeichenSentCallsSQL = `
SELECT COALESCE(c.provider_id, pc.provider_id) AS provider_id,
  a.credential_id, a.model, c.sent_at,
  CASE WHEN c.beichen_state = 'charged'
    AND COALESCE(` + beichenBaseSourceSQL + `, '') <> 'receipt_actual_cost'
    AND (` + beichenBaseSourceSQL + ` IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM beichen_call_receipts r WHERE r.call_id=c.id AND r.applied AND r.base_credits IS NULL
    ))
  THEN COALESCE(c.beichen_base_credits, (c.pricing_detail->>'base_credits')::numeric,
    COALESCE(c.actual_credits, c.beichen_credit_exact) / COALESCE(c.beichen_school_multiplier, 1))
  END AS local_credits
FROM gateway_calls c
CROSS JOIN LATERAL (SELECT
  CASE WHEN c.beichen_dispatch_kind='beichen' THEN c.beichen_dispatch_credential_id ELSE c.provider_credential_id END AS credential_id,
  CASE WHEN c.beichen_dispatch_kind='beichen' THEN c.beichen_dispatch_model ELSE c.upstream_model_name END AS model
) a
LEFT JOIN provider_credentials pc ON pc.id=a.credential_id
WHERE c.sent_at >= $1::timestamptz AND c.sent_at < $2::timestamptz
  AND a.credential_id IS NOT NULL AND a.model <> ''
  AND c.beichen_state IS DISTINCT FROM 'released'`

const refreshBeichenBillingChecksSQL = `
WITH days AS (
  SELECT ($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AS first_day,
         ($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AS end_day
), providers_to_check AS (
  SELECT id FROM providers
  WHERE lower(btrim(base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/','https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
), calls AS (` + BeichenSentCallsSQL + `
), local AS (
  SELECT provider_id, credential_id, model, (sent_at AT TIME ZONE 'Asia/Shanghai')::date AS day,
    CASE WHEN count(local_credits) = count(*) THEN sum(local_credits) END AS credits
  FROM calls JOIN providers_to_check p ON p.id=calls.provider_id GROUP BY 1,2,3,4
), daily AS (
  SELECT u.provider_id, u.credential_id, u.model::text, u.day, u.billed_upstream_micros * 0.0001::numeric AS credits
  FROM beichen_daily_usage u JOIN providers_to_check p ON p.id=u.provider_id CROSS JOIN days
  WHERE u.day >= days.first_day AND u.day < days.end_day
), archived AS (
  SELECT DISTINCT u.provider_id, u.credential_id, u.model, (u.occurred_at AT TIME ZONE 'Asia/Shanghai')::date AS day
  FROM beichen_upstream_usage_rows u JOIN providers_to_check p ON p.id=u.provider_id
  WHERE u.occurred_at >= $1 AND u.occurred_at < $2
), dimensions AS (
  SELECT provider_id, credential_id, model, day FROM local
  UNION SELECT provider_id, credential_id, model, day FROM daily
  UNION SELECT provider_id, credential_id, model, day FROM archived
  UNION SELECT b.provider_id, b.credential_id, b.model, b.day
    FROM billing_checks b JOIN providers_to_check p ON p.id=b.provider_id CROSS JOIN days
    WHERE b.day >= days.first_day AND b.day < days.end_day
), compared AS (
  SELECT d.*,
    CASE WHEN l.credential_id IS NULL THEN 0 ELSE l.credits END AS local_credits,
    CASE WHEN u.credential_id IS NOT NULL THEN u.credits
         WHEN synced.credential_id IS NOT NULL AND a.credential_id IS NULL THEN 0 END AS upstream_credits
  FROM dimensions d
  LEFT JOIN local l USING (provider_id,credential_id,model,day)
  LEFT JOIN daily u USING (provider_id,credential_id,model,day)
  LEFT JOIN archived a USING (provider_id,credential_id,model,day)
  LEFT JOIN beichen_usage_days synced ON synced.provider_id=d.provider_id
    AND synced.credential_id=d.credential_id AND synced.day=d.day
)
INSERT INTO billing_checks AS b (provider_id,credential_id,model,day,local_credits,upstream_credits,diff_credits,status)
SELECT provider_id,credential_id,model,day,local_credits,upstream_credits,local_credits-upstream_credits,
  CASE WHEN local_credits IS NULL OR upstream_credits IS NULL THEN 'missing'
       WHEN abs(local_credits-upstream_credits) > GREATEST(abs(upstream_credits)*0.01::numeric,1) THEN 'anomaly'
       ELSE 'ok' END
FROM compared
ON CONFLICT (provider_id,credential_id,model,day) DO UPDATE
SET local_credits=EXCLUDED.local_credits,upstream_credits=EXCLUDED.upstream_credits,
  diff_credits=EXCLUDED.diff_credits,status=EXCLUDED.status
WHERE ROW(b.local_credits,b.upstream_credits,b.diff_credits,b.status) IS DISTINCT FROM
  ROW(EXCLUDED.local_credits,EXCLUDED.upstream_credits,EXCLUDED.diff_credits,EXCLUDED.status)`

// Refresh the same three completed Shanghai days as the upstream day sync.
// The hourly maintenance pass lets late bills and receipts correct a check.
func (s *Store) refreshBeichenBillingChecks(ctx context.Context, now time.Time) error {
	local := now.In(shanghai)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghai)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, refreshBeichenBillingChecksSQL, end.AddDate(0, 0, -3), end)
	return err
}
