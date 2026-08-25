# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage: runs natively on the build machine (a macOS arm64 laptop or a
# CI runner) and cross compiles a static binary for the Raspberry Pi.
# CGO is disabled, so no C toolchain is involved at any point.
# ---------------------------------------------------------------------------
ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.22
# Target architecture of the deploy image. Raspbian 64 bit is linux/arm64.
ARG TARGETARCH=arm64

FROM --platform=${BUILDPLATFORM} golang:${GO_VERSION}-alpine AS builder

ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api

# ---------------------------------------------------------------------------
# Runtime stage: alpine instead of scratch/distroless because having a shell
# on the Pi is worth the few extra megabytes when something goes wrong.
# ---------------------------------------------------------------------------
FROM --platform=linux/${TARGETARCH} alpine:${ALPINE_VERSION} AS runtime

# ca-certificates for outbound TLS, tzdata so timestamps are not stuck in UTC
# when the app is configured otherwise.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S app \
    && adduser -u 10001 -S -G app -H -s /sbin/nologin app

COPY --from=builder /out/api /usr/local/bin/api

USER app:app

ENV HTTP_HOST=0.0.0.0 \
    HTTP_PORT=8080

EXPOSE 8080

# Reuses the API's own liveness endpoint, which also pings MariaDB.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget --quiet --spider "http://127.0.0.1:${HTTP_PORT}/health" || exit 1

ENTRYPOINT ["/usr/local/bin/api"]
