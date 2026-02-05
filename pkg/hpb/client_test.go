package hpb

import (
	"context"
	"log/slog"
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
	// Test the hmacSHA256 function
	secret := "test-secret"
	nonce := "test-nonce-12345"

	sig := hmacSHA256(secret, nonce)

	// Verify it's a valid hex string (64 chars for SHA256)
	if len(sig) != 64 {
		t.Errorf("expected 64 char hex signature, got %d chars", len(sig))
	}

	// Verify deterministic output
	sig2 := hmacSHA256(secret, nonce)
	if sig != sig2 {
		t.Errorf("HMAC should be deterministic: %s != %s", sig, sig2)
	}

	// Verify different inputs produce different signatures
	sig3 := hmacSHA256(secret, "different-nonce")
	if sig == sig3 {
		t.Errorf("different inputs should produce different signatures")
	}
}

func TestGenerateNonce(t *testing.T) {
	nonce1 := generateNonce()
	nonce2 := generateNonce()

	// Should be 64 chars (32 bytes hex encoded)
	if len(nonce1) != 64 {
		t.Errorf("expected 64 char nonce, got %d chars", len(nonce1))
	}

	// Should be unique
	if nonce1 == nonce2 {
		t.Error("nonces should be unique")
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

func TestBackendURLDerivation(t *testing.T) {
	tests := []struct {
		hpbURL      string
		expectedURL string
	}{
		{
			"wss://cloud.example.com/standalone-signaling/spreed",
			"https://cloud.example.com/",
		},
		{
			"ws://localhost:8080/standalone-signaling/spreed",
			"http://localhost:8080/",
		},
		{
			"wss://test.com/standalone-signaling",
			"https://test.com/",
		},
	}

	for _, tt := range tests {
		cfg := Config{
			HPBURL: tt.hpbURL,
			Secret: "test-secret",
		}
		client := NewClient(cfg)
		if client.backendURL != tt.expectedURL {
			t.Errorf("for %s: expected backend %s, got %s",
				tt.hpbURL, tt.expectedURL, client.backendURL)
		}
	}
}
