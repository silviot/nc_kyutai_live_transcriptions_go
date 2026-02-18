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

	for {
		select {
		case <-t.ctx.Done():
			logger.Debug("transcriber shutting down", "sessionID", t.sessionID)
			return

		// Handle transcript results from Modal
		case transcript := <-t.modalClient.TranscriptChan():
			t.handleTranscript(hpbClient, roomToken, transcript, logger)

		// Handle errors from Modal
		case err := <-t.modalClient.ErrorChan():
			logger.Warn("Modal error", "sessionID", t.sessionID, "error", err)
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

// handleTranscript processes a transcript from Modal and sends to HPB.
// Tokens are accumulated into a buffer and sent as the growing utterance text.
// On vad_end (Final=true), the accumulated text is sent with final=true and
// the buffer is reset for the next utterance.
func (t *Transcriber) handleTranscript(hpbClient *hpb.Client, roomToken string, transcript modal.Transcript, logger *slog.Logger) {
	if transcript.Final {
		// VAD end: finalize the current utterance
		if t.pendingText != "" {
			logger.Info("transcript finalized", "sessionID", t.sessionID, "text", t.pendingText)
			if t.broadcast != nil {
				t.broadcast(t.pendingText, true)
			}
			t.pendingText = ""
		}
		return
	}

	if transcript.Text == "" {
		return
	}

	// Accumulate token into pending utterance
	t.pendingText += transcript.Text

	logger.Info("transcript token", "sessionID", t.sessionID, "token", transcript.Text, "accumulated", t.pendingText)

	if t.broadcast != nil {
		t.broadcast(t.pendingText, false)
	}
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
