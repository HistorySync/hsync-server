# HistorySync Cloud Server - Multi-stage Docker Build
# Optimized for small image size (~15 MB) and fast builds.

# ── Stage 1: Build ───────────────────────────────────────────
FROM golang:1.25-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG BUILD_PKG=github.com/historysync/hsync-server/pkg/buildinfo

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies layer
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X ${BUILD_PKG}.version=${VERSION} -X ${BUILD_PKG}.commit=${COMMIT} -X ${BUILD_PKG}.buildTime=${BUILD_TIME} -X ${BUILD_PKG}.edition=community" \
    -o /hsync-server \
    ./cmd/hsync-server/

# ── Stage 2: Runtime ─────────────────────────────────────────
FROM alpine:3.20

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

LABEL org.opencontainers.image.title="HistorySync Cloud Server" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"

RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    && adduser -D -H -h /app hsync

COPY --from=builder /hsync-server /usr/local/bin/hsync-server
COPY migrations/ /app/migrations/

USER hsync
WORKDIR /app

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD curl -f http://localhost:8080/readyz || exit 1

ENTRYPOINT ["hsync-server"]
