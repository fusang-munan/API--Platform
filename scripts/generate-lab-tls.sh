#!/usr/bin/env bash
# Self-signed TLS for local Compose + FRP (https://106.55.103.208:25555).
set -Eeuo pipefail
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tls_dir="${repository_root}/lab-tls"
cn="${LAB_TLS_CN:-106.55.103.208}"

mkdir -p "$tls_dir"
if [[ -f "$tls_dir/cert.pem" && -f "$tls_dir/key.pem" && "${FORCE_LAB_TLS_REGEN:-0}" != "1" ]]; then
  printf 'lab-tls already exists (%s/cert.pem). Set FORCE_LAB_TLS_REGEN=1 to recreate.\n' "$tls_dir"
  exit 0
fi

openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$tls_dir/key.pem" \
  -out "$tls_dir/cert.pem" \
  -days 825 \
  -subj "/CN=${cn}" \
  -addext "subjectAltName=IP:${cn},IP:127.0.0.1,DNS:localhost"

# nginx in frontend container runs as UID 10001; lab bind-mount must be world-readable.
chmod 644 "$tls_dir/cert.pem" "$tls_dir/key.pem"
printf 'Wrote %s/cert.pem and key.pem (CN/SAN includes %s).\n' "$tls_dir" "$cn"
printf 'Browser will warn until you trust the cert — expected for lab self-signed.\n'
