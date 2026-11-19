#!/usr/bin/env bash
set +x
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
            "${compose[@]}" logs --no-color postgres redis frontend || true
        fi
        "${compose[@]}" down --volumes --remove-orphans || true
    fi
    docker image rm "$project:ci" "$project:frontend" "$project:nginx-base" >/dev/null 2>&1 || true
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
go build -trimpath -o "$work/out/ci-smtp-receiver" ./scripts/ci-smtp-receiver.go
sed -n '/^FROM .* AS runtime$/,$p' "$repo/Dockerfile" |
    sed -e 's|^FROM docker.m.daocloud.io/library/|FROM docker.io/library/|' \
        -e 's|^COPY --from=builder \(.*\) /out/|COPY \1 out/|' > "$work/Dockerfile"
echo 'COPY --chown=10001:10001 out/ci-smtp-receiver /app/ci-smtp-receiver' >> "$work/Dockerfile"
docker build --build-arg "VERSION=$NEBULA_VERSION" -t "$project:ci" "$work"

# Use the shipped Nginx configuration, startup script, image user and release
# marker. Only the Vue build output is replaced with small static CI fixtures.
mkdir -p "$work/console/assets"
cat > "$work/console/index.html" <<'HTML'
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Nebula CI fixture</title><link rel="stylesheet" href="/assets/ci-console.css"></head>
<body><div id="app">Nebula CI fixture</div><script type="module" src="/assets/ci-console.js"></script></body>
</html>
HTML
printf '%s\n' 'document.documentElement.dataset.console = "ci-fixture";' > "$work/console/assets/ci-console.js"
printf '%s\n' '#app { display: block; }' > "$work/console/assets/ci-console.css"
docker build --target nginx-base --build-arg NGINX_IMAGE=docker.io/library/nginx:1.29.1-alpine3.22 \
    --build-arg "VERSION=$NEBULA_VERSION" -t "$project:nginx-base" "$repo/frontend"
docker build -f - -t "$project:frontend" "$work/console" <<DOCKERFILE
FROM $project:nginx-base
COPY --chown=10001:10001 . /usr/share/nginx/html/
DOCKERFILE

: > "$work/empty.env"
export CI_NEBULA_DB_PASSWORD="$(openssl rand -hex 16)"
export CI_NEBULA_DATABASE_URL="postgres://nebula:${CI_NEBULA_DB_PASSWORD}@postgres:5432/nebula?sslmode=disable"
export CI_NEBULA_JWT_KEY="$(openssl rand -hex 32)"
export CI_NEBULA_AUTH_PEPPER="$(openssl rand -hex 32)"
export CI_NEBULA_API_PEPPER="$(openssl rand -hex 32)"
export CI_NEBULA_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export CI_NEBULA_TEACHER_PASSWORD="$(openssl rand -hex 16)"
teacher_email=teacher@example.test
notice_digest() {
    printf 'nebula-certificate-notice-v1:%s' "$(printf '%s' "$1" |
        LC_ALL=C tr '[:upper:]' '[:lower:]' | LC_ALL=C tr -d '[:space:]')" |
        openssl dgst -sha256 -r | cut -d ' ' -f 1
}
teacher_digest="$(notice_digest "$teacher_email")"
unknown_digest="$(notice_digest 'unregistered@example.test')"
request_nonce="$(openssl rand -hex 32)"
wrong_nonce="$(openssl rand -hex 32)"
export CI_NEBULA_NOTICE_REQUEST_SHA256="$(
    printf 'nebula-notice-request-v1:%s:%s:%s' "$NEBULA_VERSION" "$teacher_digest" "$request_nonce" |
        openssl dgst -sha256 -r | cut -d ' ' -f 1
)"
export CI_NEBULA_NOTICE_EXPIRES_AT="$(date -u -d '+1 hour' '+%Y-%m-%dT%H:%M:%SZ')"
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
  smtp-sink:
    image: ${CI_NEBULA_IMAGE}
    entrypoint: ["/app/ci-smtp-receiver"]
    networks: [data]
    read_only: true
    tmpfs: ["/tmp:size=1m,mode=1777"]
    healthcheck:
      test: ["CMD-SHELL", "printf 'QUIT\\r\\n' | nc -w 1 127.0.0.1 2525 | grep -q '^221 '"]
      interval: 2s
      timeout: 2s
      retries: 10
  migrate:
    image: ${CI_NEBULA_IMAGE}
    build: !reset null
    environment:
      DATABASE_URL: ${CI_NEBULA_DATABASE_URL}
  frontend:
    image: ${CI_NEBULA_FRONTEND_IMAGE}
    build: !reset null
    ports: !override
      - "127.0.0.1::8080"
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
      SMTP_MODE: smtp
      SMTP_HOST: smtp-sink
      SMTP_PORT: "2525"
      SMTP_USER: ""
      SMTP_PASS: ""
      SMTP_FROM: sender@example.test
      SMTP_TLS_MODE: none
      SMTP_TIMEOUT_SECONDS: "5"
      LAB_MAINTENANCE_TLS_ORIGIN: https://127.0.0.1
      LAB_MAINTENANCE_RELEASE: ${NEBULA_VERSION}
      LAB_CERTIFICATE_NOTICE_REQUEST_SHA256: ${CI_NEBULA_NOTICE_REQUEST_SHA256}
      LAB_CERTIFICATE_NOTICE_EXPIRES_AT: ${CI_NEBULA_NOTICE_EXPIRES_AT}
      HTTP_PROXY: ""
      HTTPS_PROXY: ""
      BEICHEN_CHANNELS_JSON: ""
      BEICHEN_WEB_EMAIL: ""
      BEICHEN_WEB_PASSWORD: ""
      BEICHEN_PANEL_TOKEN: ""
      BEICHEN_PANEL_GROUPS: ""
YAML
export CI_NEBULA_IMAGE="$project:ci"
export CI_NEBULA_FRONTEND_IMAGE="$project:frontend"

"${compose[@]}" up -d --wait --wait-timeout 90 postgres redis smtp-sink
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

check_anonymous_auth() {
    local url="$1" path="$2" status
    shift 2
    status="$("${request[@]}" --output "$work/response.json" --write-out '%{http_code}' \
        "$@" "$url$path")"
    if [[ "$status" != 401 ]] ||
        ! jq -e '.error.code == "AUTHENTICATION_REQUIRED"' "$work/response.json" >/dev/null; then
        echo "Expected $url$path to return 401 AUTHENTICATION_REQUIRED, got status $status" >&2
        return 1
    fi
}

check_static_response() {
    local url="$1" mime="$2" expected="$3" response
    shift 3
    response="$("${request[@]}" --output "$work/static-response" \
        --write-out '%{http_code} %{content_type}' "$@" "$url")"
    if [[ ! "$response" =~ ^200\ ($mime)([[:space:]]*\;.*)?$ ]] ||
        ! cmp -s "$expected" "$work/static-response"; then
        echo "Expected $url to return 200, MIME $mime and the contents of ${expected##*/}; got $response" >&2
        return 1
    fi
}

check_public_console() {
    local url="$1" path
    shift
    for path in /teacher/providers /student/api-keys /login; do
        check_static_response "$url$path" 'text/html' "$work/console/index.html" "$@"
    done
    check_static_response "$url/assets/ci-console.js" '(text|application)/javascript' \
        "$work/console/assets/ci-console.js" "$@"
    check_static_response "$url/assets/ci-console.css" 'text/css' "$work/console/assets/ci-console.css" "$@"
    check_static_response "$url/.well-known/nebula-release.txt" 'text/plain' "$work/expected-release" "$@"
    for path in /api/v1/teacher/providers /api/v1/student/api-keys; do
        check_anonymous_auth "$url" "$path" "$@"
    done
}

read_secure_auth_cookie() {
    local jar="$1" name="$2"
    if ! awk -F '\t' -v name="$name" '
        $1 == "#HttpOnly_127.0.0.1" && $3 == "/api/v1/auth" &&
            $4 == "TRUE" && $6 == name && $7 != "" { print $7; found = 1 }
        END { exit !found }
    ' "$jar"; then
        echo "Expected HTTPS to store $name with Secure, HttpOnly and the auth path" >&2
        return 1
    fi
}

check_https_teacher_auth() {
    # Keep the URL, Origin and certificate name aligned with the CI backend.
    local origin=https://127.0.0.1
    local -a https_request=("${request[@]}" --fail --cacert "$work/first.pem"
        --connect-to "127.0.0.1:443:$1" --header "Origin: $origin")
    local jar="$work/teacher-cookies" headers="$work/teacher-headers" response="$work/teacher-response.json"
    local initial_refresh rotated_refresh access_token

    jq -cn --arg email "$teacher_email" '{email: $email, password: env.CI_NEBULA_TEACHER_PASSWORD}' |
        "${https_request[@]}" --header 'Content-Type: application/json' --data-binary @- \
            --cookie-jar "$jar" --dump-header "$headers" --output "$response" "$origin/api/v1/auth/login" ||
        { echo "HTTPS teacher login failed" >&2; return 1; }
    jq -e --arg email "$teacher_email" '
        .data.user.email == $email and (.data.identities | index("teacher") != null)
    ' "$response" >/dev/null || { echo "HTTPS login did not offer the teacher identity" >&2; return 1; }
    if ! awk -v origin="$origin" '
        { sub(/\r$/, "") }
        tolower($1) == "access-control-allow-origin:" && $2 == origin { allowed = 1 }
        tolower($1) == "access-control-allow-credentials:" && $2 == "true" { credentials = 1 }
        END { exit !(allowed && credentials) }
    ' "$headers"; then
        echo "HTTPS login did not allow credentials for the fixture origin" >&2
        return 1
    fi
    read_secure_auth_cookie "$jar" nebula_login_challenge >/dev/null

    "${https_request[@]}" --header 'Content-Type: application/json' --data-binary '{"identity":"teacher"}' \
        --cookie "$jar" --cookie-jar "$jar" --output "$response" "$origin/api/v1/auth/session" ||
        { echo "HTTPS teacher session creation failed" >&2; return 1; }
    initial_refresh="$(read_secure_auth_cookie "$jar" nebula_refresh_token)"

    "${https_request[@]}" --request POST --cookie "$jar" --cookie-jar "$jar" \
        --output "$response" "$origin/api/v1/auth/refresh" ||
        { echo "HTTPS teacher refresh failed" >&2; return 1; }
    rotated_refresh="$(read_secure_auth_cookie "$jar" nebula_refresh_token)"
    [[ "$initial_refresh" != "$rotated_refresh" ]] ||
        { echo "HTTPS refresh did not rotate the refresh cookie" >&2; return 1; }
    access_token="$(jq -er '.data | select(.identity == "teacher") | .access_token | strings | select(length > 0)' "$response")" ||
        { echo "HTTPS refresh did not issue a teacher access token" >&2; return 1; }

    printf 'Authorization: Bearer %s\n' "$access_token" |
        "${https_request[@]}" --header @- --output "$response" "$origin/api/v1/me" ||
        { echo "HTTPS /me rejected the refreshed teacher session" >&2; return 1; }
    jq -e --arg email "$teacher_email" '
        .data.identity == "teacher" and .data.user.role == "teacher" and .data.user.email == $email
    ' "$response" >/dev/null || { echo "HTTPS /me did not return the fixture teacher" >&2; return 1; }

    printf 'Authorization: Bearer %s\n' "$access_token" |
        "${https_request[@]}" --header @- --output "$response" "$origin/api/v1/teacher/providers" ||
        { echo "HTTPS teacher providers rejected the refreshed teacher session" >&2; return 1; }
    jq -e '.data | type == "array"' "$response" >/dev/null ||
        { echo "HTTPS teacher providers did not return a list" >&2; return 1; }
}

notice_path='/.well-known/nebula-maintenance/certificate-notice'

post_notice() {
    local url="$1" digest="$2" nonce="$3" expected="$4" status
    shift 4
    status="$(printf '{"email_digest":"%s","request_nonce":"%s"}' "$digest" "$nonce" |
        "${request[@]}" "$@" -H 'Content-Type: application/json' \
            --data-binary @- --output "$work/notice-response" \
            --write-out '%{http_code}' "$url$notice_path")"
    [[ "$status" == "$expected" ]] ||
        { echo "Expected certificate notice HTTP status $expected, got $status" >&2; return 1; }
}

check_notice_response() {
    # Allow an empty response or a generic acknowledgment with no account data.
    if [[ -s "$work/notice-response" ]]; then
        jq -es 'length == 1 and (.[0] |
            . == {} or . == {"accepted":true} or . == {"status":"accepted"} or
            . == {"message":"accepted"})' "$work/notice-response" >/dev/null ||
            { echo "Expected an account-independent certificate notice acknowledgment" >&2; return 1; }
    fi
    cmp -s "$work/notice-first-response" "$work/notice-response" ||
        { echo "Certificate notice acknowledgments differ" >&2; return 1; }
}

capture_mail() {
    "${compose[@]}" exec -T smtp-sink sh -ec '
        for file in /tmp/smtp-pending-*.json; do
            if [ -f "$file" ]; then cat "$file"; fi
        done
    ' > "$work/mail.jsonl"
    mail_count="$(jq -s 'length' "$work/mail.jsonl")"
}

expect_quiet_mailbox() {
    local expected="$1" i
    for ((i = 0; i <= 10; i++)); do
        capture_mail
        [[ "$mail_count" == "$expected" ]] ||
            { echo "Expected $expected total SMTP deliveries, got $mail_count" >&2; return 1; }
        if (( i < 10 )); then sleep 1; fi
    done
}

start_backend
first_backend="$("${compose[@]}" ps -q backend)"
docker inspect --format '{{json .Mounts}}' "$first_backend" |
    jq -e --arg name "${project}_maintenance-tls" 'any(.[]; .Type == "volume" and
        .Name == $name and .Destination == "/app/maintenance-tls" and .RW)' >/dev/null
read_cert > "$work/first.pem"
check_anonymous_auth "http://$http_address" /api/v1/teacher/providers
check_anonymous_auth "https://$https_address" /api/v1/teacher/providers --cacert "$work/first.pem"

# Test the production-shaped frontend listener, where HTTP and HTTPS share a
# port. HTTPS reaches Go first, then fetches console files from frontend:8082.
"${compose[@]}" up -d --no-deps --no-build --wait --wait-timeout 45 frontend
public_address="$("${compose[@]}" port frontend 8080)"
printf '%s\n' "$NEBULA_VERSION" > "$work/expected-release"
"${compose[@]}" exec -T frontend wget -q -T 3 -O - http://frontend:8082/.well-known/nebula-release.txt \
    > "$work/internal-release"
cmp "$work/expected-release" "$work/internal-release"
check_public_console "https://$public_address" --cacert "$work/first.pem"
check_public_console "http://$public_address"
check_https_teacher_auth "$public_address"

recipient_path='/.well-known/nebula-beichen-recipient'
read_recipient() {
    "${request[@]}" --fail --cacert "$work/first.pem" "https://$1$recipient_path" |
        jq -ce 'select(keys == ["algorithm","public_key"] and .algorithm == "nacl-sealed-box-v1"
            and (.public_key | test("^[A-Za-z0-9+/]{43}=$")))'
}
read_recipient "$public_address" > "$work/first-recipient"
"${compose[@]}" exec -T backend sh -ec '
    test "$(stat -c "%a" "$LAB_BEICHEN_SEAL_DIR/recipient.key")" = 600
    test -s "$LAB_BEICHEN_SEAL_DIR/recipient.key"
'

post_notice "http://$http_address" "$teacher_digest" "$request_nonce" 404
post_notice "https://$https_address" "$teacher_digest" "$wrong_nonce" 202 --cacert "$work/first.pem"
cp "$work/notice-response" "$work/notice-first-response"
check_notice_response
post_notice "https://$https_address" "$unknown_digest" "$request_nonce" 202 --cacert "$work/first.pem"
check_notice_response
expect_quiet_mailbox 0

# Hash the leaf presented by the listener, using its DER encoding.
fingerprint="$(timeout 5 openssl s_client -connect "$https_address" -CAfile "$work/first.pem" \
    -verify_ip 127.0.0.1 -verify_return_error </dev/null 2>/dev/null |
    openssl x509 -outform DER | openssl dgst -sha256 -r | cut -d ' ' -f 1)"
post_notice "https://$https_address" "$teacher_digest" "$request_nonce" 202 --cacert "$work/first.pem"
check_notice_response
post_notice "https://$https_address" "$teacher_digest" "$request_nonce" 202 --cacert "$work/first.pem"
check_notice_response
for ((i = 0; i < 30; i++)); do
    capture_mail
    if (( mail_count > 0 )); then break; fi
    sleep 1
done
expect_quiet_mailbox 1

# The only body fields are the release, origin and leaf fingerprint. Permit
# descriptive field labels and either compact or colon-separated hex.
jq -es --arg recipient "$teacher_email" --arg release "$NEBULA_VERSION" \
    --arg origin https://127.0.0.1 --arg fingerprint "$fingerprint" '
    def trim: gsub("^\\s+|\\s+$"; "");
    def field($label): sub("^(?i:" + $label + ")\\s*[:：=]\\s*"; "");
    length == 1 and (.[0] |
        .recipients == [$recipient] and
        (.message | split("\n\n") | .[0] as $headers |
            ($headers | split("\n") | map(select(test("^(?i:to):"))) |
                map(sub("^(?i:to):"; "") | trim)) == [$recipient] and
            (.[1:] | join("\n\n") | split("\n") | map(trim | select(length > 0) |
                field("release( version)?|version|origin|https origin|server origin|school https|url|(leaf )?(certificate )?(sha-?256( fingerprint)?|fingerprint( \\(sha-?256\\))?)|版本|发布版本|证书指纹(（SHA-256）)?") |
                if test("^(?:[[:xdigit:]]{2}:){31}[[:xdigit:]]{2}$|^[[:xdigit:]]{64}$")
                then ascii_downcase | gsub(":"; "") else . end) | sort) ==
                ([$release, $origin, $fingerprint] | sort))
    )' "$work/mail.jsonl" >/dev/null ||
    { echo "Certificate notice SMTP recipient or body did not match" >&2; exit 1; }

start_backend
second_backend="$("${compose[@]}" ps -q backend)"
[[ -n "$first_backend" && -n "$second_backend" && "$first_backend" != "$second_backend" ]]
read_cert > "$work/second.pem"
cmp "$work/first.pem" "$work/second.pem"
read_recipient "$https_address" > "$work/second-recipient"
cmp "$work/first-recipient" "$work/second-recipient"
check_anonymous_auth "http://$http_address" /api/v1/teacher/providers
check_anonymous_auth "https://$https_address" /api/v1/teacher/providers --cacert "$work/first.pem"
post_notice "https://$https_address" "$teacher_digest" "$request_nonce" 202 --cacert "$work/first.pem"
check_notice_response
expect_quiet_mailbox 1
echo "Lab backend passed shared-port console checks, HTTPS teacher login and refresh, certificate creation and reuse, anonymous authentication, and certificate notice delivery"
