package session

import (
	"log/slog"
	"testing"
	"time"

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

	// Feed multiple tokens rapidly (within broadcastMinInterval)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "Hello"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " world"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "!"}, logger)

	// pendingText should accumulate all tokens
	if tr.pendingText != "Hello world!" {
		t.Errorf("pendingText = %q, want %q", tr.pendingText, "Hello world!")
	}

	// First token broadcasts immediately (lastBroadcast is zero),
	// subsequent tokens within 2s are throttled
	if len(broadcasts) != 1 {
		t.Fatalf("expected 1 broadcast (first token, rest throttled), got %d", len(broadcasts))
	}
	if broadcasts[0].text != "Hello" || broadcasts[0].final != false {
		t.Errorf("broadcast[0] = (%q, %v), want (Hello, false)", broadcasts[0].text, broadcasts[0].final)
	}

	// broadcastDirty should be true (unsent accumulated text)
	if !tr.broadcastDirty {
		t.Error("expected broadcastDirty=true after throttled tokens")
	}

	// flushPendingBroadcast sends the accumulated text (it doesn't check throttle)
	tr.flushPendingBroadcast(logger)

	if len(broadcasts) != 2 {
		t.Fatalf("expected 2 broadcasts after flush, got %d", len(broadcasts))
	}
	if broadcasts[1].text != "Hello world!" || broadcasts[1].final != false {
		t.Errorf("broadcast[1] = (%q, %v), want (Hello world!, false)", broadcasts[1].text, broadcasts[1].final)
	}
	if tr.broadcastDirty {
		t.Error("expected broadcastDirty=false after flush")
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

	// Should have 2 broadcasts: 1 non-final (first token) + 1 final (vad_end)
	// Second token is throttled, but vad_end always broadcasts immediately
	if len(broadcasts) != 2 {
		t.Fatalf("expected 2 broadcasts (1 throttled token + 1 final), got %d", len(broadcasts))
	}

	// Last broadcast should be final with full accumulated text
	last := broadcasts[len(broadcasts)-1]
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

func TestHandleTranscript_ThrottlingBehavior(t *testing.T) {
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

	// First token: broadcasts immediately (lastBroadcast is zero)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: "A"}, logger)
	if len(broadcasts) != 1 {
		t.Fatalf("first token should broadcast immediately, got %d broadcasts", len(broadcasts))
	}

	// Rapid tokens: throttled (within 2s window)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " B"}, logger)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " C"}, logger)
	if len(broadcasts) != 1 {
		t.Fatalf("rapid tokens should be throttled, got %d broadcasts", len(broadcasts))
	}

	// Simulate throttle window passing
	tr.lastBroadcast = time.Now().Add(-3 * time.Second)
	tr.handleTranscript(nil, "room1", modal.Transcript{Type: "token", Text: " D"}, logger)

	if len(broadcasts) != 2 {
		t.Fatalf("token after throttle window should broadcast, got %d broadcasts", len(broadcasts))
	}
	if broadcasts[1].text != "A B C D" {
		t.Errorf("throttled broadcast text = %q, want %q", broadcasts[1].text, "A B C D")
	}
}

func TestFlushPendingBroadcast(t *testing.T) {
	var broadcasts []string

	tr := testTranscriber(func(text string, final bool) {
		broadcasts = append(broadcasts, text)
	})

	logger := slog.Default()

	// Nothing to flush
	tr.flushPendingBroadcast(logger)
	if len(broadcasts) != 0 {
		t.Error("flush with no pending text should not broadcast")
	}

	// Set up pending text
	tr.pendingText = "accumulated text"
	tr.broadcastDirty = true
	tr.lastBroadcast = time.Time{} // allow flush

	tr.flushPendingBroadcast(logger)
	if len(broadcasts) != 1 || broadcasts[0] != "accumulated text" {
		t.Errorf("expected flush to broadcast 'accumulated text', got %v", broadcasts)
	}
	if tr.broadcastDirty {
		t.Error("broadcastDirty should be false after flush")
	}
}
