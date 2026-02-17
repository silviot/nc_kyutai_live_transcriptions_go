# Multi-stage build: builder → runtime
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Install build dependencies for CGo (Opus codec)
RUN apk add --no-cache gcc musl-dev pkgconf opus-dev opusfile-dev libogg-dev

# Copy source
COPY . .

# Build binary with CGo enabled for Opus support
RUN CGO_ENABLED=1 go build -o transcribe-service ./cmd/transcribe-service

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install runtime deps (TLS certificates + Opus shared library)
RUN apk add --no-cache ca-certificates opus opusfile

# Copy binary from builder
COPY --from=builder /build/transcribe-service .

# Health check (uses APP_PORT env var, defaults to 23000)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:${APP_PORT:-23000}/healthz || exit 1

# Expose port
EXPOSE 23000

# Run service
ENTRYPOINT ["./transcribe-service"]
