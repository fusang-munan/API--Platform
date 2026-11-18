#!/usr/bin/env bash
set -Eeuo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image=${NGINX_IMAGE:-docker.io/library/nginx:1.29.1-alpine3.22}
version="$(tr -d '\r' < "$repo/VERSION")"
prefix="nebula-lab-smoke-$$-$RANDOM"
frontend_image="$prefix:runtime"
network="$prefix"
frontend="$prefix-front"
backend_a="$prefix-backend-a"
backend_b="$prefix-backend-b"

entrypoint=(/bin/sh /etc/nginx/lab-maintenance/start.sh)
nginx_command=(nginx -c /etc/nginx/lab-maintenance/nginx.conf -g 'daemon off;')
frontend_options=(
    --read-only --cap-drop ALL --security-opt no-new-privileges:true
    --tmpfs /tmp:size=16m,mode=1777 --tmpfs /var/cache/nginx:size=16m,mode=1777
    --tmpfs /var/run:size=4m,mode=1777 --init
)

for tool in docker curl openssl python3; do
    command -v "$tool" >/dev/null || { echo "Missing required tool: $tool" >&2; exit 1; }
done

endpoint="${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}"
case "$endpoint" in
    unix://*|npipe://*) ;;
    *) echo "The smoke test requires a local Docker endpoint" >&2; exit 1 ;;
esac
if [[ "$(docker info --format '{{.OSType}}')" != linux ]]; then
    echo "The smoke test requires a Linux container engine" >&2
    exit 1
fi

work="$(mktemp -d)"
cleanup() {
    local status=$?
    if (( status != 0 )); then
        for container in "$frontend" "$backend_a" "$backend_b"; do
            if docker container inspect --format '{{.Name}} {{.State.Status}} exit={{.State.ExitCode}} {{.State.Error}}' "$container" 2>/dev/null; then
                docker logs "$container" || true
            fi
        done
    fi
    docker rm -f "$frontend" "$backend_a" "$backend_b" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    docker image rm "$frontend_image" >/dev/null 2>&1 || true
    rm -r -- "$work"
}
trap cleanup EXIT

docker_work="$work"
case "$OSTYPE" in
    msys*|cygwin*)
        docker_work="$(cd "$work" && pwd -W)"
        export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
        ;;
esac

# Match compose-local.sh's file order without loading .env or starting services.
: > "$work/empty.env"
(
    cd "$repo"
    NEBULA_VERSION="$version" docker compose --env-file "$docker_work/empty.env" \
        -f compose.yaml -f compose.local.yaml config --format json
) | python3 -c '
import json
import sys

frontend = json.load(sys.stdin)["services"]["frontend"]
checks = {
    "frontend must have no bind mounts": not any(
        volume["type"] == "bind" for volume in frontend.get("volumes", [])
    ),
    "frontend must use the image user": not frontend.get("user"),
    "frontend entrypoint differs from the smoke test": frontend.get("entrypoint") == sys.argv[1:3],
    "frontend command differs from the smoke test": frontend.get("command") == sys.argv[3:],
}
for message, passed in checks.items():
    if not passed:
        sys.exit(message)
' "${entrypoint[@]}" "${nginx_command[@]}"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=127.0.0.1 \
    -addext subjectAltName=IP:127.0.0.1 -keyout "$work/key.pem" -out "$work/cert.pem" 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=127.0.0.1 \
    -addext subjectAltName=IP:127.0.0.1 -keyout "$work/wrong-key.pem" -out "$work/wrong-cert.pem" 2>/dev/null

run_backend() {
    local name=$1 generation=$2
    cat > "$work/$generation.conf" <<EOF
server {
    listen 8080;
    location = /api/smoke { return 200 "http-$generation"; }
}
server {
    listen 8443 ssl;
    ssl_certificate /fixture-cert.pem;
    ssl_certificate_key /fixture-key.pem;
    location = /api/smoke { return 200 "tls-$generation"; }
}
EOF
    docker run -d --name "$name" --network "$network" --network-alias backend \
        --mount "type=bind,src=$docker_work/$generation.conf,dst=/etc/nginx/conf.d/default.conf,readonly" \
        --mount "type=bind,src=$docker_work/cert.pem,dst=/fixture-cert.pem,readonly" \
        --mount "type=bind,src=$docker_work/key.pem,dst=/fixture-key.pem,readonly" \
        "$image" >/dev/null
}

(
    cd "$repo/frontend"
    # Build the shipped Vue and Nginx image using the public origins for the CI runner.
    sed -e 's/\r$//' -e 's|^FROM dockerproxy.net/library/node:|FROM docker.io/library/node:|' \
        -e 's|^RUN npm config set registry https://registry.npmmirror.com$|RUN npm config set registry https://registry.npmjs.org|' \
        Dockerfile | docker build -f - --target runtime --build-arg "NGINX_IMAGE=$image" \
            --build-arg "VERSION=$version" --tag "$frontend_image" .
)
if [[ "$(docker image inspect --format '{{.Config.User}}' "$frontend_image")" != 10001:10001 ]]; then
    echo "Expected the frontend image to run as 10001:10001" >&2
    exit 1
fi

if [[ "${CHECK_LEGACY_BIND:-0}" == 1 ]]; then
    # Model the old bind mount with files readable only by the host owner.
    (umask 022; mkdir "$work/legacy")
    (umask 077; for file in start.sh nginx.conf; do
        cat "$repo/frontend/lab-maintenance/$file" > "$work/legacy/$file"
    done)
    status=0
    docker run --rm --network none "${frontend_options[@]}" \
        --mount "type=bind,src=$docker_work/legacy,dst=/etc/nginx/lab-maintenance,readonly" \
        --entrypoint "${entrypoint[0]}" "$frontend_image" "${entrypoint[1]}" \
        "${nginx_command[@]}" >"$work/legacy.log" 2>&1 || status=$?
    if (( status == 0 )) ||
        ! grep -Eiq '/etc/nginx/lab-maintenance/start\.sh.*permission denied' "$work/legacy.log"; then
        cat "$work/legacy.log" >&2
        echo "Expected the legacy bind mount to deny access to start.sh" >&2
        exit 1
    fi
    echo "Legacy bind mount denied access to the owner-only fixture"
fi

docker network create "$network" >/dev/null
docker run -d --name "$frontend" --network "$network" --publish 127.0.0.1::8080 \
    "${frontend_options[@]}" --entrypoint "${entrypoint[0]}" "$frontend_image" \
    "${entrypoint[1]}" "${nginx_command[@]}" >/dev/null
address="$(docker port "$frontend" 8080/tcp)"

request() {
    curl --noproxy '*' --silent --show-error --fail --connect-timeout 2 --max-time 4 "$@"
}

wait_for_body() {
    local expected=$1 body attempt
    shift
    for (( attempt=0; attempt<30; attempt++ )); do
        if body="$(request "$@" 2>/dev/null)" && [[ "$body" == "$expected" ]]; then
            return
        fi
        sleep 1
    done
    echo "Timed out waiting for $expected" >&2
    return 1
}

# The school frontend probe must pass even before there is a backend DNS record.
for (( attempt=0; attempt<15; attempt++ )); do
    if docker exec "$frontend" wget -q -T 2 -O /dev/null http://127.0.0.1:8080/; then
        break
    fi
    sleep 1
done
docker exec "$frontend" wget -q -T 2 -O /dev/null http://127.0.0.1:8080/

request "http://$address/" > "$work/homepage"
grep -Fq '<div id="app">' "$work/homepage"
grep -Fq 'src="/assets/' "$work/homepage"
printf '%s\n' "$version" > "$work/expected-release"
request "http://$address/.well-known/nebula-release.txt" > "$work/actual-release"
if ! cmp -s "$work/expected-release" "$work/actual-release"; then
    echo "Expected the anonymous HTTP release file to contain exactly $version and a newline" >&2
    exit 1
fi

http_url="http://$address/api/smoke"
tls_url="https://$address/api/smoke"
pin="sha256//$(openssl x509 -in "$work/cert.pem" -pubkey -noout |
    openssl pkey -pubin -outform DER | openssl dgst -sha256 -binary | openssl base64 -A)"

run_backend "$backend_a" first
wait_for_body http-first "$http_url"
wait_for_body tls-first --cacert "$work/cert.pem" --pinnedpubkey "$pin" "$tls_url"

status=0
request --cacert "$work/wrong-cert.pem" "$tls_url" >/dev/null 2>"$work/cert-error" || status=$?
if [[ "$status" != 60 ]]; then
    cat "$work/cert-error" >&2
    echo "Expected curl certificate rejection (60), got $status" >&2
    exit 1
fi

# Allocate the replacement while the original still owns its address.
run_backend "$backend_b" second
docker rm -f "$backend_a" >/dev/null
wait_for_body http-second "$http_url"
wait_for_body tls-second --cacert "$work/cert.pem" --pinnedpubkey "$pin" "$tls_url"
echo "School port smoke test passed"
