package session

import (
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/hpb"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/modal"
)

// run executes the transcriber main loop
func (t *Transcriber) run(hpbClient *hpb.Client, roomToken string, logger *slog.Logger) {
	defer t.wg.Done()
	defer t.modalClient.Close()

	// Start audio processing goroutine immediately (don't wait for Modal)
	t.wg.Add(1)
	go t.processAudioLoop(logger)

	// Connect Modal in the background (can take 30+ seconds for cold start)
	modalReady := make(chan struct{})
	go func() {
		if err := t.modalClient.Connect(t.ctx); err != nil {
			logger.Error("failed to connect to Modal", "sessionID", t.sessionID, "error", err)
			return
		}
		close(modalReady)
	}()

	// Process transcripts and handle reconnection
	audioChunkCount := 0
	modalConnectAttempts := 0
	maxAttempts := 5
	backoffBase := 2
	backoffMultiplier := 1.0

	// Ticker to flush throttled broadcasts
	flushTicker := time.NewTicker(broadcastMinInterval)
	defer flushTicker.Stop()

	for {
		select {
		case <-t.ctx.Done():
			t.drainOnShutdown(hpbClient, roomToken, logger)
			logger.Debug("transcriber shutting down", "sessionID", t.sessionID)
			return

		// Flush any throttled broadcast text
		case <-flushTicker.C:
			t.flushPendingBroadcast(logger)

		// Handle transcript results from Modal
		case transcript := <-t.modalClient.TranscriptChan():
			t.handleTranscript(hpbClient, roomToken, transcript, logger)

		// Handle errors from Modal
		case err := <-t.modalClient.ErrorChan():
			logger.Warn("Modal error", "sessionID", t.sessionID, "error", err)

			// Don't reconnect on normal close (code 1000). This means the server
			// intentionally closed the connection (e.g. idle timeout because no
			// audio was being sent). Reconnecting would just create another idle
			// connection that gets closed again, wasting resources.
			if modal.IsNormalClose(err) {
				t.flushPendingFinal(logger)
				logger.Info("Modal connection closed normally (idle timeout), not reconnecting", "sessionID", t.sessionID)
				continue
			}

			if !t.modalClient.IsConnected() {
				// Try to reconnect
				if modalConnectAttempts < maxAttempts {
					modalConnectAttempts++
					backoff := time.Duration(backoffMultiplier) * time.Second
					logger.Info("reconnecting to Modal", "sessionID", t.sessionID, "attempt", modalConnectAttempts, "backoff_ms", backoff.Milliseconds())
					time.Sleep(backoff)
					backoffMultiplier = math.Min(backoffMultiplier*float64(backoffBase), 60)
					if err := t.modalClient.Connect(t.ctx); err != nil {
						logger.Error("reconnect failed", "sessionID", t.sessionID, "error", err)
					} else {
						modalConnectAttempts = 0
						backoffMultiplier = 1.0
						logger.Info("reconnected to Modal", "sessionID", t.sessionID)
					}
				}
			}

		// Send processed audio chunks to Modal
		case chunk := <-t.audioOutCh:
			audioChunkCount = t.sendAudioChunk(chunk, audioChunkCount, logger)
		}
	}
}

// processAudioLoop reads decoded float32 PCM frames, resamples, and outputs chunks
func (t *Transcriber) processAudioLoop(logger *slog.Logger) {
	defer t.wg.Done()
	defer close(t.audioDoneCh)

	frameCount := 0
	for {
		select {
		case <-t.ctx.Done():
			t.drainAudioInput(logger, &frameCount)
			t.flushAudioCacheTail(logger)
			return

		case frame := <-t.audioInputCh:
			t.processInputFrame(frame, logger, &frameCount)
		}
	}
}

// broadcastMinInterval is the minimum time between non-final broadcast sends.
// Keep this low enough for near realtime captions while still rate-limiting.
const broadcastMinInterval = 80 * time.Millisecond

// shutdownDrainTimeout is the grace window used on stream shutdown to send
// buffered audio and receive any trailing tokens before final flush.
const shutdownDrainTimeout = 750 * time.Millisecond

// shutdownAudioDoneTimeout bounds how long we wait for the audio processing
// goroutine to flush cached samples after cancellation.
const shutdownAudioDoneTimeout = 250 * time.Millisecond

// handleTranscript processes a transcript from Modal and sends to HPB.
// Tokens are accumulated into a buffer and sent as the growing utterance text.
// On vad_end (Final=true), the full accumulated text is sent with final=true and
// the buffer is reset for the next utterance.
// Non-final broadcasts are throttled for client stability.
func (t *Transcriber) handleTranscript(hpbClient *hpb.Client, roomToken string, transcript modal.Transcript, logger *slog.Logger) {
	if transcript.Final {
		// VAD end: finalize the current utterance
		if t.pendingText != "" {
			logger.Info("transcript finalized", "sessionID", t.sessionID, "chars", len(t.pendingText))
			if t.broadcast != nil {
				t.broadcast(t.pendingText, true)
				t.lastBroadcast = time.Now()
			}
			t.pendingText = ""
			t.broadcastDirty = false
		}
		return
	}

	if transcript.Text == "" {
		return
	}

	// Accumulate token into pending utterance
	t.pendingText += transcript.Text
	t.broadcastDirty = true

	logger.Debug("transcript token", "sessionID", t.sessionID, "token", transcript.Text, "pendingChars", len(t.pendingText))

	if t.broadcast != nil {
		now := time.Now()
		if now.Sub(t.lastBroadcast) >= broadcastMinInterval {
			t.broadcast(t.pendingText, false)
			t.lastBroadcast = now
			t.broadcastDirty = false
		}
	}
}

// flushPendingBroadcast sends any accumulated but unsent transcript text.
// Called periodically from the main transcriber loop to ensure text isn't
// left unsent when tokens arrive slower than the throttle interval.
func (t *Transcriber) flushPendingBroadcast(logger *slog.Logger) {
	if t.broadcastDirty && t.broadcast != nil && t.pendingText != "" {
		t.broadcast(t.pendingText, false)
		t.lastBroadcast = time.Now()
		t.broadcastDirty = false
	}
}

// flushPendingFinal emits any pending transcript as a final segment.
// This is used on shutdown/stream close to avoid dropping trailing words
// when no explicit vad_end marker arrives.
func (t *Transcriber) flushPendingFinal(logger *slog.Logger) {
	if t.pendingText == "" || t.broadcast == nil {
		return
	}

	logger.Info("flushing pending transcript on shutdown", "sessionID", t.sessionID, "chars", len(t.pendingText))
	t.broadcast(t.pendingText, true)
	t.lastBroadcast = time.Now()
	t.pendingText = ""
	t.broadcastDirty = false
}

// AddAudioFrame adds a decoded float32 PCM frame to the processing queue
func (t *Transcriber) AddAudioFrame(frame []float32) error {
	if t.ctx.Err() != nil {
		return fmt.Errorf("transcriber context cancelled")
	}

	select {
	case t.audioInputCh <- frame:
		return nil
	case <-t.ctx.Done():
		return fmt.Errorf("transcriber context cancelled")
	default:
		// Channel full - drop frame (bounded queue backpressure)
		return fmt.Errorf("audio input channel full")
	}
}

// Close shuts down the transcriber
func (t *Transcriber) Close() error {
	t.cancel()
	t.wg.Wait()
	return nil
}

// drainOnShutdown performs a best-effort flush of queued audio and trailing
// transcript tokens before emitting the final segment.
func (t *Transcriber) drainOnShutdown(hpbClient *hpb.Client, roomToken string, logger *slog.Logger) {
	if t.audioDoneCh != nil {
		select {
		case <-t.audioDoneCh:
		case <-time.After(shutdownAudioDoneTimeout):
			logger.Debug("audio processing drain timeout", "sessionID", t.sessionID)
		}
	}

	timer := time.NewTimer(shutdownDrainTimeout)
	defer timer.Stop()

	audioChunkCount := 0
	for {
		select {
		case chunk := <-t.audioOutCh:
			audioChunkCount = t.sendAudioChunk(chunk, audioChunkCount, logger)
		case transcript := <-t.modalClient.TranscriptChan():
			t.handleTranscript(hpbClient, roomToken, transcript, logger)
		case err := <-t.modalClient.ErrorChan():
			logger.Debug("Modal error during shutdown drain", "sessionID", t.sessionID, "error", err)
		case <-timer.C:
			t.flushPendingFinal(logger)
			return
		}
	}
}

// processInputFrame resamples and chunk-buffers one decoded PCM frame.
func (t *Transcriber) processInputFrame(frame []float32, logger *slog.Logger, frameCount *int) {
	if len(frame) == 0 {
		return
	}

	*frameCount = *frameCount + 1
	if *frameCount%500 == 1 {
		logger.Debug("processing audio frame", "sessionID", t.sessionID, "samples", len(frame), "frameCount", *frameCount)
	}

	// Resample if needed (48kHz → 24kHz)
	resampled, err := t.audioPipe.ProcessFrame(frame)
	if err != nil {
		logger.Debug("resample error", "sessionID", t.sessionID, "error", err)
		return
	}

	// Buffer into 80ms chunks (matching backend stream expectations)
	chunks := t.audioCache.Add(resampled)
	t.enqueueAudioChunks(chunks, logger)
}

// drainAudioInput processes frames that were already queued at cancellation.
func (t *Transcriber) drainAudioInput(logger *slog.Logger, frameCount *int) {
	for {
		select {
		case frame := <-t.audioInputCh:
			t.processInputFrame(frame, logger, frameCount)
		default:
			return
		}
	}
}

// flushAudioCacheTail emits a final partial audio chunk (if any) so trailing
// phonemes aren't dropped when the stream ends mid-chunk.
func (t *Transcriber) flushAudioCacheTail(logger *slog.Logger) {
	tail := t.audioCache.Flush()
	if len(tail) == 0 {
		return
	}
	select {
	case t.audioOutCh <- tail:
	case <-t.ctx.Done():
	default:
		logger.Debug("audio output channel full, dropping flushed tail", "sessionID", t.sessionID, "samples", len(tail))
	}
}

// enqueueAudioChunks pushes processed chunks to the modal output queue.
func (t *Transcriber) enqueueAudioChunks(chunks [][]float32, logger *slog.Logger) {
	for _, chunk := range chunks {
		select {
		case t.audioOutCh <- chunk:
		case <-t.ctx.Done():
			return
		default:
			// Drop if output channel is full (backpressure)
			logger.Debug("audio output channel full, dropping chunk", "sessionID", t.sessionID)
		}
	}
}

// sendAudioChunk forwards one processed audio chunk to STT if connected.
func (t *Transcriber) sendAudioChunk(chunk []float32, audioChunkCount int, logger *slog.Logger) int {
	if len(chunk) == 0 || !t.modalClient.IsConnected() {
		return audioChunkCount
	}

	audioChunkCount++
	if audioChunkCount%100 == 1 {
		var sumSq float64
		var peak float32
		for _, s := range chunk {
			sumSq += float64(s) * float64(s)
			if s > peak {
				peak = s
			} else if -s > peak {
				peak = -s
			}
		}
		rms := math.Sqrt(sumSq / float64(len(chunk)))
		var rmsDB float64
		if rms > 0 {
			rmsDB = 20 * math.Log10(rms)
		} else {
			rmsDB = -999
		}
		logger.Info("audio level to Modal", "sessionID", t.sessionID,
			"rmsDB", fmt.Sprintf("%.1f", rmsDB), "peak", fmt.Sprintf("%.4f", peak),
			"samples", len(chunk), "chunkNum", audioChunkCount)
	}

	if err := t.modalClient.SendAudio(chunk); err != nil {
		logger.Debug("failed to send audio to Modal", "sessionID", t.sessionID, "error", err)
	} else if audioChunkCount <= 5 {
		logger.Debug("audio chunk sent to Modal", "sessionID", t.sessionID, "samples", len(chunk))
	}
	return audioChunkCount
}
