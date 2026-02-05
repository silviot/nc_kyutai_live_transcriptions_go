package test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MockHPBServer simulates Nextcloud HPB for testing
type MockHPBServer struct {
	listener  net.Listener
	server    *http.Server
	logger    *slog.Logger
	clients   map[*websocket.Conn]bool
	clientsMu sync.Mutex
	broadcast chan interface{}
	done      chan struct{}
}

// StartMockHPB starts a mock HPB server on the given port (0 = auto-assign)
func StartMockHPB(port int, logger *slog.Logger) (*MockHPBServer, error) {
	if logger == nil {
		logger = slog.Default()
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	mock := &MockHPBServer{
		listener:  listener,
		logger:    logger,
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan interface{}, 100),
		done:      make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", mock.handleWebSocket)

	mock.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := mock.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("mock HPB server error", "error", err)
		}
	}()
	go mock.broadcastLoop()

	logger.Info("mock HPB server started", "addr", listener.Addr().String())

	return mock, nil
}

// URL returns the WebSocket URL for this mock server
func (m *MockHPBServer) URL() string {
	return fmt.Sprintf("ws://%s", m.listener.Addr().String())
}

// handleWebSocket handles incoming WebSocket connections
func (m *MockHPBServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.Error("failed to upgrade connection", "error", err)
		return
	}

	m.clientsMu.Lock()
	m.clients[conn] = true
	m.clientsMu.Unlock()

	defer func() {
		m.clientsMu.Lock()
		delete(m.clients, conn)
		m.clientsMu.Unlock()
		conn.Close()
	}()

	for {
		select {
		case <-m.done:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				m.logger.Debug("WebSocket read error", "error", err)
			}
			return
		}

		// Parse incoming message (skip HMAC signature - format is "sig json")
		msgData := data
		if idx := findJSONStart(data); idx > 0 {
			msgData = data[idx:]
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(msgData, &msg); err != nil {
			m.logger.Debug("failed to parse message", "error", err, "data", string(data))
			continue
		}

		m.logger.Debug("HPB received message", "type", msg["type"])

		// Handle hello
		if msg["type"] == "hello" {
			response := map[string]interface{}{
				"type":      "hello",
				"sessionid": "test-session-123",
				"resumeid":  "test-resume-456",
			}
			m.sendMessage(conn, response)
		}

		// Handle incall (room join)
		if msg["type"] == "incall" {
			// Send room topology
			room := map[string]interface{}{
				"type":         "room",
				"token":        msg["room"],
				"participants": []map[string]interface{}{},
				"stunservers": []string{
					"stun:stun.l.google.com:19302",
				},
				"turnservers": []map[string]interface{}{},
			}
			m.sendMessage(conn, room)
		}

		// Handle ping
		if msg["type"] == "ping" {
			response := map[string]interface{}{
				"type": "pong",
			}
			m.sendMessage(conn, response)
		}
	}
}

// sendMessage sends a JSON message to a client (without HMAC for simplicity)
func (m *MockHPBServer) sendMessage(conn *websocket.Conn, msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// Prepend dummy HMAC signature (mock doesn't validate)
	signed := append([]byte("mocksig "), data...)
	return conn.WriteMessage(websocket.TextMessage, signed)
}

// findJSONStart finds the start of JSON in HMAC-signed message
func findJSONStart(data []byte) int {
	for i, b := range data {
		if b == '{' || b == '[' {
			return i
		}
	}
	return 0
}

// BroadcastMessage sends a message to all connected clients
func (m *MockHPBServer) BroadcastMessage(msg interface{}) {
	select {
	case m.broadcast <- msg:
	default:
		m.logger.Warn("broadcast channel full")
	}
}

// broadcastLoop sends broadcast messages to all clients
func (m *MockHPBServer) broadcastLoop() {
	for {
		select {
		case <-m.done:
			return
		case msg := <-m.broadcast:
			data, _ := json.Marshal(msg)
			signed := append([]byte("mocksig "), data...)

			m.clientsMu.Lock()
			for client := range m.clients {
				if err := client.WriteMessage(websocket.TextMessage, signed); err != nil {
					m.logger.Debug("failed to broadcast", "error", err)
					client.Close()
					delete(m.clients, client)
				}
			}
			m.clientsMu.Unlock()
		}
	}
}

// SendWebRTCOffer sends an SDP offer to all clients (simulating Janus)
func (m *MockHPBServer) SendWebRTCOffer(sessionID string, sdpOffer string) {
	msg := map[string]interface{}{
		"type": "message",
		"from": sessionID,
		"data": map[string]interface{}{
			"type": "answer",
			"offer": map[string]interface{}{
				"type": "offer",
				"sdp":  sdpOffer,
			},
		},
	}
	m.BroadcastMessage(msg)
}

// Close stops the mock server
func (m *MockHPBServer) Close() error {
	close(m.done)

	m.clientsMu.Lock()
	for client := range m.clients {
		client.Close()
	}
	m.clientsMu.Unlock()

	return m.server.Close()
}

// ClientCount returns the number of connected clients
func (m *MockHPBServer) ClientCount() int {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	return len(m.clients)
}

// WaitForConnections waits for N clients to connect (with timeout)
func (m *MockHPBServer) WaitForConnections(n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		m.clientsMu.Lock()
		count := len(m.clients)
		m.clientsMu.Unlock()

		if count >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %d connections, got %d", n, count)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
