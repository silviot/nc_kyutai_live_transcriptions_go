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

	// Start audio pipeline
	t.audioPipe.Start()
	defer t.audioPipe.Close()

	// Create audio frame channel from WebRTC
	// In a real implementation, this would be properly wired from the peer connection
	// For now, we'll set up a simple flow

	// Process transcripts and audio in parallel
	transcriptTicker := time.NewTicker(100 * time.Millisecond)
	defer transcriptTicker.Stop()

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

		// Periodically check pipeline and send audio
		case <-transcriptTicker.C:
			// Send audio to Modal if available
			if chunk, ok := t.peekAudioChunk(); ok {
				if err := t.modalClient.SendAudio(chunk); err != nil {
					logger.Debug("failed to send audio to Modal", "sessionID", t.sessionID, "error", err)
				}
			}
		}
	}
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
			"type": "transcription",
			"from": t.sessionID,
			"text": transcript.Text,
			"final": transcript.Final,
		},
	}

	if err := hpbClient.SendMessage(msg); err != nil {
		logger.Error("failed to send transcript to HPB", "sessionID", t.sessionID, "error", err)
	}
}

// peekAudioChunk retrieves the next audio chunk from the buffer
func (t *Transcriber) peekAudioChunk() ([]float32, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// TODO: This is a placeholder. In a real implementation,
	// audio would flow from WebRTC → audio pipeline → buffer
	// For now, return empty chunk to keep the loop running
	return []float32{}, false
}

// AddAudioFrame adds an audio frame from WebRTC
func (t *Transcriber) AddAudioFrame(frame []int16) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ctx.Err() != nil {
		return fmt.Errorf("transcriber context cancelled")
	}

	// TODO: Wire this to the audio pipeline input
	// chunks := t.audioCache.Add(frame)
	// for _, chunk := range chunks {
	//     // Send chunk to transcriber
	// }

	return nil
}

// Close shuts down the transcriber
func (t *Transcriber) Close() error {
	t.cancel()
	t.wg.Wait()
	return nil
}
