package test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// MockHPBServer simulates Nextcloud HPB for testing
type MockHPBServer struct {
	addr     string
	server   *http.Server
	logger   *slog.Logger
	clients  map[*websocket.Conn]bool
	broadcast chan interface{}
}

// StartMockHPB starts a mock HPB server
func StartMockHPB(port int, logger *slog.Logger) (*MockHPBServer, error) {
	if logger == nil {
		logger = slog.Default()
	}

	mock := &MockHPBServer{
		addr:      fmt.Sprintf(":%d", port),
		logger:    logger,
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan interface{}, 100),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", mock.handleWebSocket)

	mock.server = &http.Server{
		Addr:    mock.addr,
		Handler: mux,
	}

	go mock.server.ListenAndServe()
	go mock.broadcastLoop()

	logger.Info("mock HPB server started", "addr", mock.addr)

	return mock, nil
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

	m.clients[conn] = true
	defer delete(m.clients, conn)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				m.logger.Error("WebSocket error", "error", err)
			}
			return
		}

		// Parse incoming message
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			m.logger.Error("failed to parse message", "error", err)
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
			respData, _ := json.Marshal(response)
			conn.WriteMessage(websocket.TextMessage, respData)
		}

		// Handle incall (room join)
		if msg["type"] == "incall" {
			// Send room topology
			room := map[string]interface{}{
				"type":  "room",
				"token": msg["room"],
				"participants": []map[string]interface{}{},
				"stunservers": []string{
					"stun:stun.l.google.com:19302",
				},
				"turnservers": []map[string]interface{}{},
			}
			roomData, _ := json.Marshal(room)
			conn.WriteMessage(websocket.TextMessage, roomData)
		}
	}
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
	for msg := range m.broadcast {
		data, _ := json.Marshal(msg)
		for client := range m.clients {
			if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
				m.logger.Error("failed to broadcast", "error", err)
				client.Close()
				delete(m.clients, client)
			}
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
	close(m.broadcast)
	return m.server.Close()
}

// ClientCount returns the number of connected clients
func (m *MockHPBServer) ClientCount() int {
	return len(m.clients)
}

// WaitForConnections waits for N clients to connect (with timeout)
func (m *MockHPBServer) WaitForConnections(n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if len(m.clients) >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %d connections, got %d", n, len(m.clients))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
