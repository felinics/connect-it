# syntax=docker/dockerfile:1
# Build context must be the repository root:
#   docker build -t connect-it:dev .

# Static web assets are architecture-independent, so build them natively once.
FROM --platform=$BUILDPLATFORM node:22 AS web-builder
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable && corepack prepare pnpm@10.29.2 --activate
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY packages/ui packages/ui
COPY packages/sdk packages/sdk
COPY packages/web packages/web
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
RUN pnpm --dir packages/web run build

# Keep the compiler native and cross-compile the static binary for each target.
FROM --platform=$BUILDPLATFORM golang:1.25 AS go-builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION
WORKDIR /src
# All four modules must be present because their replace directives use ../ paths.
COPY packages/core packages/core
COPY packages/connectors packages/connectors
COPY packages/service packages/service
COPY packages/api packages/api
COPY version.json version.json
WORKDIR /src/packages/api
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    version="${VERSION:-$(awk -F '\"' '/\"version\"/ { print $4; exit }' /src/version.json)}" \
    && test -n "$version" \
    && GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
      -ldflags="-s -w -X github.com/memohai/connect-it/packages/core/buildinfo.Version=$version" \
      -o /out/connect-it ./cmd/connect-it

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 connectit \
    && adduser -D -H -u 10001 -G connectit connectit
COPY --from=go-builder /out/connect-it /usr/local/bin/connect-it
COPY --from=web-builder /src/packages/web/dist /usr/share/connect-it/web
USER connectit:connectit
ENV LISTEN_ADDR=:8421 \
    CONNECT_IT_WEB_DIR=/usr/share/connect-it/web
EXPOSE 8421
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=5 \
    CMD wget -qO /dev/null http://127.0.0.1:8421/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/connect-it"]
