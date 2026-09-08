#!/usr/bin/env bash
# End-to-end: provision test key, call gpt-5.6-luna, inspect gateway_calls.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
COOKIE="$(mktemp)"
trap 'rm -f "$COOKIE"' EXIT

login_session() {
  local email="$1" password="$2" identity="$3"
  rm -f "$COOKIE"
  curl -sS -c "$COOKIE" -b "$COOKIE" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$password\"}" >/dev/null
  curl -sS -c "$COOKIE" -b "$COOKIE" -X POST "$BASE/api/v1/auth/session" \
    -H 'Content-Type: application/json' \
    -d "{\"identity\":\"$identity\"}" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['access_token'])"
}

api() {
  local token="$1"; shift
  curl -sS -H "Authorization: Bearer $token" "$@"
}

echo "== 1. student: add luna to model library =="
STU_TOKEN="$(login_session student@megaflow student123456 student)"
api "$STU_TOKEN" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
  -d '{"model_id":"gpt-5.6-luna"}' | python3 -c "import sys,json; d=json.load(sys.stdin); print('added', d.get('data',{}).get('model_id','err'), d.get('error',{}).get('message',''))"

echo "== 2. student: submit key =="
SUBMIT="$(api "$STU_TOKEN" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
  \"name\": \"luna-probe-$(date +%s)\",
  \"organization_id\": \"$ORG_ID\",
  \"project_id\": \"$PROJECT_ID\",
  \"requested_monthly_credits\": \"100.000\",
  \"models\": [{\"model_id\": \"gpt-5.6-luna\"}]
}")"
KEY_ID="$(echo "$SUBMIT" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
echo "key_id=$KEY_ID"

echo "== 3. mentor: approve =="
MEN_TOKEN="$(login_session mentor@megaflow mentor123456 mentor)"
api "$MEN_TOKEN" -X POST "$BASE/api/v1/mentor/api-key-reviews/$KEY_ID/approve" \
  -H 'Content-Type: application/json' \
  -d '{"monthly_credits":"100.000"}' -o /dev/null -w "mentor_approve_http=%{http_code}\n"

echo "== 4. student: claim =="
STU_TOKEN="$(login_session student@megaflow student123456 student)"
CLAIM="$(api "$STU_TOKEN" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim")"
API_KEY="$(echo "$CLAIM" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
echo "key_prefix=$(echo "$CLAIM" | python3 -c "import sys,json; print(json.load(sys.stdin)['data'].get('key_prefix',''))")"

echo "== 5. single luna call =="
SINGLE="$(curl -sS -w "\nHTTP_STATUS:%{http_code}\n" -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Reply with exactly: luna-single-ok"}],"max_tokens":32}')"
echo "$SINGLE" | tail -5

echo "== 6. simulated multi-round (2 separate HTTP requests) =="
ROUND1="$(curl -sS -w "\nHTTP_STATUS:%{http_code}\n" -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Say round-one-ok only."}],"max_tokens":16}')"
echo "$ROUND1" | tail -3
ROUND2="$(curl -sS -w "\nHTTP_STATUS:%{http_code}\n" -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Say round-one-ok only."},{"role":"assistant","content":"round-one-ok"},{"role":"user","content":"Now say round-two-ok only."}],"max_tokens":16}')"
echo "$ROUND2" | tail -3

echo "== 7. DB: gateway_calls for luna =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.created_at, left(c.request_id,8) req, c.outcome, c.credit_milli,
       (SELECT COUNT(*) FROM gateway_call_attempts a WHERE a.call_id=c.id) attempts,
       left(COALESCE(mi.content, mi_preview.preview), 60) prompt_preview
FROM gateway_calls c
LEFT JOIN monitored_inputs mi ON mi.call_id=c.id AND mi.visible=true
LEFT JOIN LATERAL (SELECT left(content,60) preview FROM monitored_inputs WHERE call_id=c.id) mi_preview ON true
WHERE c.model_name='gpt-5.6-luna'
ORDER BY c.created_at;
"

echo "== 8. attempt details =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.created_at, a.provider_name, a.status, a.http_status, a.latency_ms, left(a.error_message,80) err
FROM gateway_calls c
JOIN gateway_call_attempts a ON a.call_id=c.id
WHERE c.model_name='gpt-5.6-luna'
ORDER BY c.created_at, a.created_at;
"
