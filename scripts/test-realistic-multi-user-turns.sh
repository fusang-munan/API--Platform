#!/usr/bin/env bash
# Realistic acceptance: one client session, 3 distinct user prompts (real dialogue).
# One user prompt = one gateway_call_turn. Codex: gpt-5.6-terra; Claude Code: claude-sonnet-5.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:25556}"
REPO="/data/home/lab_proj/MegaFlowAI/api-platform"
ORG_ID="${ORG_ID:-01a05c65-1b61-77f2-87c1-19a916ee8f9f}"
PROJECT_ID="${PROJECT_ID:-01a05c65-6e75-7e5a-a701-b9535003c194}"
CODEX_MODEL="${CODEX_MODEL:-gpt-5.6-terra}"
CLAUDE_MODEL="${CLAUDE_MODEL:-claude-sonnet-5}"
CLAUDE="${CLAUDE:-/data/home/lab_proj/.cursor-server/extensions/anthropic.claude-code-2.1.257/resources/native-binary/claude}"
RUN="${RUN:-both}" # codex | claude | both

STAMP="$(date +%s)"
MARKER="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COOKIE="$(mktemp)"
CODEX_DIR="$(mktemp -d)"
CC_SESSION="$(python3 -c 'import uuid; print(uuid.uuid4())')"
LOG_CODEX="/tmp/codex-multi-turn-${STAMP}.log"
LOG_CLAUDE="/tmp/claude-multi-turn-${STAMP}.log"

# Three-round real dialogue (self-evolving literature + ReAct each turn).
read -r -d '' USER_ROUND_1 <<EOF || true
[$MARKER][USER-ROUND-1] 我想要了解自进化的相关文献，你可以推荐几篇并阅读后帮我做个简要分析吗？请用 ReAct 节奏：先说明你打算做什么，再调用工具（检索或阅读），最后给出结论。
EOF
read -r -d '' USER_ROUND_2 <<EOF || true
[$MARKER][USER-ROUND-2] 谢谢。请针对你刚才最推荐的一篇，深入说明它的核心方法和对「自进化」的定义；继续用 ReAct（先思考再行动再观察，最后结论），引用你读到的要点。
EOF
read -r -d '' USER_ROUND_3 <<EOF || true
[$MARKER][USER-ROUND-3] 最后一轮：把你目前讨论过的文献做一个对比式总结（问题设定、方法、结论、局限），并告诉我下一步最该精读哪一篇；同样保持 ReAct 结构，完整作答。
EOF

cleanup() {
  rm -f "$COOKIE"
  rm -rf "$CODEX_DIR"
}
trap cleanup EXIT

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
  local stu men submit key_id
  stu="$(login_session student@megaflow student123456 student)"
  for mid in "$CODEX_MODEL" "$CLAUDE_MODEL"; do
    api "$stu" -X POST "$BASE/api/v1/student/models" -H 'Content-Type: application/json' \
      -d "{\"model_id\":\"$mid\"}" >/dev/null || true
  done
  submit="$(api "$stu" -X POST "$BASE/api/v1/student/api-keys" -H 'Content-Type: application/json' -d "{
    \"name\": \"multi-turn-dialog-${STAMP}\",
    \"organization_id\": \"$ORG_ID\",
    \"project_id\": \"$PROJECT_ID\",
    \"requested_monthly_credits\": \"100.000\",
    \"models\": [{\"model_id\": \"$CODEX_MODEL\"}, {\"model_id\": \"$CLAUDE_MODEL\"}]
  }")"
  if echo "$submit" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin).get('data') else 1)"; then
    key_id="$(echo "$submit" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])")"
    men="$(login_session mentor@megaflow mentor123456 mentor)"
    api "$men" -X POST "$BASE/api/v1/mentor/api-key-reviews/$key_id/approve" \
      -H 'Content-Type: application/json' -d '{"monthly_credits":"100.000"}' >/dev/null
    stu="$(login_session student@megaflow student123456 student)"
    api "$stu" -X POST "$BASE/api/v1/student/api-keys/$key_id/claim" \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])"
    return 0
  fi
  stu="$(login_session student@megaflow student123456 student)"
  key_id="$(api "$stu" "$BASE/api/v1/student/api-keys" | python3 -c "
import sys,json
keys=[k for k in json.load(sys.stdin).get('data',[]) if k.get('status')=='active']
print(keys[0]['id'] if keys else '')
")"
  if [[ -z "$key_id" ]]; then
    echo "no active API key: $submit" >&2
    exit 1
  fi
  api "$stu" -X POST "$BASE/api/v1/student/api-keys/$key_id/regenerate" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['api_key'])"
}

db_turns() {
  local label="$1"
  echo ""
  echo "== DB turns ($label) since $MARKER =="
  docker exec nebula-api-postgres-1 psql -U nebula -d nebula -c "
SELECT t.id, t.model_name, t.call_count, t.root_prompt_bytes,
       left(t.root_prompt,80) AS root_prompt,
       array_agg(c.call_index ORDER BY c.created_at) AS call_indexes,
       array_agg(c.outcome ORDER BY c.created_at) AS outcomes
FROM gateway_call_turns t
JOIN gateway_calls c ON c.turn_id = t.id
JOIN api_keys k ON k.id = t.api_key_id
WHERE k.key_prefix LIKE '${KEY_PREFIX}%'
  AND t.started_at >= timestamptz '${MARKER}' - interval '10 seconds'
GROUP BY t.id, t.model_name, t.call_count, t.root_prompt_bytes, t.root_prompt
ORDER BY min(c.created_at);
"
}

echo "== provision key (models: $CODEX_MODEL, $CLAUDE_MODEL) =="
API_KEY="$(provision_key)"
KEY_PREFIX="${API_KEY:0:16}"
echo "key_prefix=$KEY_PREFIX marker=$MARKER cc_session=$CC_SESSION"
echo ""
echo "== user round 1 preview =="
echo "$USER_ROUND_1" | head -c 120
echo "..."

run_codex() {
  mkdir -p "$CODEX_DIR"
  cat > "$CODEX_DIR/auth.json" <<EOF
{"OPENAI_API_KEY":"$API_KEY"}
EOF
  cat > "$CODEX_DIR/config.toml" <<EOF
model_provider = "nebula-local"
model = "$CODEX_MODEL"
model_reasoning_effort = "low"
disable_response_storage = true
preferred_auth_method = "apikey"

[model_providers.nebula-local]
name = "nebula-local"
base_url = "$BASE/v1"
wire_api = "responses"
requires_openai_auth = true
EOF

  local session_id
  echo ""
  echo "== Codex exec: 3 user rounds ($CODEX_MODEL) =="
  set +e
  (
    cd "$REPO"
    CODEX_HOME="$CODEX_DIR" codex exec --skip-git-repo-check \
      -s workspace-write \
      -c 'sandbox_permissions=["disk-full-read-access"]' \
      --dangerously-bypass-approvals-and-sandbox \
      "$USER_ROUND_1"
  ) 2>&1 | tee -a "$LOG_CODEX" | tail -30
  session_id="$(rg -o 'session id: [0-9a-f-]+' "$LOG_CODEX" | tail -1 | awk '{print $3}')"
  for round in 2 3; do
    local prompt="$USER_ROUND_2"
    [[ "$round" == "3" ]] && prompt="$USER_ROUND_3"
    echo "--- codex user round $round ---"
    if [[ -n "$session_id" ]]; then
      (
        cd "$REPO"
        CODEX_HOME="$CODEX_DIR" codex exec resume "$session_id" --skip-git-repo-check \
          -c 'sandbox_permissions=["disk-full-read-access"]' \
          --dangerously-bypass-approvals-and-sandbox \
          "$prompt"
      ) 2>&1 | tee -a "$LOG_CODEX" | tail -25
    else
      (
        cd "$REPO"
        CODEX_HOME="$CODEX_DIR" codex exec resume --last --skip-git-repo-check \
          -c 'sandbox_permissions=["disk-full-read-access"]' \
          --dangerously-bypass-approvals-and-sandbox \
          "$prompt"
      ) 2>&1 | tee -a "$LOG_CODEX" | tail -25
    fi
  done
  set -e
  echo "codex log=$LOG_CODEX"
  db_turns "codex"
}

run_claude() {
  export ANTHROPIC_BASE_URL="$BASE"
  export ANTHROPIC_API_KEY="$API_KEY"
  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
  export CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1

  echo ""
  echo "== Claude Code: 3 user rounds ($CLAUDE_MODEL) =="
  set +e
  (
    cd "$REPO"
    "$CLAUDE" -p "$USER_ROUND_1" --model "$CLAUDE_MODEL" --session-id "$CC_SESSION" \
      --max-turns 24 --add-dir "$REPO" --dangerously-skip-permissions
  ) 2>&1 | tee "$LOG_CLAUDE" | tail -40
  echo "--- claude user round 2 ---"
  (
    cd "$REPO"
    "$CLAUDE" -p "$USER_ROUND_2" --model "$CLAUDE_MODEL" --continue \
      --max-turns 24 --add-dir "$REPO" --dangerously-skip-permissions
  ) 2>&1 | tee -a "$LOG_CLAUDE" | tail -35
  echo "--- claude user round 3 ---"
  (
    cd "$REPO"
    "$CLAUDE" -p "$USER_ROUND_3" --model "$CLAUDE_MODEL" --continue \
      --max-turns 24 --add-dir "$REPO" --dangerously-skip-permissions
  ) 2>&1 | tee -a "$LOG_CLAUDE" | tail -35
  set -e
  echo "claude log=$LOG_CLAUDE"
  db_turns "claude"
}

case "$RUN" in
  codex) run_codex ;;
  claude) run_claude ;;
  both) run_codex; run_claude ;;
  *) echo "RUN must be codex|claude|both" >&2; exit 1 ;;
esac
