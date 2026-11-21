ALTER TABLE beichen_multiplier_versions
  ADD COLUMN group_id bigint CHECK (group_id BETWEEN 1 AND 9007199254740991),
  ADD COLUMN group_rate numeric CHECK (group_rate >= 0),
  ADD COLUMN peak_enabled boolean,
  ADD COLUMN peak_rate numeric CHECK (peak_rate >= 0),
  ADD COLUMN peak_start text,
  ADD COLUMN peak_end text,
  ADD COLUMN observed_at timestamptz,
  ADD COLUMN stale_at timestamptz CHECK (stale_at >= effective_at);

-- A failed read closes the current multiplier's validity at the time of the
-- failure. Select the latest version first so we never fall back to an older
-- one in that interval. Calls sent before the failure keep their old prices.
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
        (panel.effective_at IS NULL AND source <> 'panel' AND NOT EXISTS (
          -- A credential with a verified group needs a corresponding group quote.
          SELECT 1 FROM beichen_multiplier_versions known
          WHERE known.credential_id = p_credential AND known.effective_at <= p_time AND known.group_id IS NOT NULL
        ))
        OR (source = 'panel' AND effective_at = panel.effective_at AND p_time < panel.expires_at
          AND NOT EXISTS (
            SELECT 1 FROM provider_credentials c WHERE c.id = p_credential AND c.updated_at <= p_time
              AND NOT COALESCE(panel.credential_ciphertext_sha256 = sha256(c.credential_ciphertext),
                               panel.credential_revision = c.updated_at)
          ))
      )
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) u ON true
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'school' AND effective_at <= p_time
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) s ON true
  LEFT JOIN LATERAL (
    SELECT id, multiplier, stale_at FROM beichen_multiplier_versions
    WHERE u.id IS NOT NULL AND credential_id = p_credential AND effective_at <= p_time
      AND (u.source <> 'panel' OR id = panel.multiplier_version)
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
