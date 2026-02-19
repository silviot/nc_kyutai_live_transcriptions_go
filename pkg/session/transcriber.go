package session

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

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
			if len(chunk) > 0 {
				if !t.modalClient.IsConnected() {
					// Drop audio until Modal is ready
					continue
				}

				// Log audio level periodically for debugging
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
			}
		}
	}
}

// processAudioLoop reads decoded float32 PCM frames, resamples, and outputs chunks
func (t *Transcriber) processAudioLoop(logger *slog.Logger) {
	defer t.wg.Done()

	frameCount := 0
	for {
		select {
		case <-t.ctx.Done():
			return

		case frame := <-t.audioInputCh:
			if len(frame) == 0 {
				continue
			}

			frameCount++
			if frameCount%500 == 1 {
				logger.Debug("processing audio frame", "sessionID", t.sessionID, "samples", len(frame), "frameCount", frameCount)
			}

			// Resample if needed (48kHz → 24kHz)
			resampled, err := t.audioPipe.ProcessFrame(frame)
			if err != nil {
				logger.Debug("resample error", "sessionID", t.sessionID, "error", err)
				continue
			}

			// Buffer into 80ms chunks (matching Modal Rust server expectations)
			chunks := t.audioCache.Add(resampled)
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
	}
}

// broadcastMinInterval is the minimum time between non-final broadcast sends.
// Keep this low enough for near realtime captions while still rate-limiting.
const broadcastMinInterval = 120 * time.Millisecond

// maxPartialBroadcastChars bounds non-final transcript payload size.
// Some clients struggle to continuously update very large partial strings.
const maxPartialBroadcastChars = 320

// handleTranscript processes a transcript from Modal and sends to HPB.
// Tokens are accumulated into a buffer and sent as the growing utterance text.
// On vad_end (Final=true), the full accumulated text is sent with final=true and
// the buffer is reset for the next utterance.
// Non-final broadcasts are throttled and bounded in size for client stability.
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
			t.broadcast(t.currentPartialText(), false)
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
		t.broadcast(t.currentPartialText(), false)
		t.lastBroadcast = time.Now()
		t.broadcastDirty = false
	}
}

// currentPartialText returns the bounded non-final text to broadcast.
// Keep the most recent tail when pending text grows too large.
func (t *Transcriber) currentPartialText() string {
	if len(t.pendingText) <= maxPartialBroadcastChars {
		return t.pendingText
	}

	start := len(t.pendingText) - maxPartialBroadcastChars
	for start < len(t.pendingText) && !utf8.RuneStart(t.pendingText[start]) {
		start++
	}
	tail := strings.TrimLeft(t.pendingText[start:], " ")
	return "..." + tail
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
