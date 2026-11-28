-- A credential may retain its billing channel while a replacement is prepared.
-- Only one active credential holds the registration for a provider and channel.
ALTER TABLE beichen_provision_channels ADD COLUMN registration_provider_id uuid;
ALTER TABLE beichen_provision_channels ADD CONSTRAINT beichen_one_channel_registration
  UNIQUE (registration_provider_id, channel);

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
WITH competing AS (
  SELECT a.upstream_key_id
  FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
  WHERE a.upstream_key_id IS NOT NULL AND c.status='active' AND c.updated_at=a.updated_at
  GROUP BY a.upstream_key_id HAVING count(*)>1
), cleared AS (
  UPDATE beichen_panel_access a SET upstream_key_id=NULL
  FROM competing k, provider_credentials c
  WHERE k.upstream_key_id=a.upstream_key_id AND c.id=a.credential_id
    AND c.status='active' AND c.updated_at=a.updated_at
  RETURNING a.credential_id
)
UPDATE beichen_provision_channels SET checked_at=NULL, upstream_user_id=NULL
WHERE credential_id IN (SELECT credential_id FROM cleared);

CREATE FUNCTION beichen_upstream_key_available(key_id bigint, claimant uuid) RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
  -- Serialize claims even across providers and keep an active credential's
  -- claim reserved while its provider is temporarily disabled.
  PERFORM pg_advisory_xact_lock(hashtextextended('beichen/receipt/' || key_id::text, 0));
  RETURN NOT EXISTS (
    SELECT 1 FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
    WHERE a.upstream_key_id=key_id AND a.credential_id<>claimant
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
