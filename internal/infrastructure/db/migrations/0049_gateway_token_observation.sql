ALTER TABLE gateway_calls
  DROP CONSTRAINT gateway_calls_usage_state_check,
  ADD CONSTRAINT gateway_calls_usage_state_check
    CHECK (usage_state IN ('reported', 'partial', 'missing', 'interrupted')),
  ADD COLUMN usage_reason text,
  ADD COLUMN tool_prompt_tokens bigint CHECK (tool_prompt_tokens >= 0);

COMMENT ON COLUMN gateway_calls.usage_reason IS 'Reason the gateway token observation is missing, partial or interrupted; independent of billing review';
COMMENT ON COLUMN gateway_calls.tool_prompt_tokens IS 'Gemini tool-use prompt tokens reported separately from promptTokenCount';
