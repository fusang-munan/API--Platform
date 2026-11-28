-- A credential may retain its billing channel while a replacement is prepared.
-- Only one active credential holds the registration for a provider and channel.
ALTER TABLE beichen_provision_channels ADD COLUMN registration_provider_id uuid;
ALTER TABLE beichen_provision_channels ADD CONSTRAINT beichen_one_channel_registration
  UNIQUE (registration_provider_id, channel);

-- Preserve the credentials matched by migration 0065. Labels and group hints
-- are not sufficient evidence for a later in-place restore on their own.
CREATE TABLE beichen_credential_retirements (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('Gemini')),
  reason text NOT NULL,
  retired_at timestamptz NOT NULL
);

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
)
INSERT INTO beichen_credential_retirements(credential_id,provider_id,channel,reason,retired_at)
SELECT t.id,t.provider_id,'Gemini','0065_beichen_gemini',COALESCE(c.retired_at,c.updated_at)
FROM targets t JOIN provider_credentials c ON c.id=t.id
WHERE c.status='inactive'
ON CONFLICT (credential_id) DO NOTHING;

WITH registrations AS (
  SELECT DISTINCT ON (c.provider_id, j.channel) c.id, c.provider_id
  FROM beichen_provision_channels j JOIN provider_credentials c ON c.id=j.credential_id
  WHERE c.status='active'
  ORDER BY c.provider_id, j.channel, c.created_at, c.id
)
UPDATE beichen_provision_channels j SET registration_provider_id=r.provider_id
FROM registrations r WHERE r.id=j.credential_id;

CREATE FUNCTION refresh_beichen_channel_registration() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  owner_id uuid;
  school_channel text;
BEGIN
  IF TG_TABLE_NAME='provider_credentials' THEN
    owner_id := NEW.provider_id;
    SELECT channel INTO school_channel FROM beichen_provision_channels WHERE credential_id=NEW.id;
  ELSIF TG_OP='INSERT' THEN
    SELECT provider_id INTO owner_id FROM provider_credentials WHERE id=NEW.credential_id;
    school_channel := NEW.channel;
  ELSE
    owner_id := OLD.registration_provider_id;
    school_channel := OLD.channel;
  END IF;

  IF owner_id IS NULL OR school_channel IS NULL THEN
    RETURN NULL;
  END IF;
  UPDATE beichen_provision_channels j SET registration_provider_id=NULL
  FROM provider_credentials c WHERE c.id=j.credential_id AND c.status<>'active'
    AND j.registration_provider_id=owner_id AND j.channel=school_channel;
  UPDATE beichen_provision_channels j SET registration_provider_id=owner_id
  WHERE j.credential_id=(
    SELECT c.id FROM provider_credentials c JOIN beichen_provision_channels candidate ON candidate.credential_id=c.id
    WHERE c.provider_id=owner_id AND c.status='active' AND candidate.channel=school_channel
    ORDER BY c.created_at, c.id LIMIT 1
  ) AND NOT EXISTS (
    SELECT 1 FROM beichen_provision_channels current
    WHERE current.registration_provider_id=owner_id AND current.channel=school_channel
  );
  RETURN NULL;
END $$;

CREATE TRIGGER beichen_channel_registration_status AFTER UPDATE OF status ON provider_credentials
FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION refresh_beichen_channel_registration();
CREATE TRIGGER beichen_channel_registration_membership AFTER INSERT OR DELETE ON beichen_provision_channels
FOR EACH ROW EXECUTE FUNCTION refresh_beichen_channel_registration();

CREATE INDEX beichen_panel_access_upstream_key ON beichen_panel_access(upstream_key_id)
  WHERE upstream_key_id IS NOT NULL;

-- Old manual writes could give an ID to multiple credentials. Withhold all
-- conflicting receipt bindings until ownership can be verified again. Call and
-- usage records keep the credential IDs and receipt revisions already pinned.
-- A mapping is conflicting only while it is a valid Beichen binding. A unique
-- mapping on a provider that was moved off Beichen remains untouched.
CREATE TABLE beichen_cleared_panel_keys (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  provider_id uuid NOT NULL,
  upstream_key_id bigint NOT NULL CHECK (upstream_key_id BETWEEN 1 AND 9007199254740991),
  cleared_at timestamptz NOT NULL
);

WITH competing AS (
  SELECT c.provider_id,a.upstream_key_id
  FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
  JOIN providers p ON p.id=c.provider_id
  WHERE a.upstream_key_id IS NOT NULL AND c.status='active' AND c.updated_at=a.updated_at
    AND p.status='active'
    AND lower(btrim(p.base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/',
                                    'https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
  GROUP BY c.provider_id,a.upstream_key_id HAVING count(*)>1
), saved AS (
  INSERT INTO beichen_cleared_panel_keys(credential_id,provider_id,upstream_key_id,cleared_at)
  SELECT a.credential_id,c.provider_id,a.upstream_key_id,statement_timestamp()
  FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
  JOIN providers p ON p.id=c.provider_id
  JOIN competing k ON k.provider_id=c.provider_id AND k.upstream_key_id=a.upstream_key_id
  WHERE c.status='active' AND c.updated_at=a.updated_at AND p.status='active'
    AND lower(btrim(p.base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/',
                                    'https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
  ON CONFLICT (credential_id) DO UPDATE
  SET provider_id=excluded.provider_id,upstream_key_id=excluded.upstream_key_id,cleared_at=excluded.cleared_at
  RETURNING credential_id
), cleared AS (
  UPDATE beichen_panel_access a SET upstream_key_id=NULL
  WHERE a.credential_id IN (SELECT credential_id FROM saved)
  RETURNING a.credential_id
)
UPDATE beichen_provision_channels SET checked_at=NULL, upstream_user_id=NULL
WHERE credential_id IN (SELECT credential_id FROM cleared);

CREATE FUNCTION beichen_upstream_key_available(key_id bigint, claimant uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
  owner_provider uuid;
  claimant_active boolean;
BEGIN
  -- Serialize claims within a provider and keep an active credential's claim
  -- reserved while its provider is temporarily disabled.
  PERFORM pg_advisory_xact_lock(hashtextextended('beichen/receipt/' || key_id::text, 0));
  SELECT provider_id,status='active' INTO owner_provider,claimant_active FROM provider_credentials WHERE id=claimant;
  IF NOT claimant_active THEN
    RETURN true;
  END IF;
  RETURN NOT EXISTS (
    SELECT 1 FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
    WHERE a.upstream_key_id=key_id AND c.provider_id=owner_provider AND a.credential_id<>claimant
      AND c.status='active' AND c.updated_at=a.updated_at
  );
END $$;

CREATE FUNCTION check_beichen_upstream_key_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT beichen_upstream_key_available(NEW.upstream_key_id, NEW.credential_id) THEN
    RAISE EXCEPTION '北辰官网 Key 已绑定其他校内渠道' USING ERRCODE='23505', CONSTRAINT='beichen_panel_access_upstream_key_owner';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER beichen_panel_access_key_claim BEFORE INSERT OR UPDATE OF upstream_key_id, updated_at ON beichen_panel_access
FOR EACH ROW WHEN (NEW.upstream_key_id IS NOT NULL) EXECUTE FUNCTION check_beichen_upstream_key_claim();
