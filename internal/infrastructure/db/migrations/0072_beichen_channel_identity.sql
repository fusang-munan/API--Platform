-- A credential may retain its billing channel while a replacement is prepared.
-- Only one active credential holds the registration for a provider and channel.
-- This revision is intended for environments that have not yet applied 0072.
-- If an earlier 0072 was already recorded, repair it with a forward 0073.
ALTER TABLE beichen_provision_channels ADD COLUMN IF NOT EXISTS registration_provider_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
    WHERE conrelid='beichen_provision_channels'::regclass AND conname='beichen_one_channel_registration') THEN
    ALTER TABLE beichen_provision_channels ADD CONSTRAINT beichen_one_channel_registration
      UNIQUE (registration_provider_id, channel);
  END IF;
END $$;

-- Preserve the credentials matched by migration 0065. Labels and group hints
-- are not sufficient evidence for a later in-place restore on their own.
CREATE TABLE IF NOT EXISTS beichen_credential_retirements (
  credential_id uuid PRIMARY KEY REFERENCES provider_credentials(id) ON DELETE CASCADE,
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('Gemini')),
  reason text NOT NULL,
  evidence text NOT NULL,
  retired_at timestamptz NOT NULL,
  source_updated_at timestamptz NOT NULL,
  source_retired_at timestamptz
);
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
    WHERE conrelid='beichen_credential_retirements'::regclass AND conname='beichen_retirement_evidence_known') THEN
    ALTER TABLE beichen_credential_retirements DROP CONSTRAINT IF EXISTS beichen_credential_retirements_evidence_check;
    ALTER TABLE beichen_credential_retirements ADD CONSTRAINT beichen_retirement_evidence_known
      CHECK (evidence IN ('upstream_key_12098','0065_updated','0065_window'));
  END IF;
END $$;
UPDATE beichen_credential_retirements SET reason='pending_confirmation'
WHERE evidence='0065_window' AND reason='0065_beichen_gemini';

-- The upstream ID and the 0065 UPDATE marker survive the 0067 backfill.
WITH evidence AS (
  SELECT c.id, c.provider_id, c.updated_at, c.retired_at, marker.created_at AS marked_at,
    (EXISTS (SELECT 1 FROM beichen_panel_access a
              WHERE a.credential_id=c.id AND a.upstream_key_id=12098)
      OR EXISTS (SELECT 1 FROM beichen_website_keys k
                 WHERE k.credential_id=c.id AND k.provider_id=c.provider_id AND k.upstream_key_id=12098)) AS known_key
  FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
  LEFT JOIN maintenance_state marker ON marker.name='beichen_0065_retirement/'||c.id::text
    AND marker.created_at=COALESCE(c.retired_at,c.updated_at)
  WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
                                     'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
    AND c.status='inactive'
)
INSERT INTO beichen_credential_retirements
  (credential_id,provider_id,channel,reason,evidence,retired_at,source_updated_at,source_retired_at)
SELECT id,provider_id,'Gemini','0065_beichen_gemini',
  CASE WHEN known_key THEN 'upstream_key_12098' ELSE '0065_updated' END,
  COALESCE(retired_at,updated_at),updated_at,retired_at
FROM evidence WHERE known_key OR marked_at IS NOT NULL
ON CONFLICT (credential_id) DO UPDATE
SET evidence=EXCLUDED.evidence,reason=EXCLUDED.reason,retired_at=EXCLUDED.retired_at,
    source_updated_at=EXCLUDED.source_updated_at,source_retired_at=EXCLUDED.source_retired_at
WHERE beichen_credential_retirements.evidence='0065_window';

-- Older databases always have schema_migrations. A few direct-SQL fixtures do
-- not, so this branch is evaluated dynamically only when that ledger exists.
DO $$
BEGIN
  IF to_regclass('schema_migrations') IS NOT NULL THEN
    EXECUTE $weak$
      WITH applied AS (
        SELECT applied_at FROM schema_migrations
        WHERE version = '0065_retire_beichen_gemini_credential.sql'
      ), matched AS (
        SELECT c.id, c.provider_id, c.updated_at
        FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
        CROSS JOIN applied
        WHERE lower(btrim(p.base_url)) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
                                           'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
          AND (c.group_hint = '5' OR c.label = '哈吉米')
          AND (c.retired_at IS NULL OR c.retired_at = c.updated_at)
          AND c.created_at < applied.applied_at
          AND c.updated_at BETWEEN applied.applied_at - interval '10 minutes'
                               AND applied.applied_at + interval '10 minutes'
      )
      INSERT INTO beichen_credential_retirements
        (credential_id,provider_id,channel,reason,evidence,retired_at,source_updated_at,source_retired_at)
      SELECT m.id,m.provider_id,'Gemini','pending_confirmation','0065_window',
             COALESCE(c.retired_at,c.updated_at),c.updated_at,c.retired_at
      FROM matched m JOIN provider_credentials c ON c.id=m.id
      WHERE c.status='inactive'
      ON CONFLICT (credential_id) DO NOTHING
    $weak$;
  END IF;
END $$;

CREATE OR REPLACE FUNCTION keep_beichen_retirement_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE beichen_credential_retirements SET source_updated_at=NEW.updated_at
  WHERE credential_id=NEW.id;
  RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS beichen_retirement_revision ON provider_credentials;
CREATE TRIGGER beichen_retirement_revision AFTER UPDATE OF updated_at ON provider_credentials
FOR EACH ROW EXECUTE FUNCTION keep_beichen_retirement_revision();

WITH registrations AS (
  SELECT DISTINCT ON (c.provider_id, j.channel) c.id, c.provider_id
  FROM beichen_provision_channels j JOIN provider_credentials c ON c.id=j.credential_id
  WHERE c.status='active' AND NOT EXISTS (
    SELECT 1 FROM beichen_provision_channels current
    WHERE current.registration_provider_id=c.provider_id AND current.channel=j.channel
  )
  ORDER BY c.provider_id, j.channel, c.created_at, c.id
)
UPDATE beichen_provision_channels j SET registration_provider_id=r.provider_id
FROM registrations r WHERE r.id=j.credential_id;

CREATE OR REPLACE FUNCTION refresh_beichen_channel_registration() RETURNS trigger LANGUAGE plpgsql AS $$
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

DROP TRIGGER IF EXISTS beichen_channel_registration_status ON provider_credentials;
CREATE TRIGGER beichen_channel_registration_status AFTER UPDATE OF status ON provider_credentials
FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION refresh_beichen_channel_registration();
DROP TRIGGER IF EXISTS beichen_channel_registration_membership ON beichen_provision_channels;
CREATE TRIGGER beichen_channel_registration_membership AFTER INSERT OR DELETE ON beichen_provision_channels
FOR EACH ROW EXECUTE FUNCTION refresh_beichen_channel_registration();

CREATE INDEX IF NOT EXISTS beichen_panel_access_upstream_key ON beichen_panel_access(upstream_key_id)
  WHERE upstream_key_id IS NOT NULL;

-- Old manual writes could give an ID to multiple credentials. Withhold all
-- conflicting receipt bindings until ownership can be verified again. Call and
-- usage records keep the credential IDs and receipt revisions already pinned.
-- A mapping is conflicting only while it is a valid Beichen binding. A unique
-- mapping on a provider that was moved off Beichen remains untouched.
CREATE TABLE IF NOT EXISTS beichen_cleared_panel_keys (
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

CREATE OR REPLACE FUNCTION beichen_upstream_key_available(key_id bigint, claimant uuid) RETURNS boolean LANGUAGE plpgsql AS $$
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

CREATE OR REPLACE FUNCTION check_beichen_upstream_key_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT beichen_upstream_key_available(NEW.upstream_key_id, NEW.credential_id) THEN
    RAISE EXCEPTION '北辰官网 Key 已绑定其他校内渠道' USING ERRCODE='23505', CONSTRAINT='beichen_panel_access_upstream_key_owner';
  END IF;
  RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS beichen_panel_access_key_claim ON beichen_panel_access;
CREATE TRIGGER beichen_panel_access_key_claim BEFORE INSERT OR UPDATE OF upstream_key_id, updated_at ON beichen_panel_access
FOR EACH ROW WHEN (NEW.upstream_key_id IS NOT NULL) EXECUTE FUNCTION check_beichen_upstream_key_claim();
