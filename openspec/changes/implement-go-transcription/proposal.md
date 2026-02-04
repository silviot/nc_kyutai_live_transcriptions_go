# Change: Implement Go Live Transcription System

## Why

Complete rewrite of the Kyutai Live Transcription system in Go to resolve architectural memory and concurrency issues present in the Python implementation:

**Memory Problems:**
- Unbounded transcript queue can grow without limit
- ~100 MB per speaker in aiortc/numpy overhead
- Python GC inefficient with complex async lifecycle
- Manual `gc.collect()` calls indicate fundamental design mismatch

**Concurrency Problems:**
- 5 base tasks + 4 tasks per speaker = complex task coordination
- 3 different async locks (peer_connection, target, transcriber)
- Mixed threading.Event and asyncio
- 2-second timing windows for cleanup

**Result:**
- 6 previous memory-fix attempts failed (addressing symptoms, not root cause)
- Need architecturally sound solution for long-running server

**Go Solution:**
- pion/webrtc (production-proven by Twitch, LiveKit)
- Goroutines (10,000+ concurrent without overhead)
- Bounded channels (natural backpressure, no unbounded queues)
- Stack-based allocation, efficient GC
- Guaranteed cleanup with defer

## What Changes

Four-phase implementation:

**Phase 1 (Weeks 1-2): Core Modules**
- HPB signaling client with WebSocket and reconnection (~400 lines)
- WebRTC manager with pion/webrtc for audio extraction (~300 lines)
- Audio pipeline with resampling and buffering (~200 lines)

**Phase 2 (Week 2): Modal Integration**
- Modal transcriber WebSocket client (~200 lines)
- Audio streaming and transcript result parsing

**Phase 3 (Weeks 2-3): Orchestration**
- Session manager for room and speaker lifecycle (~300 lines)
- HTTP/gRPC API bridge for Python FastAPI frontend (~200 lines)

**Phase 4 (Weeks 3-4): Testing & Migration**
- Unit tests with mock HPB/Modal servers
- Integration tests with full flow
- Load testing for 500+ concurrent speakers
- Production deployment with parallel Python version
- Gradual traffic migration and cutover

**Breaking Changes:**
- Deployment topology: Single Go binary replaces Python + Uvicorn
- API bridge: HTTP/gRPC replaces direct Python integration (transparent to frontend)

**Non-Breaking:**
- FastAPI endpoint contract remains unchanged
- Transcript format and latency targets same as Python

## Impact

- **Affected specs:** transcription (core capability rewrite in Go)
- **Affected code:** Complete rewrite of `pkg/hpb`, `pkg/webrtc`, `pkg/audio`, `pkg/modal`, `pkg/session`
- **Removed:** memory_watchdog (no longer needed in Go)
- **New files:** All Go source code, Dockerfile, tests, CI/CD pipeline

**Success Metrics:**
- Memory: <50 MB per active speaker (vs 100 MB Python baseline)
- Stability: Zero memory leaks over 7-day continuous operation
- Performance: <1s transcript latency (match Python)
- Scalability: Handle 500+ concurrent speakers
- Reliability: Graceful reconnection, no Talk room corruption

## Rollback Plan

- Deploy Go service alongside Python version during Phase 4
- Route 10% traffic to Go, monitor metrics vs Python baseline
- Gradually increase to 100% if validation passes
- Maintain Python service for immediate rollback if needed
- Decommission Python after 2 weeks of stable Go operation

