# syntax=docker/dockerfile:1
# Admin UI image: builds the Vue app and serves it via nginx, which also
# reverse-proxies API paths to the connect-it server container.
# Build context MUST be the repository root (packages/ui submodule checked out):
#   docker build -f docker/web.Dockerfile -t connect-it-web:dev .

# ---------- Stage 1: build the Vue admin UI ----------
# Static web assets are architecture-independent, so build them natively once.
FROM --platform=$BUILDPLATFORM node:26 AS web-builder
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
# corepack prepare needs an exact version; keep in sync with pnpm-lock.yaml.
RUN corepack enable && corepack prepare pnpm@10.29.2 --activate
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY packages/ui packages/ui
COPY packages/sdk packages/sdk
COPY packages/web packages/web
RUN pnpm install --frozen-lockfile
RUN pnpm --dir packages/web run build

# ---------- Stage 2: nginx runtime ----------
FROM nginx:1.30-alpine
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=web-builder /src/packages/web/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO /dev/null http://127.0.0.1:8080/ || exit 1
