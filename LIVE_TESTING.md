# Live Testing Guide

This guide explains how to test the Go transcription service against a real Nextcloud Talk instance.

## Test Environment

- **Nextcloud Instance**: https://cloud.codemyriad.io
- **Test Room**: https://cloud.codemyriad.io/call/erwcr27x
- **HPB URL**: wss://cloud.codemyriad.io/standalone-signaling/spreed
- **Installation**: Nextcloud AIO (All-in-One)

## Prerequisites

### 1. Environment Variables

Source the credentials from the Python project:

```bash
source ../nc_kyutai_live_transcriptions/.envrc
```

Or set them manually:

```bash
export LT_HPB_URL="wss://cloud.codemyriad.io/standalone-signaling/spreed"
export LT_INTERNAL_SECRET="<your-secret>"
export MODAL_WORKSPACE="silviot"
export MODAL_KEY="<your-key>"
export MODAL_SECRET="<your-secret>"
```

### 2. Build the Service

```bash
make build
```

## Running Tests

### Quick Automated Test

```bash
make live-test
```

This runs `scripts/test-live.sh` which:
1. Verifies environment variables
2. Starts the service
3. Checks health endpoint
4. Attempts to join the test room
5. Reports status

### Comprehensive E2E Test Tool

The E2E test tool (`tools/e2e/main.go`) performs a comprehensive end-to-end test:

```bash
# Basic E2E test (30 seconds, test tone audio)
./scripts/run-e2e.sh --duration 30 --verbose

# E2E test with real audio file
./scripts/run-e2e.sh --duration 30 --audio-file ../kyutai_modal/test_audio.wav --verbose

# Full options
go run ./tools/e2e/main.go \
    --room-url "https://cloud.codemyriad.io/call/erwcr27x" \
    --duration 30 \
    --audio-file /path/to/speech.wav \
    --language en \
    --guest-name "Test Bot" \
    --enable-transcription \
    --verbose
```

**What the E2E test does:**

1. **OCS API Integration**: Joins room as guest, sets display name
2. **Signaling Settings**: Gets STUN/TURN servers from Nextcloud
3. **Call Join**: Joins the audio/video call via OCS API
4. **HPB Connection**: Connects to HPB signaling with internal auth
5. **Live Transcription**: Enables live transcription via OCS API
6. **Modal Connection**: Connects to Modal STT service
7. **Audio Streaming**: Sends test audio (tone or WAV file) to Modal
8. **Transcript Reception**: Receives and displays transcripts

**Test Results:**
- [PASS] HPB connection established
- [PASS] Modal connection established
- [PASS] Audio sent to Modal
- [PASS/WARN] Transcripts received (depends on audio content)

### Manual E2E Test

1. **Start the service**:
   ```bash
   make run
   ```

2. **Open the test room in a browser**:
   https://cloud.codemyriad.io/call/erwcr27x

3. **Join as a guest or logged-in user**

4. **Enable live transcription** via Talk UI (if available)

5. **Speak** and verify transcripts appear

### Docker Test

```bash
# Build image
make docker

# Run with credentials
make docker-run
```

## Comparing with Python Implementation

To verify identical behavior:

1. **Run Python version**:
   ```bash
   cd ../nc_kyutai_live_transcriptions
   source .envrc
   source ../kyutai_modal/.envrc
   uv run python tools/roundtrip_modal.py \
     --room-url https://cloud.codemyriad.io/call/erwcr27x \
     --duration 30 \
     --enable-transcription
   ```

2. **Run Go version** (in another terminal):
   ```bash
   cd ../nc_kyutai_live_transcriptions_go
   source ../nc_kyutai_live_transcriptions/.envrc
   make run
   ```

3. **Compare**:
   - Memory usage (`htop` or `docker stats`)
   - Transcript latency
   - Connection stability
   - Error handling

## Deployment

### SSH Access

The Nextcloud instance is accessible via SSH:

```bash
ssh cloud.codemyriad.io
```

### Installing/Reinstalling the App

Use `docker exec` to run `php occ` commands for managing the transcription app:

```bash
ssh cloud.codemyriad.io

# Uninstall the app
sudo docker exec -it nextcloud-aio-nextcloud php occ app:disable nc_kyutai_live_transcriptions
sudo docker exec -it nextcloud-aio-nextcloud php occ app:remove nc_kyutai_live_transcriptions

# Reinstall the app
sudo docker exec -it nextcloud-aio-nextcloud php occ app:install nc_kyutai_live_transcriptions
sudo docker exec -it nextcloud-aio-nextcloud php occ app:enable nc_kyutai_live_transcriptions
```

### Useful AIO Commands

```bash
# View AIO container logs
sudo docker logs nextcloud-aio-mastercontainer

# View Talk logs
sudo docker exec -it nextcloud-aio-nextcloud grep talk /var/log/nextcloud.log

# View signaling server logs
sudo docker logs nextcloud-aio-talk
```

## HPB Protocol Notes

See `../nc_kyutai_live_transcriptions/research_on_talk_connection.md` for detailed HPB protocol analysis.

Key points:
- HPB uses WebSocket at `/standalone-signaling/spreed`
- Authentication via HMAC-SHA256 with internal secret
- Message types: `hello`, `room`, `message`, `bye`
- Session resumption via `resumeid`

## Troubleshooting

### "not_allowed" Error

Usually means:
- Wrong internal secret
- Mismatched origin/allowed_origins
- Backend connectivity issue

Check HPB logs:
```bash
ssh cloud.codemyriad.io
sudo docker logs nextcloud-aio-talk 2>&1 | tail -50
```

### No Transcripts Appearing

1. Verify Modal connection is working
2. Check if audio is being received (logs should show frame counts)
3. Verify transcript messages are being sent to HPB
4. Check Talk UI for closed captions toggle

### Connection Drops

1. Check network stability
2. Verify TURN/STUN servers are accessible
3. Review HPB reconnection logs

## Memory Profiling

```bash
# Start with pprof enabled
make profile

# In another terminal, capture heap profile
curl http://localhost:8080/debug/pprof/heap > heap.pprof
go tool pprof heap.pprof

# Or view in browser
go tool pprof -http=:9090 heap.pprof
```

Target: <50 MB per active speaker (vs 100 MB Python baseline).
