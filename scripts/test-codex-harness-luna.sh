#!/usr/bin/env bash
# Real-env: Codex harness (wire_api=responses) + luna, and gpt-5.3-codex connectivity probe.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
COOKIE="$(mktemp)"
CODEX_DIR="$(mktemp -d)"
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

provision_key() {
  local name="$1"; shift
  local models_json="$1"
  local stu men submit key_id
  stu="$(login_session student@megaflow student123456 student)"
  for mid in "$@"; do
    api "$stu" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
      -d "{\"model_id\":\"$mid\"}" >/dev/null || true
  done
  submit="$(api "$stu" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
    \"name\": \"$name\",
    \"organization_id\": \"$ORG_ID\",
    \"project_id\": \"$PROJECT_ID\",
    \"requested_monthly_credits\": \"50.000\",
    \"models\": $models_json
  }")"
  key_id="$(echo "$submit" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
  men="$(login_session mentor@megaflow mentor123456 mentor)"
  api "$men" -X POST "$BASE/api/v1/mentor/api-key-reviews/$key_id/approve" \
    -H 'Content-Type: application/json' -d '{"monthly_credits":"50.000"}' >/dev/null
  stu="$(login_session student@megaflow student123456 student)"
  api "$stu" -X POST "$BASE/api/v1/student/api-keys/$key_id/claim" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])"
}

probe() {
  local label="$1" method="$2" path="$3" api_key="$4" body="$5"
  echo ""
  echo "== probe: $label =="
  local out status
  out="$(curl -sS -w "\n__HTTP__:%{http_code}" -X "$method" "$BASE$path" \
    -H "Authorization: Bearer $api_key" -H 'Content-Type: application/json' -d "$body")"
  status="${out##*__HTTP__:}"
  out="${out%__HTTP__:*}"
  echo "HTTP $status"
  echo "$out" | python3 -c "
import sys,json
raw=sys.stdin.read().strip()
try:
  d=json.loads(raw)
except Exception:
  print(raw[:800])
  raise SystemExit(0)
err=d.get('error') or {}
if err:
  print('error:', err.get('type'), err.get('code'), err.get('message','')[:300])
else:
  print('id:', d.get('id',''))
  u=d.get('usage') or {}
  if u: print('usage:', u)
  ch=d.get('choices') or []
  if ch:
    c=ch[0].get('message',{}).get('content') or ch[0].get('delta',{}).get('content')
    if c: print('content:', c[:120])
  out=d.get('output') or []
  if out:
    for item in out[:2]:
      if isinstance(item, dict) and item.get('content'):
        print('output:', str(item.get('content'))[:120])
" 2>/dev/null || echo "$out" | head -20
}

API_KEY="$(provision_key "harness-probe-$(date +%s)" '[{"model_id":"gpt-5.6-luna"},{"model_id":"gpt-5.3-codex"}]' gpt-5.6-luna gpt-5.3-codex)"
echo "key_prefix=${API_KEY:0:16}..."

probe "luna chat/completions" POST "/v1/chat/completions" "$API_KEY" \
  '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Reply exactly: luna-harness-ok"}],"max_tokens":16}'

probe "luna responses (Codex harness path)" POST "/v1/responses" "$API_KEY" \
  '{"model":"gpt-5.6-luna","input":[{"role":"user","content":"Reply exactly: luna-responses-ok"}],"max_output_tokens":32,"store":false}'

TEACH="$(login_session teacher@megaflow teacher123456 teacher)"
echo ""
echo "== teacher probe: gpt-5.3-codex upstream =="
curl -sS -H "Authorization: Bearer $TEACH" -X POST "$BASE/api/v1/teacher/model-probes" \
  -H 'Content-Type: application/json' \
  -d '{"provider_id":"01a05889-5ea9-781f-a31f-bac4f3dab44a","upstream_model_name":"gpt-5.3-codex","endpoints":["responses","chat_completions"]}' \
  | python3 -m json.tool 2>&1 | head -40

probe "codex chat/completions" POST "/v1/chat/completions" "$API_KEY" \
  '{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"Reply exactly: codex-chat-ok"}],"max_tokens":16}'

probe "codex responses (Codex harness path)" POST "/v1/responses" "$API_KEY" \
  '{"model":"gpt-5.3-codex","input":[{"role":"user","content":"Reply exactly: codex-responses-ok"}],"max_output_tokens":32,"store":false}'

echo ""
echo "== Codex CLI harness exec (luna via /v1/responses) =="
cat > "$CODEX_DIR/auth.json" <<EOF
{"OPENAI_API_KEY":"$API_KEY"}
EOF
cat > "$CODEX_DIR/config.toml" <<EOF
model_provider = "nebula-local"
model = "gpt-5.6-luna"
model_reasoning_effort = "low"
disable_response_storage = true
preferred_auth_method = "apikey"

[model_providers.nebula-local]
name = "nebula-local"
base_url = "$BASE/v1"
wire_api = "responses"
requires_openai_auth = true
EOF

set +e
CODEX_HOME="$CODEX_DIR" codex exec --skip-git-repo-check \
  -c model='"gpt-5.6-luna"' \
  -c model_provider='"nebula-local"' \
  -c 'model_providers.nebula-local.base_url='"\"$BASE/v1\"" \
  -c 'model_providers.nebula-local.wire_api='"\"responses\"" \
  -c 'model_providers.nebula-local.requires_openai_auth=true' \
  -c preferred_auth_method='"apikey"' \
  -c disable_response_storage=true \
  "Reply with exactly one line: codex-harness-luna-ok" 2>&1 | tail -40
CLI_RC=$?
set -e
echo "codex exec exit=$CLI_RC"

echo ""
echo "== DB bindings check =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT m.model_id, mb.adapter, mb.priority, mb.status
FROM models m JOIN model_bindings mb ON mb.model_id=m.id
WHERE m.model_id IN ('gpt-5.6-luna','gpt-5.3-codex') AND mb.status='active'
ORDER BY m.model_id, mb.priority;
"
