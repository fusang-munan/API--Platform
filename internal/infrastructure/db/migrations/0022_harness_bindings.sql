-- Auto-pair Codex + Claude Code harness bindings for non-image text models.
-- Mirrors 0013/0014 chat pairing: anthropic + openai_responses + openai_compatible share provider/credential/upstream.

WITH source_bindings AS (
  SELECT DISTINCT ON (mb.model_id, mb.provider_id)
    mb.model_id,
    mb.provider_id,
    mb.credential_id,
    mb.upstream_model_name,
    mb.status
  FROM model_bindings mb
  JOIN models m ON m.id = mb.model_id
  WHERE mb.adapter IN ('openai_compatible', 'openai_responses')
    AND mb.status = 'active'
    AND m.model_id !~* 'gpt-image'
    AND m.model_id !~* 'dall-e'
    AND m.model_id !~* 'dalle'
  ORDER BY mb.model_id, mb.provider_id, mb.priority ASC, mb.adapter ASC
)
INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), model_id, provider_id, credential_id, upstream_model_name, 'anthropic', 50, status, now(), now()
FROM source_bindings
ON CONFLICT (model_id, provider_id, adapter) DO UPDATE
SET upstream_model_name = EXCLUDED.upstream_model_name,
    credential_id = EXCLUDED.credential_id,
    priority = EXCLUDED.priority,
    status = EXCLUDED.status,
    updated_at = now();

WITH source_bindings AS (
  SELECT DISTINCT ON (mb.model_id, mb.provider_id)
    mb.model_id,
    mb.provider_id,
    mb.credential_id,
    mb.upstream_model_name,
    mb.status
  FROM model_bindings mb
  JOIN models m ON m.id = mb.model_id
  WHERE mb.adapter = 'anthropic'
    AND mb.status = 'active'
    AND m.model_id !~* 'gpt-image'
    AND m.model_id !~* 'dall-e'
    AND m.model_id !~* 'dalle'
  ORDER BY mb.model_id, mb.provider_id, mb.priority ASC
)
INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), model_id, provider_id, credential_id, upstream_model_name, 'openai_responses', 50, status, now(), now()
FROM source_bindings
ON CONFLICT (model_id, provider_id, adapter) DO UPDATE
SET upstream_model_name = EXCLUDED.upstream_model_name,
    credential_id = EXCLUDED.credential_id,
    priority = EXCLUDED.priority,
    status = EXCLUDED.status,
    updated_at = now();

WITH source_bindings AS (
  SELECT DISTINCT ON (mb.model_id, mb.provider_id)
    mb.model_id,
    mb.provider_id,
    mb.credential_id,
    mb.upstream_model_name,
    mb.status
  FROM model_bindings mb
  JOIN models m ON m.id = mb.model_id
  WHERE mb.adapter IN ('anthropic', 'openai_responses')
    AND mb.status = 'active'
    AND m.model_id !~* 'gpt-image'
    AND m.model_id !~* 'dall-e'
    AND m.model_id !~* 'dalle'
  ORDER BY mb.model_id, mb.provider_id, mb.priority ASC, mb.adapter ASC
)
INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), model_id, provider_id, credential_id, upstream_model_name, 'openai_compatible', 100, status, now(), now()
FROM source_bindings
ON CONFLICT (model_id, provider_id, adapter) DO UPDATE
SET upstream_model_name = EXCLUDED.upstream_model_name,
    credential_id = EXCLUDED.credential_id,
    priority = EXCLUDED.priority,
    status = EXCLUDED.status,
    updated_at = now();
