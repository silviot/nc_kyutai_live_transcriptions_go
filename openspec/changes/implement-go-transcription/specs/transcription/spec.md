# Specification: Live Transcription Service (Go)

## ADDED Requirements

### Requirement: HPB Signaling Protocol
The transcription service SHALL establish and maintain WebSocket connections to Nextcloud's High-Performance Backend (HPB) for real-time room and participant signaling.

#### Scenario: HPB Connection Establishment
- **WHEN** a transcription request arrives for a room
- **THEN** the service SHALL connect to HPB at `$LT_HPB_URL` via WebSocket
- **AND** authenticate using HMAC-SHA256 signature with `$LT_INTERNAL_SECRET`
- **AND** send `hello` message with session initialization
- **AND** expect room topology data in response

#### Scenario: Session Resumption
- **WHEN** HPB connection drops temporarily
- **THEN** the service SHALL attempt reconnection with exponential backoff (base=2, max 5 attempts)
- **AND** use `resumeid` from previous session if available
- **AND** resume without losing participant state if within 30 seconds
- **AND** reset participant state if resumeid expired

#### Scenario: Participant Join/Leave
- **WHEN** HPB receives `message` indicating participant join/leave
- **THEN** the service SHALL track participant (sessionid, userid, name)
- **AND** create WebRTC peer connection for newly joined participant
- **AND** clean up peer connection and transcriber for departing participant

#### Scenario: Room Closure
- **WHEN** HPB receives `bye` message or room ends
- **THEN** the service SHALL close all peer connections in the room
- **AND** stop all active transcribers
- **AND** release room state
- **AND** log completion metrics (total speakers, duration)

---

### Requirement: WebRTC Audio Reception
The transcription service SHALL establish WebRTC peer connections to receive participant audio from the Janus SFU.

#### Scenario: Peer Connection Creation
- **WHEN** HPB signals a participant join
- **THEN** the service SHALL create RTCPeerConnection
- **AND** request ICE candidates and connection state monitoring
- **AND** await offer from Janus (via HPB)
- **AND** send answer with audio-only configuration
- **AND** begin gathering ICE candidates

#### Scenario: Audio Track Extraction
- **WHEN** RTCPeerConnection reaches `connected` state
- **THEN** the service SHALL extract incoming audio track
- **AND** monitor OnTrack callback for frame delivery
- **AND** forward audio frames to audio pipeline
- **AND** detect track closure and cleanup

#### Scenario: Connection Failure
- **WHEN** RTCPeerConnection fails (connection timeout, no frames)
- **THEN** the service SHALL detect within 5 seconds
- **AND** log failure reason (ICE failure, renegotiation needed, timeout)
- **AND** clean up peer connection resources
- **AND** stop associated transcriber
- **AND** optionally retry (per session policy)

---

### Requirement: Audio Processing Pipeline
The transcription service SHALL reformat and buffer audio frames for streaming to Modal's Kyutai STT model.

#### Scenario: Audio Resampling
- **WHEN** WebRTC delivers 48kHz stereo audio frames
- **THEN** the service SHALL resample to 24kHz mono
- **AND** maintain audio quality (no artifacts)
- **AND** handle variable frame sizes from WebRTC

#### Scenario: Audio Format Conversion
- **WHEN** audio is resampled
- **THEN** the service SHALL convert from int16 PCM to float32 PCM
- **AND** normalize float32 range to [-1.0, 1.0]
- **AND** preserve audio quality (bit-exact verification against Python baseline)

#### Scenario: Audio Buffering
- **WHEN** float32 PCM frames arrive
- **THEN** the service SHALL buffer into 200ms chunks (4,800 samples @ 24kHz)
- **AND** send to transcriber immediately upon threshold
- **AND** implement bounded channel to avoid memory growth
- **AND** apply backpressure if transcriber cannot consume

#### Scenario: Silence Detection
- **WHEN** audio contains silence
- **THEN** the service MAY send silence frames
- **AND** the transcriber will indicate silence via `vad_end` flag
- **AND** long silence periods MAY trigger connection recheck

---

### Requirement: Modal STT Integration
The transcription service SHALL stream audio to Modal's Kyutai STT model and receive streaming transcripts.

#### Scenario: Modal Connection
- **WHEN** audio pipeline ready and Modal accessible
- **THEN** the service SHALL establish WebSocket to Modal endpoint at `$MODAL_WORKSPACE--kyutai-stt-rust-kyutaisttrustservice-serve.modal.run`
- **AND** authenticate with `$MODAL_KEY` and `$MODAL_SECRET` via proxy headers
- **AND** expect initial connection ACK within 5 seconds

#### Scenario: Audio Streaming
- **WHEN** audio chunks available
- **THEN** the service SHALL send binary frames to Modal
- **AND** each frame is float32 PCM (200ms @ 24kHz)
- **AND** send frames at real-time rate (no buffering ahead)
- **AND** keep connection idle <10 seconds (send silence if needed)

#### Scenario: Transcript Reception
- **WHEN** Modal processes audio frames
- **THEN** the service SHALL receive JSON responses: `{"text": "...", "final": bool, "vad_end": bool}`
- **AND** `text` contains partial or final transcript tokens
- **AND** `final: true` indicates end of utterance
- **AND** `vad_end: true` indicates voice activity end

#### Scenario: Cold Start Timeout
- **WHEN** Modal is in cold start (first invocation)
- **THEN** the service SHALL tolerate up to 120s before first transcript
- **AND** log cold start detected
- **AND** resume normal operation once first token received
- **AND** NOT time out on 120s window

#### Scenario: Connection Failure
- **WHEN** Modal connection fails or stalls (>30s without data)
- **THEN** the service SHALL detect and log failure
- **AND** stop audio streaming
- **AND** reconnect with exponential backoff (base=2, max 5 attempts)
- **AND** notify session of transcript loss

---

### Requirement: Session Lifecycle Management
The transcription service SHALL manage room-level state and orchestrate component interactions.

#### Scenario: Room Creation
- **WHEN** POST `/api/v1/call/transcribe` received with valid room_token
- **THEN** the service SHALL validate language support (en/fr)
- **AND** check available memory (require <50MB free per speaker)
- **AND** create room state object
- **AND** start HPB client goroutine
- **AND** return 200 OK with session ID

#### Scenario: Concurrent Speaker Management
- **WHEN** multiple speakers active in same room
- **THEN** the service SHALL maintain separate:
  - WebRTC peer connection per speaker
  - Audio pipeline per speaker
  - Modal transcriber per speaker
- **AND** coordinate transcript delivery to shared target (e.g., display)
- **AND** apply backpressure if any component saturated

#### Scenario: Memory Pressure
- **WHEN** memory usage exceeds 80% of available
- **THEN** the service SHALL:
  - Log memory pressure warning
  - Stop accepting new speakers
  - Prioritize existing speakers
  - Gracefully degrade if needed (e.g., skip silence frames)
- **AND** NOT crash or OOM

#### Scenario: Graceful Shutdown
- **WHEN** graceful shutdown signal received
- **THEN** the service SHALL:
  - Stop accepting new calls
  - Close room state gracefully
  - Wait for in-flight transcripts (up to 5 seconds)
  - Close all peer connections
  - Close Modal connections
  - Close HPB connection
- **AND** log shutdown completion metrics
- **AND** exit cleanly with code 0

#### Scenario: Error Propagation
- **WHEN** any component fails (HPB, WebRTC, Modal)
- **THEN** the service SHALL:
  - Log error with context (room, speaker, component)
  - Isolate failure to affected speaker only
  - Attempt recovery via reconnection
  - Continue serving other speakers in room
  - Notify API client of partial failure if applicable

---

### Requirement: Transcript Delivery
The transcription service SHALL deliver transcripts to Nextcloud Talk as closed captions via HPB.

#### Scenario: Transcript Routing
- **WHEN** Modal provides transcript token
- **THEN** the service SHALL:
  - Attribute transcript to source speaker
  - Format for HPB message protocol
  - Send via HPB to room (all participants receive)
- **AND** maintain ordering (no out-of-order delivery)
- **AND** apply dedupe if Modal resends (detect via token ID)

#### Scenario: Latency
- **WHEN** speaker produces audio
- **THEN** first-token transcript SHALL arrive within 1 second (target, not SLA)
- **AND** acceptable latency is <2 seconds (SLA)
- **AND** measure and log latency per transcript

#### Scenario: Partial vs Final
- **WHEN** Modal sends `final: false`
- **THEN** the service MAY buffer and not send immediately (implementation choice)
- **WHEN** Modal sends `final: true`
- **THEN** the service SHALL send immediately to HPB

---

### Requirement: Language Support
The transcription service SHALL support multi-language transcription via Kyutai STT model.

#### Scenario: Language Validation
- **WHEN** transcription request includes `lang_id`
- **THEN** the service SHALL validate against supported languages (en, fr)
- **AND** reject with 400 Bad Request if unsupported
- **AND** log unsupported language attempt

#### Scenario: Language Configuration
- **WHEN** HPB provides language setting for room
- **THEN** the service SHALL pass to Modal endpoint
- **AND** configure STT model for correct language
- **AND** NOT switch mid-session

---

### Requirement: Health & Observability
The transcription service SHALL provide health checks and operational metrics.

#### Scenario: Health Check
- **WHEN** GET `/healthz` requested
- **THEN** the service SHALL return 200 OK with status JSON:
  ```json
  {
    "status": "healthy|degraded|unhealthy",
    "rooms": N,
    "speakers": N,
    "memory_mb": X,
    "timestamp": "2026-02-05T12:00:00Z"
  }
  ```
- **AND** respond within 100ms

#### Scenario: Metrics Endpoint
- **WHEN** GET `/metrics` requested (Prometheus format)
- **THEN** the service SHALL export:
  - `transcription_rooms_active` (gauge)
  - `transcription_speakers_active` (gauge)
  - `transcription_transcripts_total` (counter)
  - `transcription_errors_total` (counter by type)
  - `transcription_latency_ms` (histogram)
  - `go_memory_alloc_bytes` (gauge)
- **AND** metrics update in real-time

#### Scenario: Structured Logging
- **WHEN** events occur (connection, error, completion)
- **THEN** the service SHALL log JSON-formatted entries with:
  - `timestamp`, `level`, `message`
  - `room_token`, `speaker_id` (if applicable)
  - `component` (hpb, webrtc, audio, modal, session)
  - `error` (if applicable)
- **AND** log to stdout for container collection

