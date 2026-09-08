#!/usr/bin/env bash
# Real Codex CLI via gateway (/v1/responses): web-research task, verify multi-call turn grouping.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
REPO="/data/home/lab_proj/MegaFlowAI/api-platform"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
MODEL="${MODEL:-gpt-5.3-codex}"
COOKIE="$(mktemp)"
CODEX_DIR="$(mktemp -d)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LOG="/tmp/codex-web-research-$(date +%s).log"
trap 'rm -f "$COOKIE"; rm -rf "$CODEX_DIR"' EXIT

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

echo "== 1. rotate to one codex key =="
STU="$(login_session student@megaflow student123456 student)"
ACTIVE_ID="$(api "$STU" "$BASE/api/v1/student/api-keys" | python3 -c "
import sys,json
keys=[k for k in json.load(sys.stdin)['data'] if k['status'] in ('active','approved')]
print(keys[0]['id'] if keys else '')
")"
if [[ -n "$ACTIVE_ID" ]]; then
  MEN="$(login_session mentor@megaflow mentor123456 mentor)"
  api "$MEN" -X POST "$BASE/api/v1/mentor/api-keys/$ACTIVE_ID/revoke" \
    -H 'Content-Type: application/json' -d '{"comment":"codex web research probe"}' >/dev/null
  echo "revoked $ACTIVE_ID"
fi

STU="$(login_session student@megaflow student123456 student)"
api "$STU" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
  -d "{\"model_id\":\"$MODEL\"}" >/dev/null || true
SUBMIT="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
  \"name\": \"codex-web-$(date +%s)\",
  \"organization_id\": \"$ORG_ID\",
  \"project_id\": \"$PROJECT_ID\",
  \"requested_monthly_credits\": \"500.000\",
  \"models\": [{\"model_id\": \"$MODEL\"}]
}")"
KEY_ID="$(echo "$SUBMIT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('data',{}).get('id','')); 
import sys
if not d.get('data'): print(d.get('error',{}), file=sys.stderr)")"
MEN="$(login_session mentor@megaflow mentor123456 mentor)"
api "$MEN" -X POST "$BASE/api/v1/mentor/api-key-reviews/$KEY_ID/approve" \
  -H 'Content-Type: application/json' -d '{"monthly_credits":"500.000"}' >/dev/null
STU="$(login_session student@megaflow student123456 student)"
API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
KEY_PREFIX="${API_KEY:0:16}"
echo "key_prefix=$KEY_PREFIX marker=$MARKER model=$MODEL log=$LOG"

mkdir -p "$CODEX_DIR"
cat > "$CODEX_DIR/auth.json" <<EOF
{"OPENAI_API_KEY":"$API_KEY"}
EOF
cat > "$CODEX_DIR/config.toml" <<EOF
model_provider = "nebula-local"
model = "$MODEL"
model_reasoning_effort = "medium"
disable_response_storage = true
preferred_auth_method = "apikey"

[model_providers.nebula-local]
name = "nebula-local"
base_url = "$BASE/v1"
wire_api = "responses"
requires_openai_auth = true
EOF

PROMPT='请联网检索「隐式规划 implicit planning」相关的学术论文，找出3篇代表性论文，每篇给出标题、作者和一句话摘要。必须实际搜索，不要编造。'

echo ""
echo "== 2. codex exec (web research, real /v1/responses) =="
set +e
(
  cd "$REPO"
  CODEX_HOME="$CODEX_DIR" codex exec --skip-git-repo-check \
    -s workspace-write \
    --enable browser_use \
    --enable browser_use_external \
    -c 'sandbox_permissions=["disk-full-read-access"]' \
    --dangerously-bypass-approvals-and-sandbox \
    "$PROMPT"
) 2>&1 | tee "$LOG" | tail -80
CLI_RC=$?
set -e
echo "codex exec exit=$CLI_RC"

echo ""
echo "== 3. DB: gateway_calls since marker =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.created_at, c.request_path, c.call_index, c.outcome,
       left(c.previous_response_id,28) prev,
       left(c.upstream_response_id,28) upstream,
       c.total_tokens
FROM gateway_calls c
JOIN api_keys k ON k.id=c.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND c.created_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY c.created_at, c.call_index;
"

echo ""
echo "== 4. DB: turn aggregation =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.id, t.call_count, count(c.id) actual_calls,
       t.total_tokens, t.credit_milli, t.outcome,
       array_agg(c.call_index ORDER BY c.created_at) indexes,
       left(t.root_prompt,70) root_prompt
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '5 seconds'
GROUP BY t.id, t.call_count, t.total_tokens, t.credit_milli, t.outcome, t.root_prompt
ORDER BY min(c.created_at);
"

echo ""
echo "== 5. mentor call-turns detail =="
TURN_ID="$(docker exec nebula-api-postgres-1 psql -U nebula -d nebula -tAc "
SELECT t.id FROM gateway_call_turns t
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY t.started_at DESC LIMIT 1")"
if [[ -n "$TURN_ID" ]]; then
  MEN="$(login_session mentor@megaflow mentor123456 mentor)"
  curl -sS -H "Authorization: Bearer $MEN" "$BASE/api/v1/mentor/call-turns/$TURN_ID" \
    | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print('turn call_count=', d['call_count'], 'outcome=', d['outcome'], 'credits=', d['credits'])
print('root_prompt=', d['root_prompt_preview'][:80])
for c in d.get('calls') or []:
    print(f\"  #{c.get('call_index')} {c.get('outcome')} {c.get('path')} tokens={c.get('total_tokens')} credits={c.get('credits')}\")
"
fi

echo ""
echo "== 6. codex output tail keywords =="
rg -n "隐式|implicit|planning|论文|title|Author" "$LOG" | tail -20 || tail -30 "$LOG"
