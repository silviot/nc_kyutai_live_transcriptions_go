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

	// Connect Modal
	if err := t.modalClient.Connect(t.ctx); err != nil {
		logger.Error("failed to connect to Modal", "sessionID", t.sessionID, "error", err)
		return
	}
	defer t.modalClient.Close()

	// Start audio processing goroutine
	t.wg.Add(1)
	go t.processAudioLoop(logger)

	// Process transcripts and handle reconnection
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
				if err := t.modalClient.SendAudio(chunk); err != nil {
					logger.Debug("failed to send audio to Modal", "sessionID", t.sessionID, "error", err)
				}
			}
		}
	}
}

// processAudioLoop reads raw audio frames, processes them, and outputs chunks
func (t *Transcriber) processAudioLoop(logger *slog.Logger) {
	defer t.wg.Done()

	for {
		select {
		case <-t.ctx.Done():
			return

		case frame := <-t.audioInputCh:
			if len(frame) == 0 {
				continue
			}

			// Convert int16 to float32
			float32Frame := int16ToFloat32(frame)

			// Resample if needed (48kHz → 24kHz)
			resampled, err := t.audioPipe.ProcessFrame(float32Frame)
			if err != nil {
				logger.Debug("resample error", "sessionID", t.sessionID, "error", err)
				continue
			}

			// Buffer into 200ms chunks
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

// int16ToFloat32 converts int16 PCM samples to float32 [-1.0, 1.0]
func int16ToFloat32(samples []int16) []float32 {
	result := make([]float32, len(samples))
	for i, sample := range samples {
		result[i] = float32(sample) / 32768.0
	}
	return result
}

// handleTranscript processes a transcript from Modal and sends to HPB
func (t *Transcriber) handleTranscript(hpbClient *hpb.Client, roomToken string, transcript modal.Transcript, logger *slog.Logger) {
	if transcript.Text == "" {
		return
	}

	logger.Debug("transcript received", "sessionID", t.sessionID, "text", transcript.Text, "final", transcript.Final)

	// Send to HPB as closed caption message
	msg := hpb.MessageMessage{
		Type:      "message",
		To:        "", // Broadcast to room
		RoomToken: roomToken,
		Data: map[string]interface{}{
			"type":  "transcription",
			"from":  t.sessionID,
			"text":  transcript.Text,
			"final": transcript.Final,
		},
	}

	if err := hpbClient.SendMessage(msg); err != nil {
		logger.Error("failed to send transcript to HPB", "sessionID", t.sessionID, "error", err)
	}
}

// AddAudioFrame adds a raw audio frame from WebRTC to the processing queue
func (t *Transcriber) AddAudioFrame(frame []int16) error {
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
