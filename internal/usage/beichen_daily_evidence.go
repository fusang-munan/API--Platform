package usage

// BeichenDailyEvidenceSQL selects one source per provider, credential and day.
// The caller supplies usage_scope(provider_id, first_day, end_day, today, as_of).
// Dates use Beijing time and end_day is exclusive.
const BeichenDailyEvidenceSQL = `
, manual_usage AS (
 SELECT u.provider_id,u.credential_id,u.day,u.model::text,u.requests,u.input_tokens,u.output_tokens,
   u.cache_read_tokens,u.cache_write_tokens,u.source,u.billed_upstream_micros * 0.000001::numeric AS actual_cost
 FROM beichen_daily_usage u JOIN usage_scope s ON s.provider_id=u.provider_id
 WHERE u.source IN ('csv','export') AND (s.first_day IS NULL OR u.day >= s.first_day) AND u.day < s.end_day
), manual_days AS (
 SELECT DISTINCT provider_id,credential_id,day FROM manual_usage
), scoped_days AS (
 SELECT s.provider_id,d.credential_id,d.website_key_id,d.day,d.unit,d.model_stats,d.fetched_at,
   m.requests,m.input_tokens,m.output_tokens,m.cache_read_tokens,m.cache_write_tokens,m.total_tokens,m.actual_cost,
   (o.usage_error IS NOT NULL OR CASE WHEN d.day=s.today
      THEN d.fetched_at < GREATEST(o.usage_fetched_at, s.as_of - interval '30 minutes')
      ELSE d.fetched_at < (d.day+1)::timestamp AT TIME ZONE 'Asia/Shanghai' END) AS stale
 FROM usage_scope s JOIN provider_credentials c ON c.provider_id=s.provider_id
 JOIN beichen_key_usage_days d ON d.credential_id=c.id AND d.key_digest=sha256(c.credential_ciphertext)
 JOIN beichen_key_observations o ON o.credential_id=d.credential_id AND o.key_digest=d.key_digest
   AND o.website_key_id=d.website_key_id
 CROSS JOIN LATERAL jsonb_to_record(d.daily_usage) AS m(requests bigint,input_tokens bigint,output_tokens bigint,
   cache_read_tokens bigint,cache_write_tokens bigint,total_tokens bigint,actual_cost numeric)
 WHERE (s.first_day IS NULL OR d.day >= s.first_day) AND d.day < s.end_day
), archive_scope AS (
 SELECT u.provider_id,u.credential_id,u.upstream_key_id,u.model,u.input_tokens,u.output_tokens,
   u.cache_read_tokens,u.cache_write_tokens,(u.occurred_at AT TIME ZONE 'Asia/Shanghai')::date AS day,
   NULLIF(upper(btrim(u.currency)),'') AS unit,
   CASE WHEN u.billing_type=0 AND u.subscription_id IS NULL THEN u.actual_cost END AS actual_cost,
   s.today, cp.last_success
 FROM usage_scope s JOIN beichen_upstream_usage_rows u ON u.provider_id=s.provider_id
 JOIN provider_credentials c ON c.id=u.credential_id AND c.provider_id=u.provider_id
 JOIN beichen_usage_checkpoints cp ON cp.credential_id=u.credential_id AND cp.upstream_key_id=u.upstream_key_id
   AND cp.upstream_user_id=u.upstream_user_id AND cp.caught_up AND cp.last_error IS NULL
 WHERE (s.first_day IS NULL OR u.occurred_at >= s.first_day::timestamp AT TIME ZONE 'Asia/Shanghai')
   AND u.occurred_at < s.end_day::timestamp AT TIME ZONE 'Asia/Shanghai'
), website_rows AS (
 SELECT u.*
 FROM archive_scope u
 LEFT JOIN scoped_days d ON d.provider_id=u.provider_id AND d.credential_id=u.credential_id
   AND d.day=u.day AND d.website_key_id=u.upstream_key_id
 WHERE (u.day < u.today AND u.last_success >= (u.day+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
    OR (u.day = u.today AND u.last_success >= d.fetched_at)
), website_days AS (
 SELECT provider_id,credential_id,day FROM website_rows GROUP BY provider_id,credential_id,day
 HAVING bool_and(unit IS NOT NULL AND actual_cost IS NOT NULL)
), key_usage_days AS (
 SELECT d.*, d.model_stats IS NOT NULL AND d.actual_cost IS NOT NULL AND (
   SELECT CASE WHEN count(m.actual_cost)=count(*) THEN COALESCE(sum(m.actual_cost),0) END
   FROM jsonb_to_recordset(COALESCE(d.model_stats,'[]')) AS m(actual_cost numeric)
 ) = d.actual_cost AS models_covered
 FROM scoped_days d
 WHERE NOT EXISTS (SELECT 1 FROM manual_days m WHERE (m.provider_id,m.credential_id,m.day)=(d.provider_id,d.credential_id,d.day))
   AND NOT EXISTS (SELECT 1 FROM website_days w WHERE (w.provider_id,w.credential_id,w.day)=(d.provider_id,d.credential_id,d.day))
), model_usage_rows AS (
 SELECT u.provider_id,u.credential_id,u.day,u.model,u.requests::numeric,u.input_tokens::numeric,u.output_tokens::numeric,
   u.cache_read_tokens::numeric,u.cache_write_tokens::numeric,NULL::numeric AS total_tokens,
   u.source,NULL::text AS unit,u.actual_cost,false AS stale
 FROM manual_usage u
 UNION ALL
 SELECT d.provider_id,d.credential_id,d.day,m.model,m.requests,m.input_tokens,m.output_tokens,m.cache_read_tokens,m.cache_write_tokens,
   m.total_tokens,'key_usage',d.unit,m.actual_cost,d.stale
 FROM key_usage_days d
 CROSS JOIN LATERAL jsonb_to_recordset(COALESCE(d.model_stats,'[]')) AS m(model text,requests bigint,input_tokens bigint,output_tokens bigint,
   cache_read_tokens bigint,cache_write_tokens bigint,total_tokens bigint,actual_cost numeric)
 UNION ALL
 SELECT w.provider_id,w.credential_id,w.day,w.model,1,w.input_tokens,w.output_tokens,w.cache_read_tokens,w.cache_write_tokens,
   NULL,'website_receipts',w.unit,w.actual_cost,false
 FROM website_rows w JOIN website_days d USING (provider_id,credential_id,day)
 WHERE NOT EXISTS (SELECT 1 FROM manual_days m WHERE (m.provider_id,m.credential_id,m.day)=(w.provider_id,w.credential_id,w.day))
)`
