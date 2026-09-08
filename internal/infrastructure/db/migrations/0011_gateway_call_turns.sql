CREATE TABLE gateway_call_turns(
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  user_id uuid NOT NULL,
  api_key_id uuid NOT NULL,
  model_id uuid NOT NULL,
  task_key varchar(256),
  project_name varchar(128) NOT NULL,
  user_name varchar(64) NOT NULL,
  api_key_name varchar(128) NOT NULL,
  model_name varchar(256) NOT NULL,
  root_prompt text NOT NULL DEFAULT '',
  root_prompt_bytes bigint NOT NULL DEFAULT 0 CHECK(root_prompt_bytes >= 0),
  call_count int NOT NULL DEFAULT 1 CHECK(call_count >= 1),
  credit_milli bigint NOT NULL DEFAULT 0 CHECK(credit_milli >= 0),
  prompt_tokens bigint NOT NULL DEFAULT 0 CHECK(prompt_tokens >= 0),
  completion_tokens bigint NOT NULL DEFAULT 0 CHECK(completion_tokens >= 0),
  total_tokens bigint NOT NULL DEFAULT 0 CHECK(total_tokens >= 0),
  outcome varchar(32) NOT NULL DEFAULT 'pending' CHECK(outcome IN ('pending','succeeded','upstream_failed','quota_rejected','outcome_unknown')),
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX gateway_call_turns_project_started ON gateway_call_turns(project_id, started_at DESC);
CREATE INDEX gateway_call_turns_key_started ON gateway_call_turns(api_key_id, started_at DESC);
CREATE INDEX gateway_call_turns_cursor ON gateway_call_turns(started_at DESC, id DESC);

ALTER TABLE gateway_calls ADD COLUMN turn_id uuid REFERENCES gateway_call_turns(id) ON DELETE SET NULL;
ALTER TABLE gateway_calls ADD COLUMN call_index int NOT NULL DEFAULT 1 CHECK(call_index >= 1);
ALTER TABLE gateway_calls ADD COLUMN task_key varchar(256);
ALTER TABLE gateway_calls ADD COLUMN previous_response_id varchar(128);
ALTER TABLE gateway_calls ADD COLUMN upstream_response_id varchar(128);
ALTER TABLE gateway_calls ADD COLUMN prompt_tokens int CHECK(prompt_tokens IS NULL OR prompt_tokens >= 0);
ALTER TABLE gateway_calls ADD COLUMN completion_tokens int CHECK(completion_tokens IS NULL OR completion_tokens >= 0);
ALTER TABLE gateway_calls ADD COLUMN total_tokens int CHECK(total_tokens IS NULL OR total_tokens >= 0);

CREATE INDEX gateway_calls_turn_index ON gateway_calls(turn_id, call_index);
CREATE INDEX gateway_calls_upstream_response ON gateway_calls(upstream_response_id) WHERE upstream_response_id IS NOT NULL;
