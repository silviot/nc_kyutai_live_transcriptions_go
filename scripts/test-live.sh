#!/usr/bin/env bash
set -e

# Live testing script for Kyutai Transcription Service
# Tests against a real Nextcloud Talk instance

echo "==================================="
echo "Live Transcription Service Test"
echo "==================================="
echo ""

# Check required environment variables
check_env() {
    local var_name=$1
    local var_value="${!var_name}"

    if [ -z "$var_value" ]; then
        echo "ERROR: $var_name is not set"
        echo ""
        echo "Set required variables by sourcing the Python project's .envrc:"
        echo "  source ../nc_kyutai_live_transcriptions/.envrc"
        echo ""
        echo "Or set them manually:"
        echo "  export LT_HPB_URL=wss://\$NEXTCLOUD_HOST/standalone-signaling/spreed"
        echo "  export LT_INTERNAL_SECRET=<your-secret>"
        echo "  export MODAL_WORKSPACE=<your-workspace>"
        echo "  export MODAL_KEY=<your-key>"
        echo "  export MODAL_SECRET=<your-secret>"
        exit 1
    fi
    echo "  $var_name: ${var_value:0:20}..."
}

echo "Checking environment..."
check_env "LT_HPB_URL"
check_env "LT_INTERNAL_SECRET"
check_env "MODAL_WORKSPACE"
check_env "MODAL_KEY"
check_env "MODAL_SECRET"
echo ""

# Configuration
ROOM_TOKEN="${TALK_ROOM_TOKEN:?Set TALK_ROOM_TOKEN to your test room token}"
SERVICE_PORT="${PORT:-8080}"
NEXTCLOUD_URL="${NEXTCLOUD_URL:?Set NEXTCLOUD_URL to your Nextcloud instance URL}"

echo "Configuration:"
echo "  Room Token: $ROOM_TOKEN"
echo "  Nextcloud URL: $NEXTCLOUD_URL"
echo "  Service Port: $SERVICE_PORT"
echo ""

# Build if needed
if [ ! -f "./transcribe-service" ]; then
    echo "Building service..."
    go build -o transcribe-service ./cmd/transcribe-service
fi

# Test 1: Health check (no server needed)
echo "==================================="
echo "Test 1: Build Verification"
echo "==================================="
./transcribe-service --help > /dev/null 2>&1 && echo "PASS: Binary runs" || echo "FAIL: Binary broken"
echo ""

# Test 2: Start service and check health
echo "==================================="
echo "Test 2: Service Startup"
echo "==================================="
echo "Starting service on port $SERVICE_PORT..."

# Start service in background
./transcribe-service -port $SERVICE_PORT &
SERVICE_PID=$!
sleep 2

# Check if still running
if kill -0 $SERVICE_PID 2>/dev/null; then
    echo "PASS: Service started (PID: $SERVICE_PID)"
else
    echo "FAIL: Service crashed on startup"
    exit 1
fi

# Test health endpoint
echo "Checking health endpoint..."
HEALTH_RESPONSE=$(curl -s http://localhost:$SERVICE_PORT/healthz || echo "FAIL")
if echo "$HEALTH_RESPONSE" | grep -q "status"; then
    echo "PASS: Health endpoint responds"
    echo "  Response: $HEALTH_RESPONSE"
else
    echo "FAIL: Health endpoint not responding"
    kill $SERVICE_PID 2>/dev/null || true
    exit 1
fi
echo ""

# Test 3: HPB Connection Test
echo "==================================="
echo "Test 3: HPB Connection"
echo "==================================="
echo "Attempting to join room $ROOM_TOKEN via HPB..."

# Make a transcribe request
TRANSCRIBE_RESPONSE=$(curl -s -X POST http://localhost:$SERVICE_PORT/api/v1/call/transcribe \
    -H "Content-Type: application/json" \
    -d "{\"roomToken\": \"$ROOM_TOKEN\", \"languageId\": \"en\"}" || echo "FAIL")

echo "Response: $TRANSCRIBE_RESPONSE"

if echo "$TRANSCRIBE_RESPONSE" | grep -q "error"; then
    echo "WARNING: Transcribe request returned error (this may be expected if no speakers in room)"
else
    echo "PASS: Transcribe request accepted"
fi
echo ""

# Test 4: Check room status
echo "==================================="
echo "Test 4: Room Status"
echo "==================================="
STATUS_RESPONSE=$(curl -s http://localhost:$SERVICE_PORT/healthz)
echo "Service status: $STATUS_RESPONSE"
echo ""

# Cleanup
echo "==================================="
echo "Cleanup"
echo "==================================="
echo "Stopping service (PID: $SERVICE_PID)..."
kill $SERVICE_PID 2>/dev/null || true
wait $SERVICE_PID 2>/dev/null || true
echo "Done"
echo ""

# Summary
echo "==================================="
echo "Test Summary"
echo "==================================="
echo "All basic tests completed."
echo ""
echo "For full E2E testing:"
echo "1. Open the test room in a browser:"
echo "   $NEXTCLOUD_URL/call/$ROOM_TOKEN"
echo ""
echo "2. Start the service:"
echo "   make run"
echo ""
echo "3. Enable transcription in the room (via Talk UI)"
echo ""
echo "4. Speak into the microphone and verify transcripts appear"
echo ""
echo "For Python comparison testing:"
echo "   cd ../nc_kyutai_live_transcriptions"
echo "   source .envrc"
echo "   uv run python tools/roundtrip_modal.py --room-url $NEXTCLOUD_URL/call/$ROOM_TOKEN"
