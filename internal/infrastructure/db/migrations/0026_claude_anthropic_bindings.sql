-- Restore anthropic bindings for Claude-family models (Claude Code harness).
-- 0023 deactivated inferred anthropic routes for all chat models; Claude models still need /v1/messages.

WITH claude_models AS (
  SELECT m.id, m.model_id
  FROM models m
  WHERE m.model_id !~* '^gpt'
    AND m.model_id ~* '(claude|opus|sonnet|haiku)'
),
source_bindings AS (
  SELECT DISTINCT ON (mb.model_id, mb.provider_id)
    mb.model_id,
    mb.provider_id,
    mb.credential_id,
    mb.upstream_model_name,
    mb.status
  FROM model_bindings mb
  JOIN claude_models cm ON cm.id = mb.model_id
  WHERE mb.adapter IN ('openai_compatible', 'openai_responses')
    AND mb.status = 'active'
  ORDER BY mb.model_id, mb.provider_id, mb.priority ASC, mb.adapter ASC
)
INSERT INTO model_bindings(id, model_id, provider_id, credential_id, upstream_model_name, adapter, priority, status, created_at, updated_at)
SELECT gen_random_uuid(), model_id, provider_id, credential_id, upstream_model_name, 'anthropic', 50, status, now(), now()
FROM source_bindings
ON CONFLICT (model_id, provider_id, adapter) DO UPDATE
SET upstream_model_name = EXCLUDED.upstream_model_name,
    credential_id = EXCLUDED.credential_id,
    priority = EXCLUDED.priority,
    status = 'active',
    updated_at = now();

UPDATE model_bindings mb
SET status = 'active', updated_at = now()
FROM models m
WHERE mb.model_id = m.id
  AND mb.adapter = 'anthropic'
  AND mb.status = 'inactive'
  AND m.model_id !~* '^gpt'
  AND m.model_id ~* '(claude|opus|sonnet|haiku)';
