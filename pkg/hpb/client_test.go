package hpb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)


func TestClientConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := Config{
		HPBURL: "ws://127.0.0.1:9999", // Invalid URL for now
		Secret: "test-secret",
		Logger: slog.Default(),
	}

	client := NewClient(cfg)
	defer client.Close()

	// Expect connection to fail (no server)
	err := client.Connect(ctx)
	if err == nil {
		t.Error("expected connection to fail")
	}
}

func TestHMACSignature(t *testing.T) {
	cfg := Config{
		HPBURL: "ws://127.0.0.1:9999",
		Secret: "test-secret",
		Logger: slog.Default(),
	}

	client := NewClient(cfg)

	msg := HelloMessage{
		Type:     "hello",
		ResumeID: "test-id",
	}
	data, _ := json.Marshal(msg)

	signed := client.addHMACSignature(data)

	// Parse signature
	parts := strings.SplitN(string(signed), " ", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid signed message format")
	}

	sig := parts[0]
	msgPart := []byte(parts[1])

	// Verify signature
	expectedH := hmac.New(sha256.New, []byte("test-secret"))
	expectedH.Write(msgPart)
	expectedSig := fmt.Sprintf("%x", expectedH.Sum(nil))

	if sig != expectedSig {
		t.Errorf("signature mismatch: got %s, want %s", sig, expectedSig)
	}
}

func TestMessageChannels(t *testing.T) {
	cfg := Config{
		HPBURL: "ws://127.0.0.1:9999",
		Secret: "test-secret",
		Logger: slog.Default(),
	}

	client := NewClient(cfg)
	defer client.Close()

	// Verify channels are accessible
	msgChan := client.MessageChan()
	errChan := client.ErrorChan()

	if msgChan == nil || errChan == nil {
		t.Error("channels should not be nil")
	}
}

func TestSessionID(t *testing.T) {
	cfg := Config{
		HPBURL: "ws://127.0.0.1:9999",
		Secret: "test-secret",
		Logger: slog.Default(),
	}

	client := NewClient(cfg)
	defer client.Close()

	// Initially empty
	if sid := client.SessionID(); sid != "" {
		t.Errorf("expected empty sessionID initially, got %s", sid)
	}
}

func TestIsConnected(t *testing.T) {
	cfg := Config{
		HPBURL: "ws://127.0.0.1:9999",
		Secret: "test-secret",
		Logger: slog.Default(),
	}

	client := NewClient(cfg)
	defer client.Close()

	// Should not be connected initially
	if client.IsConnected() {
		t.Error("expected disconnected state initially")
	}
}
