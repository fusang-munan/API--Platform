#!/usr/bin/env bash
# Claude Code + gpt-5.6-luna: tool-use task (read a local file) and verify multi-call turn grouping.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
REPO="/data/home/lab_proj/MegaFlowAI/api-platform"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
MODEL="${MODEL:-gpt-5.6-luna}"
CLAUDE="${CLAUDE:-/data/home/lab_proj/.cursor-server/extensions/anthropic.claude-code-2.1.257/resources/native-binary/claude}"
SECRET_FILE="$REPO/.claude-tool-probe-secret.txt"
MAGIC="claude-tool-probe-$(date +%s)"
COOKIE="$(mktemp)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LOG="/tmp/claude-tool-probe-$(date +%s).log"
trap 'rm -f "$COOKIE" "$SECRET_FILE"' EXIT

echo "$MAGIC" >"$SECRET_FILE"

login_session() {
  local email="$1" password="$2" identity="$3"
  rm -f "$COOKIE"
  curl -sS -c "$COOKIE" -b "$COOKIE" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "{\"email\":\"$email\",\"password\":\"$password\"}" >/dev/null
  curl -sS -c "$COOKIE" -b "$COOKIE" -X POST "$BASE/api/v1/auth/session" \
    -H 'Content-Type: application/json' -d "{\"identity\":\"$identity\"}" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['access_token'])"
}

api() { curl -sS -H "Authorization: Bearer $1" "${@:2}"; }

echo "== 1. provision or reuse key ($MODEL) =="
STU="$(login_session student@megaflow student123456 student)"
api "$STU" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
  -d "{\"model_id\":\"$MODEL\"}" >/dev/null || true
SUBMIT="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
  \"name\": \"claude-tool-probe-$(date +%s)\",
  \"organization_id\": \"$ORG_ID\",
  \"project_id\": \"$PROJECT_ID\",
  \"requested_monthly_credits\": \"50.000\",
  \"models\": [{\"model_id\": \"$MODEL\"}]
}")"
if echo "$SUBMIT" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin).get('data') else 1)"; then
  KEY_ID="$(echo "$SUBMIT" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
  MEN="$(login_session mentor@megaflow mentor123456 mentor)"
  api "$MEN" -X POST "$BASE/api/v1/mentor/api-key-reviews/$KEY_ID/approve" \
    -H 'Content-Type: application/json' -d '{"monthly_credits":"50.000"}' >/dev/null
  STU="$(login_session student@megaflow student123456 student)"
  API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
else
  STU="$(login_session student@megaflow student123456 student)"
  KEY_ID="$(api "$STU" "$BASE/api/v1/student/api-keys" | python3 -c "
import sys,json
keys=[k for k in json.load(sys.stdin).get('data',[]) if k.get('status')=='active']
print(keys[0]['id'] if keys else '')
")"
  if [[ -z "$KEY_ID" ]]; then
    echo "no active API key and create failed: $SUBMIT" >&2
    exit 1
  fi
  API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/regenerate" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
fi
KEY_PREFIX="${API_KEY:0:16}"
echo "key_prefix=$KEY_PREFIX marker=$MARKER magic=$MAGIC"

PROMPT="[$MARKER] You MUST use a tool (Read or Bash) to read the file .claude-tool-probe-secret.txt in the project directory $REPO. Do not guess. After reading, reply with exactly one line: MAGIC=<file contents>."

echo ""
echo "== 2. Claude Code (-p, tool task) =="
export ANTHROPIC_BASE_URL="$BASE"
export ANTHROPIC_API_KEY="$API_KEY"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
set +e
(
  cd "$REPO"
  "$CLAUDE" -p "$PROMPT" \
    --model "$MODEL" \
    --max-turns 12 \
    --add-dir "$REPO" \
    --dangerously-skip-permissions
) 2>&1 | tee "$LOG" | tail -60
CLI_RC=$?
set -e
echo "claude exit=$CLI_RC log=$LOG"

echo ""
echo "== 3. DB: gateway_calls since marker =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.created_at, c.request_path, c.call_index, c.outcome,
       left(c.previous_response_id,24) prev, left(c.upstream_response_id,24) upstream,
       c.total_tokens, left(COALESCE(t.root_prompt, mi.content,''),70) prompt
FROM gateway_calls c
JOIN api_keys k ON k.id=c.api_key_id
LEFT JOIN gateway_call_turns t ON t.id=c.turn_id
LEFT JOIN monitored_inputs mi ON mi.call_id=c.id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND c.created_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY c.created_at, c.call_index;
"

echo ""
echo "== 4. DB: turn aggregation =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.id, t.call_count, count(c.id) actual_calls,
       t.total_tokens, t.credit_milli,
       array_agg(c.call_index ORDER BY c.created_at) indexes,
       left(t.root_prompt,80) root_prompt
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '5 seconds'
GROUP BY t.id, t.call_count, t.total_tokens, t.credit_milli, t.root_prompt
ORDER BY min(c.created_at);
"

echo ""
echo "== 5. grep MAGIC in claude output =="
rg -n "MAGIC=|$MAGIC" "$LOG" || true
