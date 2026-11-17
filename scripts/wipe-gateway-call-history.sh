#!/usr/bin/env bash
# Remove all mentor call-review history (gateway turns/calls). Does not touch api_keys or users.
set -Eeuo pipefail

PG_CONTAINER="${PG_CONTAINER:-nebula-api-postgres-1}"
PG_USER="${PG_USER:-nebula}"
PG_DB="${PG_DB:-nebula}"

confirm="${WIPE_CONFIRM:-}"
if [[ "${1:-}" == "--yes" ]]; then
  confirm=yes
fi
if [[ "$confirm" != "yes" ]]; then
  printf 'This deletes ALL rows in monitored_inputs, gateway_call_attempts, gateway_calls, gateway_call_turns.\n'
  printf 'Re-run with: WIPE_CONFIRM=yes %s --yes\n' "$(basename "$0")"
  exit 1
fi

docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
DELETE FROM monitored_inputs;
DELETE FROM gateway_call_attempts;
DELETE FROM gateway_calls;
DELETE FROM gateway_call_turns;
COMMIT;
SELECT
  (SELECT count(*) FROM gateway_call_turns) AS turns,
  (SELECT count(*) FROM gateway_calls) AS calls,
  (SELECT count(*) FROM monitored_inputs) AS monitored;
SQL
