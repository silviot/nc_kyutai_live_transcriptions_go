# Implementation Tasks: Go Live Transcription System

## Phase 1: Core Modules (Weeks 1-2)

### 1.1 Project Setup
- [x] 1.1.1 Initialize Go module (go 1.22+)
- [x] 1.1.2 Set up package structure (cmd/, pkg/, test/)
- [x] 1.1.3 Add go.mod with core dependencies (pion, gorilla/websocket)
- [x] 1.1.4 Create Makefile for build/test/lint
- [ ] 1.1.5 Set up GitHub Actions CI/CD pipeline

### 1.2 HPB Signaling Client
- [x] 1.2.1 Define HPB protocol message types (hello, incall, room, message, bye)
- [x] 1.2.2 Implement WebSocket connection with TLS support
- [x] 1.2.3 Implement HMAC-SHA256 authentication (LT_INTERNAL_SECRET)
- [x] 1.2.4 Implement reconnection logic with exponential backoff
- [x] 1.2.5 Implement session resumption (resumeid)
- [x] 1.2.6 Implement participant tracking
- [x] 1.2.7 Write unit tests with mock HPB server
- [ ] 1.2.8 Document HPB protocol flow

### 1.3 WebRTC Manager
- [x] 1.3.1 Define RTC peer configuration and ICE candidate handling
- [x] 1.3.2 Implement PeerConnection creation with STUN/TURN servers from HPB
- [x] 1.3.3 Implement SDP offer/answer negotiation (JSEP)
- [x] 1.3.4 Implement audio track extraction from peer connection
- [x] 1.3.5 Implement OnICECandidate callback
- [x] 1.3.6 Implement connection state monitoring (connected, disconnected, failed)
- [ ] 1.3.7 Write unit tests for peer connection lifecycle
- [ ] 1.3.8 Test audio frame delivery format (channels, sample rate)

### 1.4 Audio Pipeline
- [x] 1.4.1 Define audio buffer structure (capacity, format)
- [x] 1.4.2 Implement audio frame buffering from WebRTC tracks
- [x] 1.4.3 Implement resampling (48kHz → 24kHz) using linear interpolation
- [x] 1.4.4 Implement format conversion (int16 PCM → float32 PCM)
- [x] 1.4.5 Implement 200ms chunk buffering logic
- [x] 1.4.6 Implement bounded channel for audio output (natural backpressure)
- [x] 1.4.7 Write tests for resampling accuracy
- [x] 1.4.8 Write tests for format conversion edge cases

---

## Phase 2: Modal Integration (Week 2)

### 2.1 Modal Transcriber Client
- [x] 2.1.1 Define Modal authentication (workspace key/secret)
- [x] 2.1.2 Implement WebSocket connection to Modal endpoint
- [x] 2.1.3 Implement binary frame sending (float32 PCM)
- [x] 2.1.4 Implement JSON response parsing (text, final, vad_end fields)
- [x] 2.1.5 Implement stale connection detection (timeout on no data)
- [x] 2.1.6 Implement reconnection with exponential backoff
- [ ] 2.1.7 Write unit tests with mock Modal server
- [ ] 2.1.8 Test audio streaming format and frame boundaries

### 2.2 Transcript Result Handling
- [x] 2.2.1 Define transcript message structure
- [x] 2.2.2 Implement result parsing from Modal responses
- [x] 2.2.3 Implement error handling (timeout, malformed responses)
- [x] 2.2.4 Implement backpressure handling (Modal slow to consume)
- [ ] 2.2.5 Test result ordering and deduplication

---

## Phase 3: Orchestration (Weeks 2-3)

### 3.1 Session Manager
- [x] 3.1.1 Define Room state structure (token, participants, transcribers)
- [x] 3.1.2 Implement room creation/deletion lifecycle
- [x] 3.1.3 Implement speaker/participant tracking
- [x] 3.1.4 Implement target (who gets transcripts) management
- [x] 3.1.5 Implement goroutine spawn/cleanup with WaitGroup
- [x] 3.1.6 Implement graceful shutdown with context cancellation
- [x] 3.1.7 Implement backpressure coordination between components
- [ ] 3.1.8 Write tests for concurrent room operations

### 3.2 Orchestration Coordination
- [x] 3.2.1 Implement HPB → WebRTC signaling flow
- [x] 3.2.2 Implement WebRTC → Audio pipeline flow
- [x] 3.2.3 Implement Audio → Modal transcriber flow
- [x] 3.2.4 Implement transcript → result delivery
- [ ] 3.2.5 Test cascading cleanup on any component failure
- [ ] 3.2.6 Test concurrent operations in same room

### 3.3 API Bridge
- [x] 3.3.1 Define HTTP/gRPC endpoints (matching Python contract)
- [x] 3.3.2 Implement POST /api/v1/call/transcribe
- [x] 3.3.3 Implement status/health endpoints
- [ ] 3.3.4 Implement metrics (concurrent speakers, memory usage)
- [x] 3.3.5 Implement request/response validation
- [x] 3.3.6 Write integration tests with HTTP client

---

## Phase 4: Testing & Migration (Weeks 3-4)

### 4.1 Unit Tests
- [x] 4.1.1 HPB protocol parsing and message handling
- [x] 4.1.2 HPB reconnection logic
- [ ] 4.1.3 WebRTC peer connection lifecycle
- [x] 4.1.4 Audio pipeline resampling and format conversion
- [ ] 4.1.5 Modal authentication and response parsing
- [x] 4.1.6 Session manager room operations
- [ ] 4.1.7 All error scenarios (disconnects, timeouts, malformed data)
- [ ] 4.1.8 Coverage: Target >80% overall

### 4.2 Integration Tests
- [x] 4.2.1 Mock HPB server for signaling flow
- [ ] 4.2.2 Mock Modal server for transcription flow
- [ ] 4.2.3 Full E2E flow: HPB → WebRTC → Audio → Modal
- [ ] 4.2.4 Concurrent rooms with same transcriber
- [ ] 4.2.5 Graceful shutdown under active sessions
- [ ] 4.2.6 Recovery from transient failures

### 4.3 Load & Memory Tests
- [ ] 4.3.1 Create load test harness (N concurrent rooms)
- [ ] 4.3.2 Memory profiling: pprof baseline
- [ ] 4.3.3 Test 100 concurrent speakers (target: <100 MB)
- [ ] 4.3.4 Test 500 concurrent speakers (target: <500 MB)
- [ ] 4.3.5 7-day soak test (detect leaks)
- [ ] 4.3.6 Document memory benchmarks

### 4.4 Production Readiness
- [x] 4.4.1 Write Dockerfile (multi-stage, alpine)
- [x] 4.4.2 Create Docker compose for local testing
- [x] 4.4.3 Write deployment documentation (DEPLOY.md)
- [ ] 4.4.4 Set up prometheus metrics export
- [ ] 4.4.5 Document operational runbooks (restart, scaling, debugging)

### 4.5 Live Testing (NEW - Nextcloud Integration)
- [ ] 4.5.1 Create scripts/test-live.sh for testing against live Nextcloud
- [ ] 4.5.2 Document Nextcloud AIO setup (see LIVE_TESTING.md)
- [ ] 4.5.3 Test HPB connection with real signaling server
- [ ] 4.5.4 Test WebRTC audio reception in test room
- [ ] 4.5.5 Test transcript delivery to Talk participants
- [ ] 4.5.6 Compare behavior with Python implementation

### 4.6 Parallel Deployment
- [ ] 4.6.1 Deploy Go service alongside Python in staging
- [ ] 4.6.2 Configure traffic split (10% → Go, 90% → Python)
- [ ] 4.6.3 Monitor metrics (memory, latency, error rates)
- [ ] 4.6.4 Compare transcript quality vs Python baseline
- [ ] 4.6.5 Gradually increase traffic split (10% → 50% → 100%)
- [ ] 4.6.6 Run 48-hour stability test at 100% traffic

### 4.7 Cutover & Decommission
- [ ] 4.7.1 Final validation checklist (all metrics green)
- [ ] 4.7.2 Complete cutover to Go (100% traffic)
- [ ] 4.7.3 Monitor for 48 hours
- [ ] 4.7.4 Decommission Python service
- [ ] 4.7.5 Archive this change in openspec
- [ ] 4.7.6 Update project documentation

---

## Success Criteria (Completion Gate)

Before marking Phase 4 complete:
- ✅ All unit tests passing (>80% coverage)
- ✅ All integration tests passing
- ✅ Memory: <50 MB per concurrent speaker
- ✅ Zero memory leaks over 7-day soak test
- ✅ Latency: <1s first-token transcription (match Python)
- ✅ Handles 500+ concurrent speakers
- ✅ Graceful reconnection without Talk room corruption
- ✅ Metrics match Python baseline (error rates, throughput)
- ✅ Documentation complete (API, deployment, runbooks)

