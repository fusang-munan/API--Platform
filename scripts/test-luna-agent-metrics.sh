#!/usr/bin/env bash
# Agent round-count + token usage feasibility probe for gpt-5.6-luna.
# Usage: API_KEY=neb_sk_... ./scripts/test-luna-agent-metrics.sh
#        or leave unset to provision a fresh key (costs ~0.55 credits for 11 calls).
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
ROUNDS="${ROUNDS:-10}"
MODEL="${MODEL:-gpt-5.6-luna}"
COOKIE="$(mktemp)"
REPORT="$(mktemp)"
trap 'rm -f "$COOKIE" "$REPORT"' EXIT

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

provision_key() {
  echo "== provisioning fresh API key ==" >&2
  local stu_token men_token submit key_id claim
  stu_token="$(login_session student@megaflow student123456 student)"
  api "$stu_token" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
    -d "{\"model_id\":\"$MODEL\"}" >/dev/null || true
  submit="$(api "$stu_token" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
    \"name\": \"agent-metrics-$(date +%s)\",
    \"organization_id\": \"$ORG_ID\",
    \"project_id\": \"$PROJECT_ID\",
    \"requested_monthly_credits\": \"100.000\",
    \"models\": [{\"model_id\": \"$MODEL\"}]
  }")"
  key_id="$(echo "$submit" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
  men_token="$(login_session mentor@megaflow mentor123456 mentor)"
  api "$men_token" -X POST "$BASE/api/v1/mentor/api-key-reviews/$key_id/approve" \
    -H 'Content-Type: application/json' -d '{"monthly_credits":"100.000"}' >/dev/null
  stu_token="$(login_session student@megaflow student123456 student)"
  claim="$(api "$stu_token" -X POST "$BASE/api/v1/student/api-keys/$key_id/claim")"
  echo "$claim" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])"
}

if [[ -z "${API_KEY:-}" ]]; then
  API_KEY="$(provision_key)"
fi
export API_KEY

TASK_ID="agent-task-$(date +%s)-$(openssl rand -hex 4)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "task_id=$TASK_ID marker=$MARKER rounds=$ROUNDS"

python3 - "$API_KEY" "$TASK_ID" "$ROUNDS" "$MODEL" "$BASE" "$REPORT" <<'PY'
import json, os, subprocess, sys, urllib.request

api_key, task_id, rounds_s, model, base, report_path = sys.argv[1:7]
rounds = int(rounds_s)
messages = [{"role": "user", "content": "You are a counter. Reply with exactly: round-1-ok"}]
rows = []

def post_chat(body):
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
        payload = json.loads(resp.read().decode())
    usage = payload.get("usage") or {}
    content = ""
    try:
        content = payload["choices"][0]["message"]["content"]
    except Exception:
        pass
    return usage, content.strip(), payload.get("id")

for i in range(1, rounds + 1):
    body = {
        "model": model,
        "messages": messages,
        "max_tokens": 24,
        "user": task_id,
        "metadata": {"agent_task_id": task_id, "round": i},
    }
    usage, assistant, upstream_id = post_chat(body)
    rows.append({
        "round": i,
        "upstream_id": upstream_id,
        "assistant": assistant,
        "prompt_tokens": usage.get("prompt_tokens"),
        "completion_tokens": usage.get("completion_tokens"),
        "total_tokens": usage.get("total_tokens"),
    })
    messages.append({"role": "assistant", "content": assistant or f"round-{i}-ok"})
    if i < rounds:
        messages.append({"role": "user", "content": f"Now reply with exactly: round-{i+1}-ok"})

# one streaming call with same task id
stream_body = {
    "model": model,
    "messages": [{"role": "user", "content": "Reply with exactly: stream-ok"}],
    "max_tokens": 16,
    "stream": True,
    "user": task_id,
}
req = urllib.request.Request(
    f"{base}/v1/chat/completions",
    data=json.dumps(stream_body).encode(),
    headers={
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "X-Agent-Task-Id": task_id,
    },
    method="POST",
)
stream_usage = None
stream_text = []
with urllib.request.urlopen(req, timeout=120) as resp:
    for raw in resp:
        line = raw.decode(errors="replace").strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if data == "[DONE]":
            break
        try:
            chunk = json.loads(data)
        except json.JSONDecodeError:
            continue
        if chunk.get("usage"):
            stream_usage = chunk["usage"]
        delta = (((chunk.get("choices") or [{}])[0]).get("delta") or {}).get("content")
        if delta:
            stream_text.append(delta)

rows.append({
    "round": "stream",
    "upstream_id": None,
    "assistant": "".join(stream_text).strip(),
    "prompt_tokens": (stream_usage or {}).get("prompt_tokens"),
    "completion_tokens": (stream_usage or {}).get("completion_tokens"),
    "total_tokens": (stream_usage or {}).get("total_tokens"),
})

summary = {
    "task_id": task_id,
    "http_calls": len(rows),
    "rounds_non_stream": rounds,
    "client_prompt_tokens": sum(r["prompt_tokens"] or 0 for r in rows),
    "client_completion_tokens": sum(r["completion_tokens"] or 0 for r in rows),
    "client_total_tokens": sum(r["total_tokens"] or 0 for r in rows),
    "rows": rows,
}
with open(report_path, "w") as f:
    json.dump(summary, f, indent=2)
print(json.dumps(summary, indent=2))
PY

echo ""
echo "== DB: gateway_calls since marker (by api_key prefix) =="
KEY_PREFIX="$(python3 -c "import os; print(os.environ['API_KEY'][:16])")"
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.created_at, left(c.request_id,8) req, c.outcome, c.credit_milli,
       left(COALESCE(mi.content,''), 50) last_user_msg
FROM gateway_calls c
JOIN api_keys k ON k.id=c.api_key_id
LEFT JOIN monitored_inputs mi ON mi.call_id=c.id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND c.created_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY c.created_at;
"

echo ""
echo "== DB: gateway_call_turns =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.started_at, t.call_count, t.total_tokens, t.credit_milli, left(t.root_prompt,50) prompt
FROM gateway_call_turns t
JOIN api_keys k ON k.id=t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY t.started_at;
"

echo ""
echo "== DB: per-call tokens =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT c.call_index, c.prompt_tokens, c.completion_tokens, c.total_tokens, left(c.upstream_response_id,24) upstream
FROM gateway_calls c
JOIN api_keys k ON k.id=c.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND c.created_at >= timestamptz '${MARKER}' - interval '5 seconds'
ORDER BY c.created_at;
"

echo ""
echo "== DB schema: token / task correlation columns on gateway_calls =="
docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT column_name FROM information_schema.columns
WHERE table_name='gateway_calls'
  AND column_name ~* 'token|session|task|cache|response|metadata|user'
ORDER BY 1;
"

echo ""
echo "== client-side report saved =="
cat "$REPORT"
