.PHONY: build test lint run clean docker docker-run live-test help

# Build configuration
BINARY := transcribe-service
GO := go
DOCKER_IMAGE := nc-transcribe-go

# Default target
help:
	@echo "Kyutai Live Transcription Service (Go)"
	@echo ""
	@echo "Usage:"
	@echo "  make build       - Build the binary"
	@echo "  make test        - Run all tests"
	@echo "  make test-unit   - Run unit tests only"
	@echo "  make test-int    - Run integration tests"
	@echo "  make lint        - Run linter"
	@echo "  make run         - Run locally (requires .envrc)"
	@echo "  make docker      - Build Docker image"
	@echo "  make docker-run  - Run in Docker"
	@echo "  make live-test   - Test against live Nextcloud"
	@echo "  make clean       - Clean build artifacts"
	@echo ""

# Build binary
build:
	@echo "Building $(BINARY)..."
	$(GO) build -o $(BINARY) ./cmd/transcribe-service
	@echo "Built: ./$(BINARY)"

# Run all tests
test:
	@echo "Running all tests..."
	$(GO) test -v -race ./...

# Run unit tests only (fast)
test-unit:
	@echo "Running unit tests..."
	$(GO) test -v ./pkg/audio ./pkg/hpb

# Run integration tests
test-int:
	@echo "Running integration tests..."
	$(GO) test -v ./test

# Run with race detector and coverage
test-cover:
	@echo "Running tests with coverage..."
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Lint
lint:
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run ./...

# Run locally
run: build
	@echo "Starting service..."
	@echo "Using HPB URL: $${LT_HPB_URL:-not set}"
	./$(BINARY) -port $${PORT:-8080}

# Build Docker image
docker:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):latest .
	@echo "Built: $(DOCKER_IMAGE):latest"

# Run in Docker
docker-run:
	@echo "Running in Docker..."
	docker run --rm -it \
		-p 8080:8080 \
		-e LT_HPB_URL="$${LT_HPB_URL}" \
		-e LT_INTERNAL_SECRET="$${LT_INTERNAL_SECRET}" \
		-e STT_STREAM_URL="$${STT_STREAM_URL}" \
		-e MODAL_WORKSPACE="$${MODAL_WORKSPACE}" \
		-e MODAL_KEY="$${MODAL_KEY}" \
		-e MODAL_SECRET="$${MODAL_SECRET}" \
		$(DOCKER_IMAGE):latest

# Live test against real Nextcloud
live-test: build
	@echo "Running live test against Nextcloud..."
	./scripts/test-live.sh

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(BINARY)
	rm -f coverage.out coverage.html
	$(GO) clean -cache

# Memory profiling
profile:
	@echo "Running with memory profiling..."
	$(GO) run -race ./cmd/transcribe-service -port 8080 &
	@echo "Visit http://localhost:8080/debug/pprof/"
