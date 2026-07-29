# syntax=docker/dockerfile:1
# Go API server image. Build context MUST be the repository root:
#   docker build -f docker/server.Dockerfile -t connect-it-server:dev .

# ---------- Stage 1: build the Go binary ----------
# Keep the compiler native and cross-compile the static binary for each target.
FROM --platform=$BUILDPLATFORM golang:1.26 AS go-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# All four modules must be present: go.mod replace directives use ../ paths.
COPY packages/core packages/core
COPY packages/connectors packages/connectors
COPY packages/service packages/service
COPY packages/api packages/api
WORKDIR /src/packages/api
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/connect-it ./cmd/connect-it

# ---------- Stage 2: runtime ----------
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
