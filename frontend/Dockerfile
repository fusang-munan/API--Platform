ARG NGINX_IMAGE=dockerproxy.net/library/nginx:1.29.1-alpine3.22
FROM dockerproxy.net/library/node:22.17.1-alpine3.22 AS builder
WORKDIR /src
RUN npm config set registry https://registry.npmmirror.com
COPY package.json package-lock.json* ./
RUN npm install --ignore-scripts
COPY . .
RUN npm run build

FROM ${NGINX_IMAGE} AS nginx-base
RUN addgroup -S -g 10001 nebula && adduser -S -D -H -u 10001 -G nebula nebula && \
    chown -R 10001:10001 /usr/share/nginx/html /var/cache/nginx /var/log/nginx /etc/nginx
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY lab-maintenance/start.sh lab-maintenance/nginx.conf /etc/nginx/lab-maintenance/
RUN chmod 0755 /etc/nginx/lab-maintenance && \
    chmod 0444 /etc/nginx/conf.d/default.conf /etc/nginx/lab-maintenance/start.sh /etc/nginx/lab-maintenance/nginx.conf
ARG VERSION
RUN if [ -n "$VERSION" ]; then \
        mkdir -p /usr/share/nginx/html/.well-known && \
        printf '%s\n' "$VERSION" > /usr/share/nginx/html/.well-known/nebula-release.txt; \
    fi
USER 10001:10001
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]

FROM nginx-base AS runtime
COPY --from=builder --chown=10001:10001 /src/dist/ /usr/share/nginx/html/
