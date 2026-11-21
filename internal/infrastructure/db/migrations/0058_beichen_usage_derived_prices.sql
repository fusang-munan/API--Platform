ALTER TABLE beichen_group_price_observations
  ADD COLUMN source text NOT NULL DEFAULT 'website' CHECK (source IN ('website', 'derived_from_usage')),
  DROP CONSTRAINT beichen_group_price_observations_pkey,
  ADD PRIMARY KEY (provider_id, group_id, model, observed_at, source);

-- The six verified rows per model establish the base rates. The observed
-- multiplier is evidence only. A fresh Key observation supplies the applied rate.
INSERT INTO beichen_group_price_observations (provider_id, group_id, model, observed_at, source, group_rules, pricing)
SELECT p.id, 16, evidence.model, TIMESTAMPTZ '2026-11-21 20:14:00+08', 'derived_from_usage',
  jsonb_build_object('receipt_count', 6, 'observed_rate_multiplier', 0.60),
  jsonb_build_object('billing_mode', 'token', 'input_price', evidence.input, 'output_price', evidence.output)
FROM providers p
CROSS JOIN (VALUES ('claude-opus-5-5', 0.000004, 0.000020), ('claude-sonnet-5-5', 0.000002, 0.000010))
  evidence(model, input, output)
WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/','https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
ON CONFLICT DO NOTHING;

DO $$
DECLARE
  source_check record;
BEGIN
  FOR source_check IN
    SELECT conname FROM pg_constraint
    WHERE conrelid='beichen_price_versions'::regclass AND contype='c'
      AND pg_get_constraintdef(oid) LIKE '%source%'
  LOOP
    EXECUTE format('ALTER TABLE beichen_price_versions DROP CONSTRAINT %I', source_check.conname);
  END LOOP;
END
$$;

ALTER TABLE beichen_price_versions ADD COLUMN observed_at timestamptz,
  ADD CONSTRAINT beichen_price_versions_source_check CHECK (
    (track='upstream' AND basis IN ('effective','base') AND source IN ('billing','csv'))
    OR (track='upstream' AND basis='base' AND source IN ('panel','derived_from_usage'))
    OR (track='school' AND basis IN ('manual','follow') AND source='teacher')
  ),
  ADD CONSTRAINT beichen_price_versions_source_actor_check
    CHECK ((actor_id IS NULL)=(source IN ('billing','panel','derived_from_usage'))),
  ADD CONSTRAINT beichen_price_versions_derived_evidence_check
    CHECK (source<>'derived_from_usage' OR (group_id IS NOT NULL AND observed_at IS NOT NULL AND observed_at<=effective_at));

ALTER TABLE gateway_calls DROP CONSTRAINT gateway_calls_beichen_base_source_check,
  ADD CONSTRAINT gateway_calls_beichen_base_source_check CHECK (beichen_base_source IN (
    'gateway_usage_price','receipt_usage_price','receipt_actual_cost','teacher_manual_price','derived_from_usage'
  ));

-- A current conditional or fixed website quote takes precedence. Derived
-- versions must come from the exact same verified group and snapshot.
CREATE OR REPLACE FUNCTION beichen_price_at(p_credential uuid, p_model text, p_time timestamptz)
RETURNS TABLE (
  mode text, upstream_version bigint, multiplier_version bigint, school_version bigint,
  upstream_input numeric, upstream_output numeric, upstream_cache_read numeric, upstream_cache_write numeric,
  school_input numeric, school_output numeric, school_cache_read numeric, school_cache_write numeric
)
LANGUAGE sql STABLE AS $$
  SELECT COALESCE(s.basis, 'follow'), u.id,
    CASE WHEN u.basis = 'base' AND (m.stale_at IS NULL OR p_time < m.stale_at) THEN m.id END,
    s.id, r.input, r.output, r.cache_read, r.cache_write,
    CASE WHEN s.basis = 'manual' THEN s.input_rate ELSE r.input END,
    CASE WHEN s.basis = 'manual' THEN s.output_rate ELSE r.output END,
    CASE WHEN s.basis = 'manual' THEN s.cache_read_rate ELSE r.cache_read END,
    CASE WHEN s.basis = 'manual' THEN s.cache_write_rate ELSE r.cache_write END
  FROM (VALUES (1)) AS seed(n)
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_panel_snapshots
    WHERE credential_id = p_credential AND effective_at <= p_time
    ORDER BY effective_at DESC LIMIT 1
  ) panel ON true
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'upstream' AND effective_at <= p_time
      AND (
        (panel.effective_at IS NULL AND source IN ('billing','csv') AND NOT EXISTS (
          SELECT 1 FROM beichen_multiplier_versions known
          WHERE known.credential_id = p_credential AND known.effective_at <= p_time AND known.group_id IS NOT NULL
        ))
        OR (source IN ('panel','derived_from_usage') AND effective_at = panel.effective_at AND p_time < panel.expires_at
          AND (source = 'panel' OR (group_id = panel.group_id AND NULLIF(panel.model_pricing -> p_model,'null'::jsonb) IS NULL))
          AND NOT EXISTS (
            SELECT 1 FROM provider_credentials c WHERE c.id = p_credential AND c.updated_at <= p_time
              AND NOT COALESCE(panel.credential_ciphertext_sha256 = sha256(c.credential_ciphertext),
                               panel.credential_revision = c.updated_at)
          ))
      )
    ORDER BY effective_at DESC, (source='panel') DESC, id DESC LIMIT 1
  ) u ON true
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'school' AND effective_at <= p_time
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) s ON true
  LEFT JOIN LATERAL (
    SELECT id, multiplier, stale_at FROM beichen_multiplier_versions
    WHERE u.id IS NOT NULL AND credential_id = p_credential AND effective_at <= p_time
      AND (u.source IN ('billing','csv') OR id = panel.multiplier_version)
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) m ON true
  CROSS JOIN LATERAL (SELECT CASE
    WHEN m.stale_at <= p_time AND (u.basis = 'base' OR u.source = 'billing') THEN NULL
    WHEN u.basis = 'effective' THEN 1::numeric ELSE m.multiplier END AS factor) f
  CROSS JOIN LATERAL (SELECT
    u.input_rate * f.factor AS input,
    u.output_rate * f.factor AS output,
    u.cache_read_rate * f.factor AS cache_read,
    u.cache_write_rate * f.factor AS cache_write
  ) r;
$$;
