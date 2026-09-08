#!/usr/bin/env bash
# Local Docker Compose wrapper for Linux (no Doppler).
# Loads .env.local, rewrites DATABASE_URL/REDIS_URL for Compose DNS, then runs compose.
set -Eeuo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${repository_root}/.env.local"

if [[ ! -f "$env_file" ]]; then
  printf 'Missing %s — copy .env.example and fill in secrets, or run scripts/init-local-env.sh\n' "$env_file" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

version="$(tr -d '\r\n' < "${repository_root}/VERSION")"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'VERSION must contain a semantic version\n' >&2
  exit 1
fi

required=(
  JWT_SIGNING_KEY AUTH_STATE_HASH_PEPPER API_KEY_HASH_PEPPER
  PROVIDER_CREDENTIAL_ENCRYPTION_KEY
  BOOTSTRAP_TEACHER_NAME BOOTSTRAP_TEACHER_EMAIL BOOTSTRAP_TEACHER_PASSWORD
  SMTP_HOST SMTP_FROM DATABASE_URL REDIS_URL
)
for name in "${required[@]}"; do
  value="${!name:-}"
  if [[ -z "$value" || "$value" == replace_with* ]]; then
    printf 'Missing or placeholder value for %s in .env.local\n' "$name" >&2
    exit 1
  fi
done

if [[ "${HTTP_ADDRESS:-:8080}" != ":8080" ]]; then
  printf 'HTTP_ADDRESS must be :8080 for local Compose\n' >&2
  exit 1
fi

# Rewrite loopback hosts to Compose service names.
export DATABASE_URL
export REDIS_URL
export DATABASE_URL="${DATABASE_URL//localhost/postgres}"
export DATABASE_URL="${DATABASE_URL//127.0.0.1/postgres}"
export REDIS_URL="${REDIS_URL//localhost/redis}"
export REDIS_URL="${REDIS_URL//127.0.0.1/redis}"

if [[ "$DATABASE_URL" =~ postgres://([^:/]+):([^@]+)@postgres:5432/([^?]+) ]]; then
  export POSTGRES_USER="${BASH_REMATCH[1]}"
  export POSTGRES_PASSWORD="${BASH_REMATCH[2]}"
  export POSTGRES_DB="${BASH_REMATCH[3]}"
else
  printf 'DATABASE_URL must be postgres://user:pass@postgres:5432/dbname after rewrite\n' >&2
  exit 1
fi

export NEBULA_VERSION="$version"

cd "$repository_root"
if [[ $# -eq 0 ]]; then
  set -- up -d --build
fi
exec docker compose --project-name nebula-api --file compose.yaml --file compose.local.yaml "$@"
