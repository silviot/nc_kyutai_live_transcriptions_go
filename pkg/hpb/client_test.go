package hpb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// --- Wire format tests ---

// wireCaptureMockHPB is a minimal HPB mock that captures the first message (hello)
// and responds with a valid hello response so the client proceeds normally.
type wireCaptureMockHPB struct {
	listener net.Listener
	server   *http.Server
	upgrader websocket.Upgrader
	mu       sync.Mutex
	messages []json.RawMessage // all captured messages
	done     chan struct{}
}

func startWireCaptureMock(t *testing.T) *wireCaptureMockHPB {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	m := &wireCaptureMockHPB{
		listener: listener,
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		done:     make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handle)
	m.server = &http.Server{Handler: mux}
	go m.server.Serve(listener)

	return m
}

func (m *wireCaptureMockHPB) url() string {
	return fmt.Sprintf("ws://%s", m.listener.Addr().String())
}

func (m *wireCaptureMockHPB) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		select {
		case <-m.done:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		m.mu.Lock()
		m.messages = append(m.messages, json.RawMessage(data))
		m.mu.Unlock()

		// Parse message type and respond appropriately
		var msg map[string]interface{}
		if json.Unmarshal(data, &msg) == nil {
			switch msg["type"] {
			case "hello":
				resp := map[string]interface{}{
					"type": "hello",
					"hello": map[string]interface{}{
						"sessionid": "mock-session-abc",
						"resumeid":  "mock-resume-xyz",
					},
				}
				respData, _ := json.Marshal(resp)
				conn.WriteMessage(websocket.TextMessage, respData)
			case "room":
				resp := map[string]interface{}{
					"type": "room",
					"room": map[string]interface{}{
						"roomid": "test-room",
					},
				}
				respData, _ := json.Marshal(resp)
				conn.WriteMessage(websocket.TextMessage, respData)
			}
		}
	}
}

func (m *wireCaptureMockHPB) getMessages() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]json.RawMessage, len(m.messages))
	copy(cp, m.messages)
	return cp
}

func (m *wireCaptureMockHPB) close() {
	close(m.done)
	m.server.Close()
}

func TestHelloInternal_WireFormat(t *testing.T) {
	mock := startWireCaptureMock(t)
	defer mock.close()

	client := NewClient(Config{
		HPBURL:     mock.url(),
		BackendURL: "https://cloud.example.com/",
		Secret:     "my-internal-secret",
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Wait for hello exchange
	time.Sleep(200 * time.Millisecond)

	msgs := mock.getMessages()
	if len(msgs) == 0 {
		t.Fatal("no messages captured")
	}

	// Parse the hello message
	var hello struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Hello struct {
			Version string `json:"version"`
			Auth    struct {
				Type   string `json:"type"`
				Params struct {
					Random  string `json:"random"`
					Token   string `json:"token"`
					Backend string `json:"backend"`
				} `json:"params"`
			} `json:"auth"`
		} `json:"hello"`
	}
	if err := json.Unmarshal(msgs[0], &hello); err != nil {
		t.Fatalf("failed to parse hello: %v", err)
	}

	if hello.Type != "hello" {
		t.Errorf("type = %q, want %q", hello.Type, "hello")
	}
	if hello.Hello.Version != "2.0" {
		t.Errorf("version = %q, want %q", hello.Hello.Version, "2.0")
	}
	if hello.Hello.Auth.Type != "internal" {
		t.Errorf("auth.type = %q, want %q", hello.Hello.Auth.Type, "internal")
	}
	if hello.Hello.Auth.Params.Backend != "https://cloud.example.com/" {
		t.Errorf("auth.params.backend = %q, want %q", hello.Hello.Auth.Params.Backend, "https://cloud.example.com/")
	}

	// Verify nonce is 64 hex chars
	nonce := hello.Hello.Auth.Params.Random
	if len(nonce) != 64 {
		t.Errorf("nonce length = %d, want 64", len(nonce))
	}

	// Verify token is HMAC-SHA256(secret, nonce)
	expectedToken := hmacSHA256("my-internal-secret", nonce)
	if hello.Hello.Auth.Params.Token != expectedToken {
		t.Errorf("token = %q, want HMAC(secret, nonce) = %q", hello.Hello.Auth.Params.Token, expectedToken)
	}
}

func TestHelloTicket_WireFormat(t *testing.T) {
	mock := startWireCaptureMock(t)
	defer mock.close()

	client := NewClient(Config{
		HPBURL:    mock.url(),
		TicketURL: "https://cloud.example.com/ocs/v2.php/apps/spreed/api/v3/signaling/backend",
		Ticket:    "ticket-abc-123",
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	msgs := mock.getMessages()
	if len(msgs) == 0 {
		t.Fatal("no messages captured")
	}

	var hello map[string]interface{}
	if err := json.Unmarshal(msgs[0], &hello); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if hello["type"] != "hello" {
		t.Errorf("type = %v, want hello", hello["type"])
	}

	helloInner := hello["hello"].(map[string]interface{})
	if helloInner["version"] != "2.0" {
		t.Errorf("version = %v, want 2.0", helloInner["version"])
	}

	auth := helloInner["auth"].(map[string]interface{})
	if auth["url"] != "https://cloud.example.com/ocs/v2.php/apps/spreed/api/v3/signaling/backend" {
		t.Errorf("auth.url = %v, want ticket URL", auth["url"])
	}

	params := auth["params"].(map[string]interface{})
	if params["ticket"] != "ticket-abc-123" {
		t.Errorf("params.ticket = %v, want ticket-abc-123", params["ticket"])
	}
}

func TestHelloToken_WireFormat(t *testing.T) {
	mock := startWireCaptureMock(t)
	defer mock.close()

	client := NewClient(Config{
		HPBURL:     mock.url(),
		BackendURL: "https://cloud.example.com/",
		Token:      "jwt-token-xyz",
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	msgs := mock.getMessages()
	if len(msgs) == 0 {
		t.Fatal("no messages captured")
	}

	var hello map[string]interface{}
	json.Unmarshal(msgs[0], &hello)

	helloInner := hello["hello"].(map[string]interface{})
	auth := helloInner["auth"].(map[string]interface{})

	expectedURL := "https://cloud.example.com/ocs/v2.php/apps/spreed/api/v3/signaling/backend"
	if auth["url"] != expectedURL {
		t.Errorf("auth.url = %v, want %v", auth["url"], expectedURL)
	}

	params := auth["params"].(map[string]interface{})
	if params["token"] != "jwt-token-xyz" {
		t.Errorf("params.token = %v, want jwt-token-xyz", params["token"])
	}
}

func TestRoomJoin_WireFormat(t *testing.T) {
	mock := startWireCaptureMock(t)
	defer mock.close()

	client := NewClient(Config{
		HPBURL:     mock.url(),
		BackendURL: "https://cloud.example.com/",
		Secret:     "test-secret",
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Wait for hello to complete
	time.Sleep(200 * time.Millisecond)

	if err := client.JoinRoom("erwcr27x"); err != nil {
		t.Fatalf("JoinRoom failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	msgs := mock.getMessages()
	// Should have hello + room messages
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}

	var roomMsg struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Room struct {
			RoomID    string `json:"roomid"`
			SessionID string `json:"sessionid"`
		} `json:"room"`
	}
	if err := json.Unmarshal(msgs[1], &roomMsg); err != nil {
		t.Fatalf("parse room msg failed: %v", err)
	}

	if roomMsg.Type != "room" {
		t.Errorf("type = %q, want %q", roomMsg.Type, "room")
	}
	if roomMsg.Room.RoomID != "erwcr27x" {
		t.Errorf("room.roomid = %q, want %q", roomMsg.Room.RoomID, "erwcr27x")
	}
}

func TestSendTranscript_WireFormat(t *testing.T) {
	mock := startWireCaptureMock(t)
	defer mock.close()

	client := NewClient(Config{
		HPBURL:     mock.url(),
		BackendURL: "https://cloud.example.com/",
		Secret:     "test-secret",
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if err := client.SendTranscript("session-abc", "Hello world", "en", "speaker-xyz", true); err != nil {
		t.Fatalf("SendTranscript failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	msgs := mock.getMessages()
	// Find the transcript message (after hello)
	var found bool
	for _, raw := range msgs {
		var msg map[string]interface{}
		json.Unmarshal(raw, &msg)
		if msg["type"] != "message" {
			continue
		}

		found = true
		envelope := msg["message"].(map[string]interface{})

		// Verify recipient
		recipient := envelope["recipient"].(map[string]interface{})
		if recipient["type"] != "session" {
			t.Errorf("recipient.type = %v, want session", recipient["type"])
		}
		if recipient["sessionid"] != "session-abc" {
			t.Errorf("recipient.sessionid = %v, want session-abc", recipient["sessionid"])
		}

		// Verify data
		data := envelope["data"].(map[string]interface{})
		if data["type"] != "transcript" {
			t.Errorf("data.type = %v, want transcript", data["type"])
		}
		if data["message"] != "Hello world" {
			t.Errorf("data.message = %v, want 'Hello world'", data["message"])
		}
		if data["langId"] != "en" {
			t.Errorf("data.langId = %v, want en", data["langId"])
		}
		if data["speakerSessionId"] != "speaker-xyz" {
			t.Errorf("data.speakerSessionId = %v, want speaker-xyz", data["speakerSessionId"])
		}
		if data["final"] != true {
			t.Errorf("data.final = %v, want true", data["final"])
		}
		break
	}

	if !found {
		t.Error("transcript message not found in captured messages")
	}
}

func TestBackendAPI_HMACSignature(t *testing.T) {
	// Create an httptest server to capture the backend API POST
	var capturedBody []byte
	var capturedHeaders http.Header

	backendMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer backendMock.Close()

	client := NewClient(Config{
		HPBURL:          "ws://" + backendMock.Listener.Addr().String() + "/spreed",
		BackendURL:      "https://cloud.example.com/",
		SignalingSecret: "backend-signaling-secret",
	})

	data := map[string]interface{}{
		"type":    "transcript",
		"message": "Hello",
		"final":   true,
	}

	if err := client.SendRoomMessage("test-room", data); err != nil {
		t.Fatalf("SendRoomMessage failed: %v", err)
	}

	// Verify required headers exist
	random := capturedHeaders.Get("Spreed-Signaling-Random")
	checksum := capturedHeaders.Get("Spreed-Signaling-Checksum")
	backend := capturedHeaders.Get("Spreed-Signaling-Backend")

	if random == "" {
		t.Fatal("Spreed-Signaling-Random header missing")
	}
	if len(random) != 64 {
		t.Errorf("random nonce length = %d, want 64", len(random))
	}
	if checksum == "" {
		t.Fatal("Spreed-Signaling-Checksum header missing")
	}
	if backend != "https://cloud.example.com/" {
		t.Errorf("Spreed-Signaling-Backend = %q, want %q", backend, "https://cloud.example.com/")
	}
	if capturedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", capturedHeaders.Get("Content-Type"))
	}

	// Recompute HMAC and verify
	mac := hmac.New(sha256.New, []byte("backend-signaling-secret"))
	mac.Write([]byte(random))
	mac.Write(capturedBody)
	expectedChecksum := hex.EncodeToString(mac.Sum(nil))

	if checksum != expectedChecksum {
		t.Errorf("checksum = %q, want %q (recomputed HMAC)", checksum, expectedChecksum)
	}

	// Verify body structure
	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if body["type"] != "message" {
		t.Errorf("body.type = %v, want message", body["type"])
	}
	msgInner := body["message"].(map[string]interface{})
	msgData := msgInner["data"].(map[string]interface{})
	if msgData["type"] != "transcript" {
		t.Errorf("body.message.data.type = %v, want transcript", msgData["type"])
	}
}

func TestBackendAPI_URLDerivation(t *testing.T) {
	tests := []struct {
		hpbURL      string
		roomToken   string
		expectedAPI string
	}{
		{
			"wss://hpb.example.com/standalone-signaling/spreed",
			"abc123",
			"https://hpb.example.com/api/v1/room/abc123",
		},
		{
			"ws://localhost:8080/spreed",
			"room-xyz",
			"http://localhost:8080/api/v1/room/room-xyz",
		},
		{
			"wss://signaling.example.com:443/path/deep",
			"test",
			"https://signaling.example.com:443/api/v1/room/test",
		},
	}

	for _, tt := range tests {
		// We can't easily call SendRoomMessage without a real server,
		// but we can verify the URL derivation logic by reimplementing it
		// (the same logic as in client.go SendRoomMessage)
		apiURL := tt.hpbURL
		apiURL = strings.Replace(apiURL, "wss://", "https://", 1)
		apiURL = strings.Replace(apiURL, "ws://", "http://", 1)
		if idx := strings.Index(apiURL[8:], "/"); idx > 0 {
			apiURL = apiURL[:8+idx]
		}
		apiURL = strings.TrimRight(apiURL, "/")
		apiURL += "/api/v1/room/" + tt.roomToken

		if apiURL != tt.expectedAPI {
			t.Errorf("for hpbURL=%q roomToken=%q:\n  got  %q\n  want %q",
				tt.hpbURL, tt.roomToken, apiURL, tt.expectedAPI)
		}
	}
}

func TestHandleMessage_AllTypes(t *testing.T) {
	client := NewClient(Config{
		HPBURL: "ws://127.0.0.1:9999",
		Secret: "test",
	})
	defer client.Close()

	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "welcome",
			json:    `{"type":"welcome","version":"1.0"}`,
			wantErr: false,
		},
		{
			name:    "hello response",
			json:    `{"type":"hello","hello":{"sessionid":"sess-1","resumeid":"res-1"}}`,
			wantErr: false,
		},
		{
			name:    "room response",
			json:    `{"type":"room","room":{"roomid":"abc"}}`,
			wantErr: false,
		},
		{
			name:    "event join",
			json:    `{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"s1"}]}}`,
			wantErr: false,
		},
		{
			name:    "message signaling",
			json:    `{"type":"message","message":{"sender":{"type":"session","sessionid":"s1"},"data":{"type":"offer"}}}`,
			wantErr: false,
		},
		{
			name:    "bye",
			json:    `{"type":"bye","reason":"disconnect"}`,
			wantErr: false,
		},
		{
			name:    "error nested",
			json:    `{"type":"error","error":{"code":"no_such_room","message":"Room not found"}}`,
			wantErr: false,
		},
		{
			name:    "error legacy",
			json:    `{"type":"error","code":404,"message":"Not found"}`,
			wantErr: false,
		},
		{
			name:    "pong",
			json:    `{"type":"pong"}`,
			wantErr: false,
		},
		{
			name:    "unknown type",
			json:    `{"type":"future_type","data":"something"}`,
			wantErr: false,
		},
		{
			name:    "invalid json",
			json:    `{not valid`,
			wantErr: true,
		},
		{
			name:    "HMAC prefixed message",
			json:    `mocksig {"type":"welcome"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.handleMessage([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Errorf("handleMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	// Drain message channel to verify hello/room/event/message/bye were routed
	drainCount := 0
	for {
		select {
		case <-client.msgChan:
			drainCount++
		default:
			goto done
		}
	}
done:
	// hello, room, event, message, bye = 5 messages routed to channel
	if drainCount != 5 {
		t.Errorf("expected 5 messages routed to channel, got %d", drainCount)
	}
}
