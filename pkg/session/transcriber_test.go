package session

import (
	"log/slog"
	"testing"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/modal"
)

// testTranscriber creates a minimal Transcriber suitable for testing handleTranscript.
func testTranscriber(broadcast func(text string, final bool)) *Transcriber {
	return &Transcriber{
		sessionID: "test-session",
		broadcast: broadcast,
	}
}

func TestHandleTranscript_TokenAccumulation(t *testing.T) {
	var broadcasts []struct {
		text  string
		final bool
	}

	tr := testTranscriber(func(text string, final bool) {
		broadcasts = append(broadcasts, struct {
			text  string
			final bool
		}{text, final})
	})

	logger := slog.Default()

	// Feed multiple tokens
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "Hello"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " world"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "!"}, logger)

	// pendingText should accumulate
	if tr.pendingText != "Hello world!" {
		t.Errorf("pendingText = %q, want %q", tr.pendingText, "Hello world!")
	}

	// Each token should trigger a broadcast with accumulated text, final=false
	if len(broadcasts) != 3 {
		t.Fatalf("expected 3 broadcasts, got %d", len(broadcasts))
	}
	if broadcasts[0].text != "Hello" || broadcasts[0].final != false {
		t.Errorf("broadcast[0] = (%q, %v), want (Hello, false)", broadcasts[0].text, broadcasts[0].final)
	}
	if broadcasts[1].text != "Hello world" || broadcasts[1].final != false {
		t.Errorf("broadcast[1] = (%q, %v), want (Hello world, false)", broadcasts[1].text, broadcasts[1].final)
	}
	if broadcasts[2].text != "Hello world!" || broadcasts[2].final != false {
		t.Errorf("broadcast[2] = (%q, %v), want (Hello world!, false)", broadcasts[2].text, broadcasts[2].final)
	}
}

func TestHandleTranscript_VADEndFinalizes(t *testing.T) {
	var broadcasts []struct {
		text  string
		final bool
	}

	tr := testTranscriber(func(text string, final bool) {
		broadcasts = append(broadcasts, struct {
			text  string
			final bool
		}{text, final})
	})

	logger := slog.Default()

	// Feed tokens then vad_end
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "Hello"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " world"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "vad_end", Final: true, VADEnd: true}, logger)

	// After vad_end, pendingText should be reset
	if tr.pendingText != "" {
		t.Errorf("pendingText = %q, want empty after vad_end", tr.pendingText)
	}

	// Should have 3 broadcasts: 2 tokens (partial) + 1 final
	if len(broadcasts) != 3 {
		t.Fatalf("expected 3 broadcasts, got %d", len(broadcasts))
	}

	// Last broadcast should be final with full accumulated text
	last := broadcasts[2]
	if last.text != "Hello world" {
		t.Errorf("final broadcast text = %q, want %q", last.text, "Hello world")
	}
	if last.final != true {
		t.Error("final broadcast should have final=true")
	}
}

func TestHandleTranscript_EmptyTokenIgnored(t *testing.T) {
	broadcastCount := 0

	tr := testTranscriber(func(text string, final bool) {
		broadcastCount++
	})

	logger := slog.Default()

	// Feed empty text token
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: ""}, logger)

	if broadcastCount != 0 {
		t.Errorf("expected 0 broadcasts for empty token, got %d", broadcastCount)
	}
	if tr.pendingText != "" {
		t.Errorf("pendingText = %q, want empty", tr.pendingText)
	}
}

func TestHandleTranscript_VADEndWithNoText(t *testing.T) {
	broadcastCount := 0

	tr := testTranscriber(func(text string, final bool) {
		broadcastCount++
	})

	logger := slog.Default()

	// Send vad_end without any prior tokens
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "vad_end", Final: true, VADEnd: true}, logger)

	// Should not broadcast when there's no accumulated text
	if broadcastCount != 0 {
		t.Errorf("expected 0 broadcasts for vad_end with no text, got %d", broadcastCount)
	}
}

func TestHandleTranscript_NilBroadcast(t *testing.T) {
	// Verify no panic when broadcast is nil
	tr := testTranscriber(nil)
	logger := slog.Default()

	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "hello"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "vad_end", Final: true}, logger)

	// Just verify no panic occurred
	if tr.pendingText != "" {
		t.Errorf("pendingText = %q, want empty after vad_end", tr.pendingText)
	}
}

func TestHandleTranscript_MultipleUtterances(t *testing.T) {
	var finals []string

	tr := testTranscriber(func(text string, final bool) {
		if final {
			finals = append(finals, text)
		}
	})

	logger := slog.Default()

	// First utterance
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "First"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "vad_end", Final: true}, logger)

	// Second utterance
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "Second"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "vad_end", Final: true}, logger)

	if len(finals) != 2 {
		t.Fatalf("expected 2 final broadcasts, got %d", len(finals))
	}
	if finals[0] != "First" {
		t.Errorf("first utterance = %q, want %q", finals[0], "First")
	}
	if finals[1] != "Second" {
		t.Errorf("second utterance = %q, want %q", finals[1], "Second")
	}
}
