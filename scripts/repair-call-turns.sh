#!/usr/bin/env bash
set -Eeuo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${repository_root}/.env.local"
dry_run=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=true ;;
    -h|--help)
      printf 'Usage: %s [--dry-run]\n' "$(basename "$0")"
      exit 0
      ;;
  esac
done

if [[ ! -f "$env_file" ]]; then
  printf 'Missing %s\n' "$env_file" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

version="$(tr -d '\r\n' < "${repository_root}/VERSION")"
export DATABASE_URL="${DATABASE_URL//localhost/postgres}"
export DATABASE_URL="${DATABASE_URL//127.0.0.1/postgres}"
export NEBULA_VERSION="$version"

args=()
if [[ "$dry_run" == true ]]; then
  args+=(--dry-run)
fi

cd "$repository_root"
docker compose --project-name nebula-api --file compose.yaml --file compose.local.yaml run --rm \
  --entrypoint /app/nebula-repair-call-turns \
  migrate "${args[@]}"
