CREATE TABLE beichen_website_directories (
  provider_id uuid PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  platforms jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(platforms) = 'array'),
  last_success timestamptz,
  last_error text,
  failure_count int NOT NULL DEFAULT 0 CHECK (failure_count BETWEEN 0 AND 6),
  next_attempt timestamptz NOT NULL DEFAULT '-infinity',
  lease_id uuid,
  lease_until timestamptz,
  CHECK ((lease_id IS NULL) = (lease_until IS NULL))
);
