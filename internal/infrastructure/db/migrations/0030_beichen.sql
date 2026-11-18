CREATE TABLE beichen_datasets (
  credential_id uuid NOT NULL REFERENCES provider_credentials(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('catalog','billing','prices','usage')),
  last_success timestamptz,
  last_error text,
  failure_count int NOT NULL DEFAULT 0 CHECK (failure_count BETWEEN 0 AND 6),
  next_attempt timestamptz NOT NULL DEFAULT '-infinity',
  lease_id uuid,
  lease_until timestamptz,
  PRIMARY KEY (credential_id, kind),
  CHECK ((lease_id IS NULL) = (lease_until IS NULL))
);

-- Historical rows keep the original UUID after a provider credential is deleted.
CREATE TABLE beichen_price_versions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  credential_id uuid NOT NULL,
  model varchar(256) NOT NULL CHECK (model <> ''),
  track text NOT NULL CHECK (track IN ('upstream','school')),
  basis text NOT NULL,
  source text NOT NULL,
  actor_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  effective_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  input_rate bigint CHECK (input_rate BETWEEN 0 AND 1000000000000),
  output_rate bigint CHECK (output_rate BETWEEN 0 AND 1000000000000),
  cache_read_rate bigint CHECK (cache_read_rate BETWEEN 0 AND 1000000000000),
  cache_write_rate bigint CHECK (cache_write_rate BETWEEN 0 AND 1000000000000),
  CHECK (
    (track = 'upstream' AND basis IN ('effective','base') AND source IN ('billing','csv'))
    OR (track = 'school' AND basis IN ('manual','follow') AND source = 'teacher')
  ),
  CHECK ((actor_id IS NULL) = (source = 'billing')),
  CHECK (
    (basis = 'follow' AND input_rate IS NULL AND output_rate IS NULL AND cache_read_rate IS NULL AND cache_write_rate IS NULL)
    OR (basis <> 'follow' AND input_rate IS NOT NULL AND output_rate IS NOT NULL)
  )
);

CREATE INDEX beichen_price_versions_lookup ON beichen_price_versions
  (credential_id, model, track, effective_at DESC, id DESC);

CREATE TABLE beichen_multiplier_versions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  credential_id uuid NOT NULL,
  effective_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  multiplier numeric NOT NULL CHECK (multiplier BETWEEN 0 AND 1000),
  timezone text CHECK (char_length(timezone) <= 64)
);

CREATE INDEX beichen_multiplier_versions_lookup ON beichen_multiplier_versions
  (credential_id, effective_at DESC, id DESC);

CREATE TABLE beichen_daily_usage (
  provider_id uuid NOT NULL,
  credential_id uuid NOT NULL,
  day date NOT NULL,
  model varchar(256) NOT NULL CHECK (model <> ''),
  requests bigint CHECK (requests >= 0),
  input_tokens bigint CHECK (input_tokens >= 0),
  output_tokens bigint CHECK (output_tokens >= 0),
  cache_read_tokens bigint CHECK (cache_read_tokens >= 0),
  cache_write_tokens bigint CHECK (cache_write_tokens >= 0),
  billed_upstream_micros bigint CHECK (billed_upstream_micros >= 0),
  source text NOT NULL CHECK (source IN ('usage','csv')),
  actor_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (credential_id, day, model),
  CHECK ((actor_id IS NULL) = (source = 'usage')),
  CHECK (num_nonnulls(requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, billed_upstream_micros) > 0)
);

CREATE INDEX beichen_daily_usage_provider_day ON beichen_daily_usage(provider_id, day);

CREATE TABLE beichen_usage_days (
  provider_id uuid NOT NULL,
  credential_id uuid NOT NULL,
  day date NOT NULL,
  empty boolean NOT NULL,
  synced_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (provider_id, day, credential_id)
);

CREATE INDEX gateway_calls_beichen_compare ON gateway_calls (provider_id, sent_at)
  WHERE provider_credential_id IS NOT NULL AND upstream_model_name IS NOT NULL AND sent_at IS NOT NULL;

-- The same lookup reconstructs the exact versions for a call using sent_at.
CREATE FUNCTION beichen_price_at(p_credential uuid, p_model text, p_time timestamptz)
RETURNS TABLE (
  mode text, upstream_version bigint, multiplier_version bigint, school_version bigint,
  upstream_input numeric, upstream_output numeric, upstream_cache_read numeric, upstream_cache_write numeric,
  school_input numeric, school_output numeric, school_cache_read numeric, school_cache_write numeric
)
LANGUAGE sql STABLE AS $$
  SELECT COALESCE(s.basis, 'follow'), u.id, m.id, s.id, r.input, r.output, r.cache_read, r.cache_write,
    CASE WHEN s.basis = 'manual' THEN s.input_rate ELSE r.input END,
    CASE WHEN s.basis = 'manual' THEN s.output_rate ELSE r.output END,
    CASE WHEN s.basis = 'manual' THEN s.cache_read_rate ELSE r.cache_read END,
    CASE WHEN s.basis = 'manual' THEN s.cache_write_rate ELSE r.cache_write END
  FROM (VALUES (1)) AS seed(n)
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'upstream' AND effective_at <= p_time
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) u ON true
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'school' AND effective_at <= p_time
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) s ON true
  LEFT JOIN LATERAL (
    SELECT id, multiplier FROM beichen_multiplier_versions
    WHERE u.basis = 'base' AND credential_id = p_credential AND effective_at <= p_time
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) m ON true
  CROSS JOIN LATERAL (SELECT CASE WHEN u.basis = 'effective' THEN 1::numeric ELSE m.multiplier END AS factor) f
  CROSS JOIN LATERAL (SELECT
    u.input_rate * f.factor AS input,
    u.output_rate * f.factor AS output,
    u.cache_read_rate * f.factor AS cache_read,
    u.cache_write_rate * f.factor AS cache_write
  ) r;
$$;

-- Keep exact arithmetic until the day totals are converted to the display unit.
CREATE FUNCTION beichen_weighted_tokens(i bigint, o bigint, r bigint, w bigint,
  i_rate numeric, o_rate numeric, r_rate numeric, w_rate numeric)
RETURNS numeric
LANGUAGE sql IMMUTABLE AS $$
  SELECT i::numeric * i_rate + o::numeric * o_rate
    + CASE WHEN r = 0 OR r_rate = 0 THEN 0 ELSE r::numeric * r_rate END
    + CASE WHEN w = 0 OR w_rate = 0 THEN 0 ELSE w::numeric * w_rate END;
$$;
