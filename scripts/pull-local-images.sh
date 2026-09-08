#!/usr/bin/env bash
# Pre-pull and retag base images from faster mirrors for local Linux deployment.
set -Eeuo pipefail

pull_and_tag() {
  local src="$1" dst="$2"
  if docker image inspect "$dst" >/dev/null 2>&1; then
    printf 'Already have %s\n' "$dst"
    return 0
  fi
  printf 'Pulling %s ...\n' "$src"
  docker pull "$src"
  docker tag "$src" "$dst"
  printf 'Tagged %s -> %s\n' "$src" "$dst"
}

pull_and_tag docker.1ms.run/postgres:17.5 \
  docker.m.daocloud.io/library/postgres:17.11-alpine3.24

if ! docker image inspect docker.m.daocloud.io/library/redis:8.2.8-alpine3.22 >/dev/null 2>&1; then
  pull_and_tag docker.1ms.run/library/redis:8.2.8-alpine3.22 \
    docker.m.daocloud.io/library/redis:8.2.8-alpine3.22 || \
  pull_and_tag redis:7-alpine \
    docker.m.daocloud.io/library/redis:8.2.8-alpine3.22
fi

pull_and_tag dockerproxy.net/library/golang:1.26.6-alpine3.24 \
  docker.m.daocloud.io/library/golang:1.26.6-alpine3.24

pull_and_tag dockerproxy.net/library/alpine:3.22.5 \
  docker.m.daocloud.io/library/alpine:3.22.5

pull_and_tag dockerproxy.net/library/node:22.17.1-alpine3.22 \
  dockerproxy.net/library/node:22.17.1-alpine3.22

pull_and_tag dockerproxy.net/library/nginx:1.29.1-alpine3.22 \
  dockerproxy.net/library/nginx:1.29.1-alpine3.22

printf 'Base images ready.\n'
