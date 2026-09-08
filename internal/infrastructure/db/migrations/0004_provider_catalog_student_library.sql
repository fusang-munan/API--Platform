CREATE TABLE provider_model_catalog(
  id uuid PRIMARY KEY,
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  model_id citext NOT NULL,
  description varchar(4096),
  owned_by varchar(256),
  raw_entry jsonb NOT NULL DEFAULT '{}'::jsonb,
  status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','inactive')),
  last_seen_at timestamptz NOT NULL,
  synced_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(provider_id, model_id)
);
CREATE INDEX provider_model_catalog_provider_status_model_id ON provider_model_catalog(provider_id, status, model_id);

CREATE TABLE student_model_libraries(
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  model_id uuid NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(user_id, model_id)
);
CREATE INDEX student_model_libraries_user_id ON student_model_libraries(user_id);
