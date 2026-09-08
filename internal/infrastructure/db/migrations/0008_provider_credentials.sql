CREATE TABLE provider_credentials (
  id uuid PRIMARY KEY,
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  label citext NOT NULL,
  group_hint varchar(128),
  credential_ciphertext bytea NOT NULL,
  status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','inactive')),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(provider_id, label)
);
CREATE INDEX provider_credentials_provider_status ON provider_credentials(provider_id, status);

INSERT INTO provider_credentials (id, provider_id, label, credential_ciphertext, status, created_at, updated_at)
SELECT gen_random_uuid(), id, 'default', credential_ciphertext, status, created_at, updated_at
FROM providers
WHERE credential_ciphertext IS NOT NULL AND octet_length(credential_ciphertext) > 0;

ALTER TABLE provider_model_catalog ADD COLUMN credential_id uuid REFERENCES provider_credentials(id) ON DELETE CASCADE;

UPDATE provider_model_catalog pmc
SET credential_id = pc.id
FROM provider_credentials pc
WHERE pc.provider_id = pmc.provider_id AND pc.label = 'default';

ALTER TABLE provider_model_catalog ALTER COLUMN credential_id SET NOT NULL;

ALTER TABLE provider_model_catalog DROP CONSTRAINT provider_model_catalog_provider_id_model_id_key;
CREATE UNIQUE INDEX provider_model_catalog_credential_model_id ON provider_model_catalog(credential_id, model_id);

ALTER TABLE model_bindings ADD COLUMN credential_id uuid REFERENCES provider_credentials(id) ON DELETE RESTRICT;

UPDATE model_bindings mb
SET credential_id = pc.id
FROM provider_credentials pc
WHERE pc.provider_id = mb.provider_id AND pc.label = 'default';

ALTER TABLE providers DROP COLUMN credential_ciphertext;
