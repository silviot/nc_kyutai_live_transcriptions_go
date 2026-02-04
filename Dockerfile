# Multi-stage build: builder → runtime
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy source
COPY . .

# Build binary
RUN go build -o transcribe-service ./cmd/transcribe-service

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install minimal deps (TLS certificates for HTTPS)
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /build/transcribe-service .

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/healthz || exit 1

# Expose port
EXPOSE 8080

# Run service
ENTRYPOINT ["./transcribe-service"]
