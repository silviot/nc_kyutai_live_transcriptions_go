# Project Context

## Purpose

Kyutai Live Transcription for Nextcloud Talk (Go implementation) - a real-time speech-to-text solution that integrates with Nextcloud Talk video calls using Kyutai's streaming STT model deployed on Modal.com.

**Key Goals:**
- Low latency (~0.5 second first-token latency) streaming transcription
- GPU-accelerated inference on Modal.com's serverless infrastructure (A10G/A100)
- Automatic scaling from zero with pay-per-second billing
- Multi-language support (English and French via `kyutai/stt-1b-en_fr`)
- <50 MB memory per active speaker (vs 100 MB Python)
- No memory leaks over 7+ day operation
- Graceful handling of 500+ concurrent speakers

**How it works:**
1. Connects to Nextcloud's High-Performance Backend (HPB) signaling server via WebSocket
2. Receives participant audio via WebRTC from Janus SFU using pion/webrtc
3. Sends audio to Modal's Kyutai STT inference service
4. Broadcasts transcriptions back to call participants as closed captions

## Tech Stack

**Language & Runtime:**
- Go 1.22+ (single compiled binary)
- Standard library for core concurrency

**Core Dependencies:**
- `github.com/pion/webrtc/v4` - WebRTC peer connections for audio reception
- `github.com/gorilla/websocket` - WebSocket communication with HPB and Modal
- `github.com/pion/opus` - Opus codec operations
- `github.com/go-audio/resampling` - Audio resampling (48kHz → 24kHz)

**Development & Testing:**
- `testing` - Go's standard testing framework
- `mock` libraries for HPB/Modal server simulation

**Container:**
- Docker multi-stage build (Go 1.22 builder → distroless)
- No system audio library dependencies (pure Go where possible)

## Project Conventions

### Code Style

- **Formatter:** `gofmt` (standard)
- **Linter:** `golangci-lint`
- **Naming conventions:**
  - `roomToken` - Nextcloud Talk call identifier
  - `ncSessionID` - Participant's Nextcloud session ID
  - `langID` - Language code (en/fr)
  - `resumeID` - HPB session resume token
- **Logging:** Structured logging with `log/slog`, logs to stdout
- **Package organization:**
  - `cmd/transcribe-service` - Main entry point
  - `pkg/hpb` - HPB signaling client
  - `pkg/webrtc` - WebRTC peer connection management
  - `pkg/audio` - Audio processing pipeline
  - `pkg/modal` - Modal STT client
  - `pkg/session` - Session lifecycle management

### Architecture Patterns

**Component Architecture:**
```
Nextcloud Talk (FastAPI Python)
        │
        ▼
    POST /api/v1/call/transcribe
        │
        ▼
┌─────────────────────────────────┐
│  Transcribe Service (Go)        │ Manages lifecycle
└────────────┬────────────────────┘
             │ goroutines per room
             ▼
┌─────────────────────────────────┐
│  HPB Client (gorilla/websocket) │ WebSocket to HPB
└────────────┬────────────────────┘
             │ Signaling: hello, incall, room, message, bye
             ▼
┌─────────────────────────────────┐
│  WebRTC Manager (pion/webrtc)   │ Peer connections, audio extraction
└────────────┬────────────────────┘
             │ Audio frames
             ▼
┌─────────────────────────────────┐
│  Audio Pipeline                 │ Resampling, format conversion, buffering
└────────────┬────────────────────┘
             │
             ▼
┌─────────────────────────────────┐
│  Modal Client (gorilla/websocket)│ WebSocket to Modal GPU
└────────────┬────────────────────┘
             │ wss://
             ▼
    Modal.com Kyutai STT Inference
```

**Key Design Elements:**
- **Goroutines:** Lightweight concurrency primitives (context-based cancellation)
- **Channels:** Bounded message passing with backpressure
- **Graceful shutdown:** `defer` statements guarantee cleanup order
- **No memory watchdog:** Go's GC handles cleanup efficiently
- **Error handling:** Custom error types for HPB/Modal/WebRTC failures

**Concurrency Patterns:**
- `context.Context` for cancellation and deadlines
- `sync.Mutex` for critical sections (minimal scope)
- Unbuffered/bounded channels for synchronization
- Explicit goroutine lifecycle with `sync.WaitGroup`

### Testing Strategy

- **Framework:** Go's standard `testing` package
- **Coverage:** `go test -cover`
- **Test location:** `*_test.go` alongside package files
- **Patterns:** Mock servers for HPB/Modal, audio fixtures for pipeline tests

**Validation:**
1. Unit tests for HPB protocol, WebRTC negotiation, audio pipeline
2. Integration tests with mock HPB/Modal servers
3. Load tests for concurrent speaker capacity
4. Memory profiling with `pprof`

**Live Testing:**
- Nextcloud instance: `$NEXTCLOUD_URL` (Nextcloud AIO recommended)
- Test room: `$NEXTCLOUD_URL/call/$TALK_ROOM_TOKEN`
- See `LIVE_TESTING.md` for detailed instructions
- Use `make live-test` for automated basic tests

### Git Workflow

- Branch naming: Feature branches (e.g., `phase-1-hpb-client`)
- Commit messages: Imperative mood, concise descriptions
- CI required to pass before merge
- GitHub Actions: Build, test, lint, security scan

## Domain Context

**Audio Processing Pipeline:**
- WebRTC delivers 48kHz stereo audio
- Resampled to 24kHz mono (Kyutai model expectation)
- Encoded as raw float32 PCM (32-bit LE, [-1.0, 1.0] range)
- Buffered in 200ms chunks (optimal for streaming inference)

**HPB Protocol:**
- WebSocket connection to `LT_HPB_URL`
- Session-based with `resumeid` for reconnection
- Message types: `hello`, `incall`, `room`, `message`, `bye`
- Authentication via HMAC-SHA256 signatures with `LT_INTERNAL_SECRET`

**Modal Integration:**
- WebSocket endpoint: `wss://{workspace}--kyutai-stt-rust-kyutaisttrustservice-serve.modal.run/v1/stream`
- Binary WebSocket frames for audio (float32 PCM)
- JSON responses: `{"text": "...", "final": bool, "vad_end": bool}`
- Workspace auth: Key + secret proxy headers

**WebRTC/Janus:**
- STUN/TURN servers from HPB settings
- RTCPeerConnection for receiving audio tracks
- JSEP for offer/answer negotiation
- Automatic ICE gathering and connectivity

## Important Constraints

**Memory Management:**
- Target: <50 MB per active transcriber (vs 100 MB Python)
- Stack-based allocation where possible
- No unbounded queues (all channels bounded)
- Automatic GC (no manual collection needed)

**Language Support:**
- English and French (Kyutai model limitation)
- Validated before transcription starts

**Modal Cold Start:**
- 120s timeout for Modal cold starts
- Exponential backoff for connection retries (base=2, max 5 attempts)

**Concurrency:**
- Design for 500+ concurrent speakers
- Each speaker: 1 HPB client, 1 WebRTC peer, 1 audio pipeline, 1 Modal client
- Graceful degradation under memory pressure

## External Dependencies

**Nextcloud Integration:**
- **AppAPI framework** - ExApp lifecycle management (via Python FastAPI frontend)
- **HPB (High-Performance Backend)** - WebSocket signaling for room events
- **Talk app (spreed)** - Closed captions capability discovery
- **OCS API** - Settings retrieval

**Modal.com Integration:**
- **Workspace authentication** - Key + secret proxy auth
- **WebSocket endpoint** - Binary audio streaming
- **Protocol** - Custom JSON response format for transcriptions

**WebRTC/Janus:**
- STUN/TURN servers from HPB settings
- RTCPeerConnection for receiving audio tracks
- JSEP for offer/answer negotiation

**Environment Variables (Required):**
- `LT_HPB_URL` - HPB signaling server URL
- `LT_INTERNAL_SECRET` - HMAC secret for Nextcloud validation
- `MODAL_WORKSPACE` - Modal.com workspace name
- `MODAL_KEY` - Modal API key
- `MODAL_SECRET` - Modal API secret

**Environment Variables (Optional):**
- `SKIP_CERT_VERIFY` - For self-signed certs
- `PORT` - Server listen port (default 8080)
- `LOG_LEVEL` - Logging verbosity (default info)

