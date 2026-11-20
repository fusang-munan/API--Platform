ALTER TABLE beichen_panel_snapshots
  ADD COLUMN credential_ciphertext_sha256 bytea
  CHECK (octet_length(credential_ciphertext_sha256) = 32);

-- Only backfill observations already proven to belong to the current key.
UPDATE beichen_panel_snapshots s
SET credential_ciphertext_sha256 = sha256(c.credential_ciphertext)
FROM provider_credentials c
WHERE c.id = s.credential_id AND c.updated_at = s.credential_revision
  AND s.expires_at > statement_timestamp();

CREATE OR REPLACE FUNCTION beichen_price_at(p_credential uuid, p_model text, p_time timestamptz)
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
    SELECT * FROM beichen_panel_snapshots
    WHERE credential_id = p_credential AND effective_at <= p_time
    ORDER BY effective_at DESC LIMIT 1
  ) panel ON true
  LEFT JOIN LATERAL (
    SELECT * FROM beichen_price_versions
    WHERE credential_id = p_credential AND model = p_model AND track = 'upstream' AND effective_at <= p_time
      AND (
        (panel.effective_at IS NULL AND source <> 'panel')
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
    SELECT id, multiplier FROM beichen_multiplier_versions
    WHERE u.basis = 'base' AND credential_id = p_credential AND effective_at <= p_time
      AND (u.source <> 'panel' OR id = panel.multiplier_version)
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
