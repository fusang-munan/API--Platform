-- Undo anthropic bindings that were auto-inferred from OpenAI chat lines (0022).
-- Anthropic must be created only after an explicit /v1/messages route probe.
UPDATE model_bindings mb_anthropic
SET status = 'inactive', updated_at = now()
FROM model_bindings mb_chat
WHERE mb_anthropic.model_id = mb_chat.model_id
  AND mb_anthropic.provider_id = mb_chat.provider_id
  AND mb_anthropic.adapter = 'anthropic'
  AND mb_anthropic.status = 'active'
  AND mb_chat.adapter IN ('openai_compatible', 'openai_responses')
  AND mb_chat.status = 'active';
