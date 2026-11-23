-- Serialize with Beichen provider edits and lock the credentials used by the
-- survivor check, so another status update cannot invalidate it mid-migration.
DO $$
BEGIN
  PERFORM p.id FROM providers p
  WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
                                     'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
  ORDER BY p.id FOR NO KEY UPDATE;

  PERFORM c.id FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
  WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
                                     'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
  ORDER BY c.id FOR NO KEY UPDATE OF c;
END $$;

WITH matched AS (
  SELECT c.id, c.provider_id
  FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
  WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
                                     'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
    AND (
      c.group_hint = '5' OR c.label = '哈吉米'
      OR EXISTS (SELECT 1 FROM beichen_panel_access a
                 WHERE a.credential_id = c.id AND a.upstream_key_id = 12098)
      OR EXISTS (SELECT 1 FROM beichen_website_keys k
                 WHERE k.credential_id = c.id AND k.provider_id = c.provider_id AND k.upstream_key_id = 12098)
    )
), targets AS (
  SELECT m.id, m.provider_id FROM matched m
  WHERE EXISTS (
    SELECT 1 FROM provider_credentials c
    WHERE c.provider_id = m.provider_id AND c.status = 'active'
      AND NOT EXISTS (SELECT 1 FROM matched other WHERE other.id = c.id)
  )
), retired_credentials AS (
  UPDATE provider_credentials c SET status = 'inactive', updated_at = statement_timestamp()
  FROM targets t WHERE c.id = t.id AND c.status = 'active'
), retired_catalog AS (
  UPDATE provider_model_catalog m SET status = 'inactive', updated_at = statement_timestamp()
  FROM targets t WHERE m.credential_id = t.id AND m.provider_id = t.provider_id AND m.status = 'active'
)
UPDATE model_bindings b SET status = 'inactive', updated_at = statement_timestamp()
FROM targets t WHERE b.credential_id = t.id AND b.provider_id = t.provider_id AND b.status = 'active';
