#!/bin/sh
set -eu

source_config=/etc/nginx/conf.d/default.conf
generated_config=/tmp/lab-maintenance-http.conf

# Guard the two substitutions so changes to the shared site fail visibly.
if [ "$(grep -cE '^[[:space:]]+listen 8080;[[:space:]]*$' "$source_config")" -ne 1 ] ||
   [ "$(grep -cE '^[[:space:]]+proxy_pass http://backend:8080;[[:space:]]*$' "$source_config")" -ne 2 ]; then
    echo "Lab frontend configuration no longer matches the shared Nginx site" >&2
    exit 1
fi

umask 077
sed -e 's|^  *listen 8080;[[:space:]]*$|  listen 127.0.0.1:8081 proxy_protocol;\n  set_real_ip_from 127.0.0.1;\n  real_ip_header proxy_protocol;|' \
    -e 's|proxy_pass http://backend:8080;|proxy_pass http://lab_http_backend;|' \
    "$source_config" > "$generated_config"

exec "$@"
