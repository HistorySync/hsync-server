# HistorySync Cloud Server - Multi-stage Docker Build
# Optimized for small image size (~15 MB) and fast builds.

# ── Stage 1: Build ───────────────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies layer
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" \
    -o /hsync-server \
    ./cmd/hsync-server/

# ── Stage 2: Runtime ─────────────────────────────────────────
FROM alpine:3.20

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
    CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["hsync-server"]
