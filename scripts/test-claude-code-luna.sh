#!/usr/bin/env bash
# Test Claude Code + gpt-5.6-luna via gateway (/v1/messages): prompt capture + multi-call turn grouping.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
REPO="/data/home/lab_proj/MegaFlowAI/api-platform"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
MODEL="${MODEL:-gpt-5.6-luna}"
CLAUDE="${CLAUDE:-/data/home/lab_proj/.cursor-server/extensions/anthropic.claude-code-2.1.257/resources/native-binary/claude}"
COOKIE="$(mktemp)"
MARKER="CLAUDE-LUNA-$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LOG="/tmp/claude-luna-$(date +%s).log"
trap 'rm -f "$COOKIE"' EXIT

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

echo "== 0. Claude Code requires active anthropic binding =="
TEACH="$(login_session teacher@megaflow teacher123456 teacher)"
PROVIDER="$(docker exec nebula-api-postgres-1 psql -U nebula -d nebula -tAc \
  "SELECT provider_id FROM model_bindings mb JOIN models m ON m.id=mb.model_id \
   WHERE m.model_id='$MODEL' AND mb.adapter='anthropic' AND mb.status='active' LIMIT 1")"
echo "provider=$PROVIDER"
if [ -z "$PROVIDER" ]; then
  echo "ERROR: model $MODEL has no active anthropic binding; configure one in teacher console first."
  exit 1
fi

echo "== 1. reclaim API key for test =="
KEY_ID="$(docker exec nebula-api-postgres-1 psql -U nebula -d nebula -tAc \
  "SELECT k.id FROM api_keys k JOIN users u ON u.id=k.owner_user_id \
   WHERE u.email='student@megaflow' AND k.status='active' ORDER BY k.created_at DESC LIMIT 1")"
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c \
  "UPDATE api_keys SET status='approved', key_hash=NULL, key_prefix=NULL, claimed_at=NULL WHERE id='$KEY_ID';" >/dev/null
STU="$(login_session student@megaflow student123456 student)"
API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
KEY_PREFIX="${API_KEY:0:16}"
echo "key_prefix=$KEY_PREFIX marker=$MARKER"

echo "== 2. curl /v1/messages prompt capture =="
PROMPT="[$MARKER] 用一句话介绍隐式规划（implicit planning）论文方向。"
HTTP_CODE="$(curl -sS -o /tmp/claude-luna-curl.json -w '%{http_code}' \
  -X POST "$BASE/v1/messages" \
  -H "x-api-key: $API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}")"
echo "curl_http=$HTTP_CODE"
python3 -m json.tool /tmp/claude-luna-curl.json 2>/dev/null | head -20 || cat /tmp/claude-luna-curl.json | head -c 500

sleep 1
echo "-- DB after curl --"
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.started_at, t.call_count, left(t.root_prompt,80) root_prompt, c.outcome, c.request_path
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
ORDER BY t.started_at DESC LIMIT 3;
"

echo "== 3. real Claude Code CLI (-p) =="
export ANTHROPIC_BASE_URL="$BASE"
export ANTHROPIC_API_KEY="$API_KEY"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
set +e
"$CLAUDE" -p "[$MARKER-CLI] 只回复 OK-CLAUDE-LUNA，不要调用工具。" --model "$MODEL" --max-turns 3 2>&1 | tee "$LOG"
CLI_RC=$?
set -e
echo "claude_exit=$CLI_RC log=$LOG"

sleep 2
echo "== 4. final DB summary =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.id turn_id, t.started_at, t.call_count, count(c.id) actual_calls,
       left(t.root_prompt,100) root_prompt,
       string_agg(DISTINCT c.request_path, ', ') paths,
       string_agg(DISTINCT c.outcome::text, ', ') outcomes
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND (t.root_prompt LIKE '%$MARKER%' OR EXISTS (
    SELECT 1 FROM monitored_inputs mi WHERE mi.turn_id=t.id AND mi.content LIKE '%$MARKER%'
  ))
GROUP BY t.id,t.started_at,t.call_count,t.root_prompt
ORDER BY t.started_at;
" -c "
SELECT c.call_index, c.outcome, left(COALESCE(mi.content,''),80) monitored
FROM gateway_calls c
JOIN gateway_call_turns t ON t.id=c.turn_id
JOIN api_keys k ON k.id=c.api_key_id
LEFT JOIN monitored_inputs mi ON mi.call_id=c.id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at > now() - interval '10 minutes'
ORDER BY c.created_at;
"
