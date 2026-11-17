#!/usr/bin/env bash
# Generate .env.local with development defaults for Docker Compose on Linux.
set -Eeuo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${repository_root}/.env.local"

if [[ -f "$env_file" ]]; then
  printf '.env.local already exists — remove it first to regenerate\n' >&2
  exit 1
fi

rand_b64() { openssl rand -base64 "$1" | tr -d '\n'; }

jwt_key="$(rand_b64 32)"
hash_pepper="$(rand_b64 32)"
api_key_pepper="$(rand_b64 32)"
provider_key="$(openssl rand -base64 32 | tr -d '\n')"
db_pass="$(openssl rand -hex 16)"

umask 077
cat > "$env_file" <<EOF
APP_ENV=development
HTTP_ADDRESS=:8080
PUBLIC_APP_URL=http://127.0.0.1:25555
DATABASE_URL=postgres://nebula:${db_pass}@localhost:5432/nebula?sslmode=disable
REDIS_URL=redis://localhost:6379/0

JWT_SIGNING_KEY=${jwt_key}
ACCESS_TOKEN_TTL_MINUTES=15
REFRESH_TOKEN_TTL_HOURS=168
AUTH_STATE_HASH_PEPPER=${hash_pepper}
API_KEY_HASH_PEPPER=${api_key_pepper}
PROVIDER_CREDENTIAL_ENCRYPTION_KEY=${provider_key}

BOOTSTRAP_TEACHER_NAME="Platform Teacher"
BOOTSTRAP_TEACHER_EMAIL=teacher@megaflow
BOOTSTRAP_TEACHER_PASSWORD=teacher123456

TEST_STUDENT_1_NAME="Test Student"
TEST_STUDENT_1_EMAIL=student@megaflow
TEST_STUDENT_1_PASSWORD=student123456

TEST_MENTOR_1_NAME="Test Mentor"
TEST_MENTOR_1_EMAIL=mentor@megaflow
TEST_MENTOR_1_PASSWORD=mentor123456

SMTP_MODE=log
EXPOSE_VERIFICATION_CODE_IN_RESPONSE=true
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USER=
SMTP_PASS=
SMTP_FROM=noreply@megaflow
SMTP_FROM_NAME="Nebula API"
SMTP_TLS_MODE=starttls
SMTP_TIMEOUT_SECONDS=30
EMAIL_VERIFICATION_CODE_TTL_MINUTES=10
EMAIL_VERIFICATION_SEND_COOLDOWN_SECONDS=60
EMAIL_VERIFICATION_MAX_ATTEMPTS=5
EMAIL_VERIFICATION_LOCKOUT_MINUTES=15
TEACHER_INVITATION_TTL_HOURS=24

SSE_STREAM_MAX_LENGTH=1000
SSE_HEARTBEAT_SECONDS=15
UPSTREAM_CONNECT_TIMEOUT_SECONDS=10
UPSTREAM_RESPONSE_HEADER_TIMEOUT_SECONDS=60
GATEWAY_MAX_REQUEST_BYTES=104857600
GATEWAY_RESOURCE_ROUTE_TTL_HOURS=24
EOF

chmod 600 "$env_file"
printf 'Created %s\n' "$env_file"
printf 'Initial accounts:\n'
printf '  teacher  teacher@megaflow  / teacher123456\n'
printf '  mentor   mentor@megaflow   / mentor123456\n'
printf '  student  student@megaflow  / student123456\n'
