package modal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockModalServer simulates a Modal STT WebSocket endpoint for testing.
type mockModalServer struct {
	server       *httptest.Server
	upgrader     websocket.Upgrader
	mu           sync.Mutex
	lastHeaders  http.Header      // headers from most recent connection
	received     [][]byte         // raw binary messages received
	conn         *websocket.Conn  // most recent client connection
	onConnect    func(http.Header) // optional callback on connect
}

func newMockModalServer() *mockModalServer {
	m := &mockModalServer{
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockModalServer) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.lastHeaders = r.Header.Clone()
	if m.onConnect != nil {
		m.onConnect(r.Header)
	}
	m.mu.Unlock()

	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	defer conn.Close()
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			m.mu.Lock()
			m.received = append(m.received, data)
			m.mu.Unlock()
		}
	}
}

func (m *mockModalServer) wsURL() string {
	return "ws" + strings.TrimPrefix(m.server.URL, "http")
}

func (m *mockModalServer) sendJSON(msg interface{}) error {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	data, _ := json.Marshal(msg)
	return c.WriteMessage(websocket.TextMessage, data)
}

func (m *mockModalServer) sendPing() error {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
}

func (m *mockModalServer) close() {
	m.mu.Lock()
	if m.conn != nil {
		m.conn.Close()
	}
	m.mu.Unlock()
	m.server.Close()
}

// patchClientURL overrides the client's connection to point at our mock.
// We connect manually because Connect() hardcodes the Modal URL.
func connectToMock(c *Client, url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	headers := map[string][]string{
		"Modal-Key":    {c.key},
		"Modal-Secret": {c.secret},
	}

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(c.ctx, url, headers)
	if err != nil {
		return err
	}

	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(
			websocket.PongMessage,
			[]byte(appData),
			time.Now().Add(10*time.Second),
		)
	})

	c.conn = conn
	c.connected = true

	c.wg.Add(1)
	go c.readLoop()

	return nil
}

func TestModalConnectAndAuth(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{
		Workspace: "test-ws",
		Key:       "modal-key-123",
		Secret:    "modal-secret-456",
	})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	mock.mu.Lock()
	headers := mock.lastHeaders
	mock.mu.Unlock()

	if got := headers.Get("Modal-Key"); got != "modal-key-123" {
		t.Errorf("Modal-Key header = %q, want %q", got, "modal-key-123")
	}
	if got := headers.Get("Modal-Secret"); got != "modal-secret-456" {
		t.Errorf("Modal-Secret header = %q, want %q", got, "modal-secret-456")
	}
}

func TestModalSendAudio_BinaryFormat(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	// Send known float32 samples
	samples := []float32{0.0, 1.0, -1.0, 0.5, -0.5}
	if err := client.SendAudio(samples); err != nil {
		t.Fatalf("SendAudio failed: %v", err)
	}

	// Wait for message to arrive
	time.Sleep(100 * time.Millisecond)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.received) != 1 {
		t.Fatalf("expected 1 binary message, got %d", len(mock.received))
	}

	data := mock.received[0]

	// Verify length is multiple of 4
	if len(data)%4 != 0 {
		t.Errorf("binary data length %d is not a multiple of 4", len(data))
	}

	// Verify exact byte count: 5 samples * 4 bytes = 20
	if len(data) != 20 {
		t.Errorf("expected 20 bytes, got %d", len(data))
	}

	// Decode and verify each float32 sample
	for i, expected := range samples {
		bits := binary.LittleEndian.Uint32(data[i*4:])
		got := math.Float32frombits(bits)
		if got != expected {
			t.Errorf("sample[%d] = %v, want %v", i, got, expected)
		}
	}
}

func TestModalReceiveToken(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	// Send a token message from the mock server
	time.Sleep(50 * time.Millisecond)
	mock.sendJSON(map[string]interface{}{"type": "token", "text": "hello"})

	select {
	case tr := <-client.TranscriptChan():
		if tr.Type != "token" {
			t.Errorf("Type = %q, want %q", tr.Type, "token")
		}
		if tr.Text != "hello" {
			t.Errorf("Text = %q, want %q", tr.Text, "hello")
		}
		if tr.Final {
			t.Error("Final should be false for token")
		}
		if tr.VADEnd {
			t.Error("VADEnd should be false for token")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transcript")
	}
}

func TestModalReceiveVADEnd(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	mock.sendJSON(map[string]interface{}{"type": "vad_end"})

	select {
	case tr := <-client.TranscriptChan():
		if tr.Type != "vad_end" {
			t.Errorf("Type = %q, want %q", tr.Type, "vad_end")
		}
		if !tr.Final {
			t.Error("Final should be true for vad_end")
		}
		if !tr.VADEnd {
			t.Error("VADEnd should be true for vad_end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for vad_end")
	}
}

func TestModalReceiveError(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	mock.sendJSON(map[string]interface{}{"type": "error", "message": "bad audio"})

	select {
	case err := <-client.ErrorChan():
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !strings.Contains(err.Error(), "bad audio") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "bad audio")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
}

func TestModalPingPong(t *testing.T) {
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	// Set up pong handler on the mock server's conn to verify pong is received
	pongCh := make(chan string, 1)
	mock.mu.Lock()
	mock.conn.SetPongHandler(func(appData string) error {
		pongCh <- appData
		return nil
	})
	mock.mu.Unlock()

	// Send ping from mock server
	time.Sleep(50 * time.Millisecond)
	if err := mock.sendPing(); err != nil {
		t.Fatalf("sendPing failed: %v", err)
	}

	// The pong handler fires during ReadMessage, so we need the mock server
	// to be in a read loop. The server's handle() goroutine is already reading.
	// Wait for pong.
	select {
	case data := <-pongCh:
		if data != "ping" {
			t.Errorf("pong data = %q, want %q", data, "ping")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pong response")
	}
}

func TestModalNotConnected(t *testing.T) {
	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	// Should not be connected initially
	if client.IsConnected() {
		t.Error("expected disconnected state initially")
	}

	// SendAudio should fail when not connected
	err := client.SendAudio([]float32{0.1, 0.2})
	if err == nil {
		t.Error("expected error when sending audio while disconnected")
	}
}

func TestModalConnectFailure(t *testing.T) {
	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	// Try to connect to an invalid address
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Connect(ctx)
	if err == nil {
		t.Error("expected connection error for invalid Modal URL")
	}
}

func TestIsNormalClose(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "normal close 1000",
			err:      &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"},
			expected: true,
		},
		{
			name:     "abnormal close 1006",
			err:      &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "abnormal"},
			expected: false,
		},
		{
			name:     "going away 1001",
			err:      &websocket.CloseError{Code: websocket.CloseGoingAway, Text: "going away"},
			expected: false,
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("some random error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNormalClose(tt.err)
			if got != tt.expected {
				t.Errorf("IsNormalClose(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestModalNormalCloseOnErrorChan(t *testing.T) {
	// Verify that when the server sends a normal close (1000),
	// the error appears on ErrorChan and IsNormalClose returns true.
	mock := newMockModalServer()
	defer mock.close()

	client := NewClient(Config{Workspace: "test", Key: "k", Secret: "s"})
	defer client.Close()

	if err := connectToMock(client, mock.wsURL()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Server sends normal close frame
	mock.mu.Lock()
	conn := mock.conn
	mock.mu.Unlock()
	conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "idle timeout"))

	select {
	case err := <-client.ErrorChan():
		if !IsNormalClose(err) {
			t.Errorf("expected IsNormalClose=true for error %q", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close error on ErrorChan")
	}

	// Client should be disconnected
	if client.IsConnected() {
		t.Error("expected disconnected after normal close")
	}
}
