#!/usr/bin/env bash
# Simulate Codex-style multi-call chain: call-2 carries previous_response_id from call-1.
# Proves turn grouping (1 prompt turn, call_count=2) even without real Codex CLI.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
MODEL="${MODEL:-gpt-5.6-luna}"
COOKIE="$(mktemp)"
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

echo "== provision key =="
STU="$(login_session student@megaflow student123456 student)"
api "$STU" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
  -d "{\"model_id\":\"$MODEL\"}" >/dev/null || true
SUBMIT="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
  \"name\": \"chain-probe-$(date +%s)\",
  \"organization_id\": \"$ORG_ID\",
  \"project_id\": \"$PROJECT_ID\",
  \"requested_monthly_credits\": \"10.000\",
  \"models\": [{\"model_id\": \"$MODEL\"}]
}")"
KEY_ID="$(echo "$SUBMIT" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
MEN="$(login_session mentor@megaflow mentor123456 mentor)"
api "$MEN" -X POST "$BASE/api/v1/mentor/api-key-reviews/$KEY_ID/approve" \
  -H 'Content-Type: application/json' -d '{"monthly_credits":"10.000"}' >/dev/null
STU="$(login_session student@megaflow student123456 student)"
API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
CACHE_KEY="codex-thread-$(date +%s)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "== call 1 (turn root, like user prompt) =="
R1="$(curl -sS -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"prompt_cache_key\":\"$CACHE_KEY\",\"messages\":[{\"role\":\"user\",\"content\":\"Say chain-one-ok only.\"}],\"max_tokens\":16}")"
RESP1="$(echo "$R1" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('id',''))")"
echo "upstream_id=$RESP1"

echo "== call 2 (chained via previous_response_id, like Codex internal step) =="
R2="$(curl -sS -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"prompt_cache_key\":\"$CACHE_KEY\",\"previous_response_id\":\"$RESP1\",\"messages\":[{\"role\":\"user\",\"content\":\"Say chain-one-ok only.\"},{\"role\":\"assistant\",\"content\":\"chain-one-ok\"},{\"role\":\"user\",\"content\":\"Now say chain-two-ok only.\"}],\"max_tokens\":16}")"
echo "$R2" | python3 -c "import sys,json; d=json.load(sys.stdin); print('call2 content:', d.get('choices',[{}])[0].get('message',{}).get('content',''))"

echo ""
echo "== DB: same turn? =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.id turn_id, t.call_count, t.total_tokens, left(t.root_prompt,40) root_prompt,
       array_agg(c.call_index ORDER BY c.call_index) indexes,
       array_agg(c.total_tokens ORDER BY c.call_index) tokens
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.name LIKE 'chain-probe%'
  AND t.started_at >= timestamptz '$MARKER' - interval '5 seconds'
GROUP BY t.id, t.call_count, t.total_tokens, t.root_prompt;
"
