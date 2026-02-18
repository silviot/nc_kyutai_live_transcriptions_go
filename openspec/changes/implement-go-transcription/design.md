# Design Document: Go Live Transcription System

## Context

**Problem:** Python implementation suffers from:
- Unbounded queues causing memory growth
- Complex async/await lifecycle with race conditions
- Inefficient GC for aiortc/numpy
- 6 failed memory-fix attempts over 2 years
- Cannot reliably scale to 500+ concurrent speakers

**Opportunity:** Rewrite in Go using proven libraries (pion, gorilla) and simpler concurrency model (goroutines, channels).

**Constraints:**
- Must maintain HTTP API contract with existing FastAPI frontend
- Must handle HPB protocol identically (auth, reconnection, messages)
- Must produce identical transcript output (language, format, latency)
- Must fit within Docker container with 2GB memory limit
- Target: <50 MB per speaker (vs 100 MB Python baseline)

## Goals

**Primary Goals:**
1. Reduce memory per speaker from 100 MB to <50 MB
2. Eliminate unbounded queue growth (all channels bounded)
3. Eliminate manual GC and watchdog mechanisms
4. Handle 500+ concurrent speakers reliably
5. Zero memory leaks over 7+ day operation
6. Maintain <1s first-token transcript latency

**Non-Goals:**
- Rewrite FastAPI frontend (stays Python)
- Support additional languages (stay with en/fr)
- Implement new features (scope limited to rewrite)
- Support WebRTC browser clients (stay headless only)

## Decisions

### 1. Language & Runtime: Go 1.22+

**Decision:** Use Go with standard library concurrency

**Why:**
- `goroutines` cheaper than Python tasks (10x less overhead)
- `context.Context` standard for cancellation/deadline
- `sync.Mutex` minimal and predictable
- No GC overhead during allocation (stack-first)
- Single binary, no runtime dependencies
- pion/webrtc mature and production-proven

**Alternatives Considered:**
- **Rust:** Safest (compile-time guarantees), but webrtc-rs still 0.x, steeper learning curve, slower to implement
- **Elixir:** Excellent for fault tolerance, but ex_webrtc pre-1.0, adds Erlang VM ops complexity
- **Python + async fixes:** 6 attempts already failed; architectural mismatch

**Trade-off:** Go less type-safe than Rust but significantly faster to implement.

### 2. WebRTC: pion/webrtc v4

**Decision:** Use pion/webrtc for peer connections, audio extraction

**Why:**
- Production-proven (Twitch, LiveKit use pion)
- Full W3C WebRTC API support
- Modular (pion/ice, pion/stun, pion/turn)
- Active maintenance (updated 2025)
- Pure Go, no CGO dependencies

**Alternatives Considered:**
- **webrtc-rs:** Rust, 0.x version, API not stable
- **str0m:** Excellent design, but less documented for audio-only use
- **aiortc (Python):** Root cause of memory issues, avoid

**Trade-off:** pion has steeper learning curve than aiortc but solves memory problem.

### 3. WebSocket: gorilla/websocket

**Decision:** Use gorilla/websocket for HPB and Modal connections

**Why:**
- 6+ years production maturity
- Concurrent writes (necessary for bidirectional flow)
- Full TLS support
- Low allocation footprint

**Alternatives Considered:**
- **coder/websocket:** Zero-alloc, but less battle-tested
- **nhooyr/websocket:** Good, but less mature than gorilla

**Trade-off:** Gorilla has proven track record; not absolute fastest but solid.

### 4. Audio Processing: Pure Go + Resampling Library

**Decision:**
- No numpy/scipy: implement audio operations in Go
- Use `go-audio/resampling` for 48kHz → 24kHz downsampling
- Manual int16 → float32 conversion (straightforward, no deps)

**Why:**
- Removes numpy memory overhead
- Resampling can use libsoxr (via cgo) or pure Go implementations
- Audio frames are small and ephemeral (200ms chunks, <50KB each)
- No need for matrix operations

**Alternatives Considered:**
- **cgo to libsoxr:** Fast resampling but adds C dependency (Docker complexity)
- **pure Go resampler:** Slower but portable; acceptable for real-time audio
- **Fixed-point resampling:** Optimize later if CPU bottleneck

**Trade-off:** Pure Go slower but simpler deployment. Benchmark and optimize if CPU becomes issue.

### 5. Concurrency Model: Goroutines + Context

**Decision:**
- HPB client: 1 goroutine per room
- WebRTC peer: 1 goroutine per speaker (handles track callbacks)
- Audio pipeline: 1 goroutine per speaker (processes frames)
- Modal transcriber: 1 goroutine per speaker (sends/receives)
- Session orchestrator: 1 goroutine per room (manages lifecycle)

**Why:**
- Goroutines 10-100x cheaper than Python tasks (can spawn 10,000+)
- `context.Context` standard for cancellation (no manual teardown)
- Bounded channels prevent unbounded growth (built-in backpressure)
- Simple, clear lifecycle with `defer` guarantees cleanup order

**Alternatives Considered:**
- **Thread pool:** More complex, less Idiomatic Go
- **Async/await:** Python already tried, root cause of issues

**Channel Topology:**
```
HPB client
  ├─ room events → orchestrator
  └─ ICE candidates → WebRTC peer

WebRTC peer
  ├─ audio frames → audio pipeline
  └─ state changes → orchestrator

Audio pipeline
  ├─ resampled audio → Modal transcriber
  └─ backpressure ← transcriber

Modal transcriber
  ├─ transcripts → orchestrator
  └─ status ← orchestrator

Orchestrator
  ├─ coordinates all above
  ├─ delivers transcripts to HPB
  └─ manages shutdown cascade
```

### 6. Memory Bounds: Bounded Channels Everywhere

**Decision:**
- HPB message queue: 100 messages (fixed)
- Audio buffer channels: 10 buffers (200ms each = 2s max latency)
- Transcript result queue: 50 transcripts (fixed)
- Modal reconnection attempts: 5 (exponential backoff)

**Why:**
- Prevents unbounded growth (root cause of Python memory issues)
- Natural backpressure: goroutine blocks when channel full
- Forces explicit handling of "slow consumer" scenario
- Easier to reason about max memory: bounded channels × max size

**Alternatives Considered:**
- **Unbounded channels:** Simpler but reproduces Python problem
- **Lock-based bounded queues:** More complex, less idiomatic Go

**Trade-off:** Bounded channels require careful tuning; if too small, will drop data; if too large, wastes memory. Design with monitoring to validate bounds.

### 7. Error Handling: Typed Errors + Logging

**Decision:**
- Define error types per component (HPBError, WebRTCError, ModalError)
- All errors logged immediately with context (room, speaker, component)
- Errors don't cascade: each component handles its own recovery
- Session lifecycle continues if individual speaker fails

**Why:**
- Clear error semantics for debugging
- Isolated failures don't kill entire service
- Logging provides observability without external tracing

**Alternatives Considered:**
- **panic/recover:** Unacceptable for long-running server
- **Bare error strings:** No semantics, hard to diagnose

**Trade-off:** More boilerplate but much better observability.

### 8. API Bridge: HTTP-only (No gRPC for MVP)

**Decision:**
- Implement HTTP server matching Python contract
- Endpoints: POST /api/v1/call/transcribe, GET /healthz, GET /metrics
- No gRPC initially (adds complexity without immediate need)

**Why:**
- Maintains drop-in replacement for Python version
- FastAPI frontend needs no changes
- HTTP sufficient for request rate (few transcribe calls per second)

**Alternatives Considered:**
- **gRPC:** Better for streaming, but Nextcloud integration is HTTP
- **GraphQL:** Over-engineered for current needs

**Trade-off:** HTTP slower but sufficient; can add gRPC later if needed.

### 9. Deployment: Single Binary in Container

**Decision:**
- Multi-stage Dockerfile: Go builder → distroless runtime
- No Python runtime in final image
- Environment variables only (no config files)
- Metrics exposed via Prometheus format

**Why:**
- Single binary deployment (no Python deps to manage)
- Distroless reduces surface area and image size
- Environment variables match Nextcloud conventions
- Prometheus easy to integrate with existing monitoring

**Alternatives Considered:**
- **Alpine + dynamic linking:** Simpler build, harder to debug if libs change
- **Config files:** More flexibility but complexity for simple app

**Trade-off:** Distroless harder to debug locally; use multi-stage for dev/prod split.

## Risks & Mitigation

| Risk | Impact | Mitigation | Acceptance |
|------|--------|-----------|-----------|
| **pion/webrtc API changes** | Forced rewrite | Follow SDP negotiation spec; write adapter layer if needed | Medium |
| **Audio quality regression** | Transcription accuracy drops | Bit-exact resampling test vs Python baseline | High |
| **Memory leak in pion/webrtc** | Service fails in production | Monitor with pprof, 7-day soak test before cutover | High |
| **Modal connection instability** | Transcriptions fail | Exponential backoff (5 attempts), document retry policy | Medium |
| **Nextcloud HPB protocol drift** | Signaling broken | Version HPB client code, maintain feature parity with Python | Low |
| **Go concurrency bugs (race condition)** | Intermittent failures | `-race` flag in tests, high test coverage, stress tests | Medium |
| **Resampling performance** | CPU bottleneck | Benchmark vs Python; cache resampler coefficients | Low |
| **Goroutine leak** | Memory growth over time | Review goroutine shutdown in all paths, pprof analysis | Medium |

## Migration Plan

### Pre-migration (Week 0)
1. Set up Go project with all dependencies
2. Write comprehensive tests (unit + integration)
3. Deploy to staging with mock HPB/Modal
4. Run 7-day soak test (memory + stability)

### Phase 1: Parallel Deployment (Week 3)
1. Deploy Go service to production (alongside Python)
2. Configure load balancer to route 10% traffic to Go
3. Monitor metrics vs Python baseline

### Phase 2: Validation (Week 3-4)
1. Compare memory usage, latency, error rates
2. Run A/B test on real traffic
3. Verify transcript quality identical

### Phase 3: Cutover (Week 4)
1. Increase traffic 10% → 50% → 100% over 2-3 days
2. Monitor closely after each increase
3. Maintain Python service for rollback (48h)

### Phase 4: Decommission (Week 4+)
1. After 2 weeks stable Go operation
2. Decommission Python service
3. Archive this change in openspec
4. Document lessons learned

## Open Questions

1. **Resampling library choice:** Pure Go vs libsoxr (cgo)?
   - **Decision:** Pure Go linear interpolation (implemented)
   - Simpler deployment, no cgo dependencies
   - Benchmark against Python baseline in live testing
   - If quality issues: consider libsoxr via cgo

2. **Modal cold start timeout calibration:** Is 120s sufficient?
   - **Status:** Implemented with 120s timeout
   - Validate with actual Modal workspace during live testing
   - Document in Kyutai integration runbook

3. **HPB reconnection backoff tuning:** Exponential (base=2, max 5) appropriate?
   - **Status:** Implemented as designed
   - Monitor reconnection frequency during live testing
   - Adjust if too aggressive or passive

4. **Bounded channel sizes:** Are proposed sizes (100, 10, 50) correct?
   - **Status:** Implemented:
     - Audio input: 100 frames (~2s buffer)
     - Audio output: 10 chunks (200ms each = 2s buffer)
     - Modal reconnection: 5 attempts
   - Validate under load during live testing
   - Monitor channel fullness in production metrics

5. **API contract completeness:** Any undocumented Python behaviors needed?
   - **Status:** Basic endpoints implemented (POST /transcribe, GET /healthz)
   - Test with actual Nextcloud Talk instance (see LIVE_TESTING.md)
   - Compare behavior with Python roundtrip_modal.py script

