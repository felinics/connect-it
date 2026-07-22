# syntax=docker/dockerfile:1
# Build context MUST be the repository root with the packages/ui submodule
# checked out:  docker build -f docker/Dockerfile -t connect-it:dev .

# ---------- Stage 1: build the Vue admin UI ----------
FROM node:22 AS web-builder
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

# ---------- Stage 2: build the Go binary ----------
FROM golang:1.25 AS go-builder
WORKDIR /src
# All four modules must be present: go.mod replace directives use ../ paths.
COPY packages/core packages/core
COPY packages/connectors packages/connectors
COPY packages/service packages/service
COPY packages/api packages/api
# Same destination the `mise run build-web` task uses; served via go:embed.
COPY --from=web-builder /src/packages/web/dist /src/packages/api/webdist
WORKDIR /src/packages/api
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/connect-it ./cmd/connect-it

# ---------- Stage 3: runtime ----------
# alpine (not distroless/static): busybox wget keeps the HEALTHCHECK inside
# the image so bare `docker run` gets health status too, and a shell remains
# available for debugging this internal tool. Mitigations: non-root user,
# static binary, minimal package set.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 connectit \
    && adduser -D -H -u 10001 -G connectit connectit
COPY --from=go-builder /out/connect-it /usr/local/bin/connect-it
USER connectit:connectit
# The binary defaults to :8080 anyway; set it explicitly so EXPOSE and
# HEALTHCHECK below stay truthful if the default ever changes.
ENV LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=5 \
    CMD wget -qO /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/connect-it"]
