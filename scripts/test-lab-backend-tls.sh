#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
repo="$PWD"
case "${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}" in
    unix://*) ;;
    *) echo "A local Linux Docker engine is required" >&2; exit 1 ;;
esac
work="$(mktemp -d)"
project="nebula-lab-backend-${GITHUB_RUN_ID:-0}-${GITHUB_RUN_ATTEMPT:-0}-$$"
export NEBULA_VERSION="$(tr -d '\r\n' < VERSION)"

compose=(docker compose -p "$project" --env-file "$work/empty.env"
    -f compose.yaml -f compose.local.yaml -f "$work/ci.yaml")
cleanup() {
    local status=$?
    if [[ -f "$work/ci.yaml" ]]; then
        if (( status != 0 )); then
            "${compose[@]}" ps -a || true
            "${compose[@]}" logs --no-color postgres redis migrate backend || true
        fi
        "${compose[@]}" down --volumes --remove-orphans || true
    fi
    docker image rm "$project:ci" >/dev/null 2>&1 || true
    rm -rf -- "$work"
}
trap cleanup EXIT

# Compile with the runner's Go toolchain and use the real Dockerfile runtime stage.
mkdir "$work/out"
export CGO_ENABLED=0
for entry in server migrate maintenance repair-call-turns providercatalog; do
    binary="$entry"
    if [[ "$entry" == providercatalog ]]; then
        binary=provider-catalog
    fi
    go build -trimpath -o "$work/out/nebula-$binary" "./cmd/$entry"
done
sed -n '/^FROM .* AS runtime$/,$p' "$repo/Dockerfile" |
    sed -e 's|^FROM docker.m.daocloud.io/library/|FROM docker.io/library/|' \
        -e 's|^COPY --from=builder \(.*\) /out/|COPY \1 out/|' > "$work/Dockerfile"
docker build --build-arg "VERSION=$NEBULA_VERSION" -t "$project:ci" "$work"

: > "$work/empty.env"
export CI_NEBULA_DB_PASSWORD="$(openssl rand -hex 16)"
export CI_NEBULA_DATABASE_URL="postgres://nebula:${CI_NEBULA_DB_PASSWORD}@postgres:5432/nebula?sslmode=disable"
export CI_NEBULA_JWT_KEY="$(openssl rand -hex 32)"
export CI_NEBULA_AUTH_PEPPER="$(openssl rand -hex 32)"
export CI_NEBULA_API_PEPPER="$(openssl rand -hex 32)"
export CI_NEBULA_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export CI_NEBULA_TEACHER_PASSWORD="$(openssl rand -hex 16)"
cat > "$work/ci.yaml" <<'YAML'
services:
  postgres:
    image: docker.io/library/postgres:17.5
    environment:
      POSTGRES_USER: nebula
      POSTGRES_PASSWORD: ${CI_NEBULA_DB_PASSWORD}
      POSTGRES_DB: nebula
  redis:
    image: docker.io/library/redis:8.2.8-alpine3.22
  migrate:
    image: ${CI_NEBULA_IMAGE}
    build: !reset null
    environment:
      DATABASE_URL: ${CI_NEBULA_DATABASE_URL}
  backend:
    image: ${CI_NEBULA_IMAGE}
    build: !reset null
    ports: !override
      - "127.0.0.1::8080"
      - "127.0.0.1::8443"
    environment:
      APP_ENV: test
      HTTP_ADDRESS: ":8080"
      DATABASE_URL: ${CI_NEBULA_DATABASE_URL}
      REDIS_URL: redis://redis:6379/0
      JWT_SIGNING_KEY: ${CI_NEBULA_JWT_KEY}
      AUTH_STATE_HASH_PEPPER: ${CI_NEBULA_AUTH_PEPPER}
      API_KEY_HASH_PEPPER: ${CI_NEBULA_API_PEPPER}
      PROVIDER_CREDENTIAL_ENCRYPTION_KEY: ${CI_NEBULA_ENCRYPTION_KEY}
      BOOTSTRAP_TEACHER_NAME: CI Teacher
      BOOTSTRAP_TEACHER_EMAIL: teacher@example.test
      BOOTSTRAP_TEACHER_PASSWORD: ${CI_NEBULA_TEACHER_PASSWORD}
      SMTP_MODE: log
      LAB_MAINTENANCE_TLS_ORIGIN: https://127.0.0.1
      HTTP_PROXY: ""
      HTTPS_PROXY: ""
      BEICHEN_PANEL_TOKEN: ""
      BEICHEN_PANEL_GROUPS: ""
YAML
export CI_NEBULA_IMAGE="$project:ci"

"${compose[@]}" up -d --wait --wait-timeout 90 postgres redis
"${compose[@]}" run --rm --no-deps migrate
# Inspect the fresh named volume with the actual image user before Go starts.
"${compose[@]}" run --rm --no-deps --entrypoint sh backend -ec '
    test "$(id -u):$(id -g)" = 10001:10001
    test -d "$LAB_MAINTENANCE_TLS_DIR"
    test -z "$(ls -A "$LAB_MAINTENANCE_TLS_DIR")"
'

request=(curl --noproxy '*' --silent --show-error --connect-timeout 2 --max-time 5)
start_backend() {
    # Databases and migration are already ready. Skip the school-only proxy relay.
    "${compose[@]}" up -d --no-deps --no-build --force-recreate --wait --wait-timeout 90 backend
    http_address="$("${compose[@]}" port backend 8080)"
    https_address="$("${compose[@]}" port backend 8443)"
    "${request[@]}" --fail "http://$http_address/health/ready" >/dev/null
}

read_cert() {
    "${compose[@]}" exec -T backend sh -ec '
        test "$(id -u):$(id -g)" = 10001:10001
        identity="$LAB_MAINTENANCE_TLS_DIR/identity.pem"
        test "$(stat -c "%a %u:%g" "$identity")" = "600 10001:10001"
        sed -n "/^-----BEGIN CERTIFICATE-----$/,/^-----END CERTIFICATE-----$/p" "$identity"
    '
}

check_teacher_auth() {
    local url="$1" status
    shift
    status="$("${request[@]}" --output "$work/response.json" --write-out '%{http_code}' \
        "$@" "$url/api/v1/teacher/providers")"
    [[ "$status" == 401 ]] || { echo "Expected teacher HTTP status 401, got $status" >&2; return 1; }
    jq -e '.error.code == "AUTHENTICATION_REQUIRED"' "$work/response.json" >/dev/null
}

start_backend
first_backend="$("${compose[@]}" ps -q backend)"
docker inspect --format '{{json .Mounts}}' "$first_backend" |
    jq -e --arg name "${project}_maintenance-tls" 'any(.[]; .Type == "volume" and
        .Name == $name and .Destination == "/app/maintenance-tls" and .RW)' >/dev/null
read_cert > "$work/first.pem"
check_teacher_auth "http://$http_address"
check_teacher_auth "https://$https_address" --cacert "$work/first.pem"

start_backend
second_backend="$("${compose[@]}" ps -q backend)"
[[ -n "$first_backend" && -n "$second_backend" && "$first_backend" != "$second_backend" ]]
read_cert > "$work/second.pem"
cmp "$work/first.pem" "$work/second.pem"
check_teacher_auth "http://$http_address"
check_teacher_auth "https://$https_address" --cacert "$work/first.pem"
echo "School backend passed HTTP readiness, HTTPS certificate creation and reuse, and anonymous teacher authentication checks"
