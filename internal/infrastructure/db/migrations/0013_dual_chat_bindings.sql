-- Backfill missing chat gateway pairs for non-image models only.
INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), mb.model_id, mb.provider_id, mb.credential_id, mb.upstream_model_name, 'openai_responses', 50, mb.status, now(), now()
FROM model_bindings mb
JOIN models m ON m.id = mb.model_id
WHERE mb.adapter = 'openai_compatible'
  AND mb.status = 'active'
  AND m.model_id !~* 'gpt-image'
  AND m.model_id !~* 'dall-e'
  AND m.model_id !~* 'dalle'
  AND NOT EXISTS (
    SELECT 1 FROM model_bindings existing
    WHERE existing.model_id = mb.model_id
      AND existing.provider_id = mb.provider_id
      AND existing.adapter = 'openai_responses'
      AND existing.status = 'active'
  );

INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), mb.model_id, mb.provider_id, mb.credential_id, mb.upstream_model_name, 'openai_compatible', 100, mb.status, now(), now()
FROM model_bindings mb
JOIN models m ON m.id = mb.model_id
WHERE mb.adapter = 'openai_responses'
  AND mb.status = 'active'
  AND m.model_id !~* 'gpt-image'
  AND m.model_id !~* 'dall-e'
  AND m.model_id !~* 'dalle'
  AND NOT EXISTS (
    SELECT 1 FROM model_bindings existing
    WHERE existing.model_id = mb.model_id
      AND existing.provider_id = mb.provider_id
      AND existing.adapter = 'openai_compatible'
      AND existing.status = 'active'
  );
