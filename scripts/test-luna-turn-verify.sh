#!/usr/bin/env bash
# Verify call-review turn grouping with gpt-5.6-luna:
# - separate HTTP prompts => separate turns (call_count=1 each)
# - same X-Agent-Task-Id => one turn with multiple calls
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
MODEL="${MODEL:-gpt-5.6-luna}"
ROUNDS="${ROUNDS:-4}"
COOKIE="$(mktemp)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
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

echo "== 1. ensure single active key =="
STU="$(login_session student@megaflow student123456 student)"
ACTIVE_ID="$(api "$STU" "$BASE/api/v1/student/api-keys" | python3 -c "
import sys,json
keys=[k for k in json.load(sys.stdin)['data'] if k['status'] in ('active','approved')]
print(keys[0]['id'] if keys else '')
")"
if [[ -n "$ACTIVE_ID" ]]; then
  MEN="$(login_session mentor@megaflow mentor123456 mentor)"
  api "$MEN" -X POST "$BASE/api/v1/mentor/api-keys/$ACTIVE_ID/revoke" \
    -H 'Content-Type: application/json' -d '{"comment":"turn verify rotate"}' >/dev/null
  echo "revoked previous key $ACTIVE_ID"
fi

echo "== 2. provision one key ($MODEL) =="
STU="$(login_session student@megaflow student123456 student)"
api "$STU" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
  -d "{\"model_id\":\"$MODEL\"}" >/dev/null || true
SUBMIT="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
  \"name\": \"luna-turn-verify-$(date +%s)\",
  \"organization_id\": \"$ORG_ID\",
  \"project_id\": \"$PROJECT_ID\",
  \"requested_monthly_credits\": \"200.000\",
  \"models\": [{\"model_id\": \"$MODEL\"}]
}")"
KEY_ID="$(echo "$SUBMIT" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
MEN="$(login_session mentor@megaflow mentor123456 mentor)"
api "$MEN" -X POST "$BASE/api/v1/mentor/api-key-reviews/$KEY_ID/approve" \
  -H 'Content-Type: application/json' -d '{"monthly_credits":"200.000"}' >/dev/null
STU="$(login_session student@megaflow student123456 student)"
API_KEY="$(api "$STU" -X POST "$BASE/api/v1/student/api-keys/$KEY_ID/claim" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])")"
KEY_PREFIX="${API_KEY:0:16}"
echo "key_id=$KEY_ID prefix=$KEY_PREFIX marker=$MARKER"

echo "== 3. single prompt (expect 1 turn, 1 call) =="
curl -sS -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Reply exactly: verify-single-ok"}],"max_tokens":16}' >/dev/null

TASK_ID="agent-task-$(date +%s)"
echo "== 4. agent task $ROUNDS rounds with same X-Agent-Task-Id (expect 1 turn, $ROUNDS calls) =="
python3 - "$API_KEY" "$TASK_ID" "$ROUNDS" "$MODEL" "$BASE" <<'PY'
import json, sys, urllib.request

api_key, task_id, rounds_s, model, base = sys.argv[1:6]
rounds = int(rounds_s)
messages = [{"role": "user", "content": "You are a counter. Reply with exactly: round-1-ok"}]

def post(body):
    req = urllib.request.Request(
        f"{base}/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "X-Agent-Task-Id": task_id,
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=120) as resp:
        return json.loads(resp.read().decode())

for i in range(1, rounds + 1):
    payload = post({
        "model": model,
        "messages": messages,
        "max_tokens": 24,
        "user": task_id,
        "metadata": {"agent_task_id": task_id, "round": i},
    })
    content = payload["choices"][0]["message"]["content"].strip()
    messages.append({"role": "assistant", "content": content or f"round-{i}-ok"})
    if i < rounds:
        messages.append({"role": "user", "content": f"Now reply with exactly: round-{i+1}-ok"})
    print(f"round {i}: {content[:40]}")
PY

echo ""
echo "== 5. DB turns since marker =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.started_at, t.call_count, count(c.id) actual_calls,
       left(t.root_prompt,45) root_prompt, left(COALESCE(t.task_key,''),24) task_key
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id=t.id
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '5 seconds'
GROUP BY t.id, t.started_at, t.call_count, t.root_prompt, t.task_key
ORDER BY t.started_at;
"

echo ""
echo "== 6. DB calls in agent turn =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.call_index, c.outcome, c.total_tokens, left(COALESCE(t.task_key,''),24) task_key
FROM gateway_calls c
JOIN gateway_call_turns t ON t.id=c.turn_id
JOIN api_keys k ON k.id=c.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.task_key='${TASK_ID}'
ORDER BY c.call_index;
"

echo ""
echo "== 7. mentor call-turns since marker =="
MEN="$(login_session mentor@megaflow mentor123456 mentor)"
curl -sS -H "Authorization: Bearer $MEN" "$BASE/api/v1/mentor/call-turns?project_id=${PROJECT_ID}" \
  | python3 -c "
import sys,json
items=json.load(sys.stdin)['data']['items']
recent=[i for i in items if i.get('started_at','') >= '${MARKER}'[:19]]
for item in recent:
    print(f\"- {item['root_prompt_preview'][:40]!r} call_count={item['call_count']} credits={item['credits']} task={item.get('task_key') or '-'}\")
print('total_recent', len(recent))
"

echo ""
echo "== 8. key count per student/project =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT u.name student, p.name project, count(*) FILTER (WHERE k.status IN ('active','approved','pending_mentor')) live_keys
FROM users u
JOIN project_members pm ON pm.user_id=u.id
JOIN projects p ON p.id=pm.project_id
LEFT JOIN api_keys k ON k.owner_user_id=u.id AND k.project_id=p.id
WHERE u.name='Test Student' AND p.name='api平台'
GROUP BY u.name, p.name;
"
