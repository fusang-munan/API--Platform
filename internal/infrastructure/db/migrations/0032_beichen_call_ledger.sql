ALTER TABLE gateway_calls
  ADD COLUMN beichen_state text CHECK (beichen_state IN ('reserved','pending_review','charged','released')),
  ADD COLUMN beichen_dispatch_at timestamptz,
  ADD COLUMN beichen_dispatch_kind text CHECK (beichen_dispatch_kind IN ('beichen','legacy')),
  ADD COLUMN beichen_dispatch_credential_id uuid,
  ADD COLUMN beichen_dispatch_model text,
  ADD COLUMN beichen_price_at timestamptz,
  ADD COLUMN beichen_credit_exact numeric CHECK (beichen_credit_exact >= 0),
  ADD COLUMN beichen_review_reason text,
  ADD COLUMN beichen_upstream_price_version bigint,
  ADD COLUMN beichen_multiplier_version bigint,
  ADD CHECK ((beichen_state IS NOT DISTINCT FROM 'charged') = (beichen_credit_exact IS NOT NULL)),
  ADD CHECK ((beichen_state IS NOT DISTINCT FROM 'pending_review') = (beichen_review_reason IS NOT NULL));

ALTER TABLE project_month_credit_buckets ADD COLUMN charged_milli_remainder numeric NOT NULL DEFAULT 0
  CHECK (charged_milli_remainder >= 0 AND charged_milli_remainder < 1);
ALTER TABLE project_member_month_credit_buckets ADD COLUMN charged_milli_remainder numeric NOT NULL DEFAULT 0
  CHECK (charged_milli_remainder >= 0 AND charged_milli_remainder < 1);
ALTER TABLE api_key_month_credit_buckets ADD COLUMN charged_milli_remainder numeric NOT NULL DEFAULT 0
  CHECK (charged_milli_remainder >= 0 AND charged_milli_remainder < 1);
ALTER TABLE monthly_usage_cube ADD COLUMN charged_milli_remainder numeric NOT NULL DEFAULT 0
  CHECK (charged_milli_remainder >= 0 AND charged_milli_remainder < 1);
ALTER TABLE gateway_call_turns ADD COLUMN charged_milli_remainder numeric NOT NULL DEFAULT 0
  CHECK (charged_milli_remainder >= 0 AND charged_milli_remainder < 1);

CREATE INDEX gateway_calls_beichen_pending_review ON gateway_calls (created_at, id)
  WHERE beichen_state = 'pending_review';

-- Rates in migration 0030 are millionths of an upstream unit per million
-- tokens. Multiplication by a decimal literal preserves the entire scale.
-- A zero cache rate does not prove zero cache tokens. Infer an omitted zero
-- only when the two other explicit OpenAI input categories exhaust the total.
CREATE FUNCTION beichen_token_credits(
  protocol text, input_tokens bigint, output_tokens bigint, cache_read bigint,
  cache_miss bigint, cache_write bigint,
  input_rate numeric, output_rate numeric, read_rate numeric, write_rate numeric
) RETURNS numeric LANGUAGE sql IMMUTABLE AS $$
  WITH normalized AS (
    SELECT
      CASE WHEN protocol IN ('openai_compatible','openai_responses','openai_realtime')
                AND input_tokens::numeric = cache_miss::numeric + cache_write
           THEN COALESCE(cache_read,0) ELSE cache_read END AS reads,
      CASE WHEN protocol IN ('openai_compatible','openai_responses','openai_realtime')
                AND input_tokens::numeric = cache_miss::numeric + cache_read
           THEN COALESCE(cache_write,0) ELSE cache_write END AS writes
  ), counts AS (
    SELECT CASE WHEN protocol = 'anthropic' THEN input_tokens
                ELSE COALESCE(cache_miss::numeric, input_tokens::numeric - reads - writes)
           END AS uncached, reads, writes
    FROM normalized
  )
  SELECT CASE
    WHEN input_tokens IS NULL OR output_tokens IS NULL OR uncached IS NULL OR reads IS NULL OR writes IS NULL
      OR input_tokens < 0 OR output_tokens < 0 OR uncached < 0
      OR cache_read < 0 OR cache_write < 0 OR cache_miss < 0
      OR (protocol <> 'anthropic' AND uncached + reads + writes <> input_tokens)
      OR input_rate IS NULL OR output_rate IS NULL
    THEN NULL
    ELSE beichen_weighted_tokens(uncached::bigint, output_tokens, reads, writes,
           input_rate, output_rate, read_rate, write_rate) * 0.0000000001::numeric
  END
  FROM counts;
$$;
