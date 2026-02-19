package hpb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	hpbReadTimeout         = 3 * time.Minute
	hpbControlWriteTimeout = 5 * time.Second
)

// Client manages WebSocket connection to HPB with reconnection and internal auth
type Client struct {
	hpbURL          string
	backendURL      string
	secret          string // Internal client secret (for WebSocket HMAC auth)
	signalingSecret string // Backend signaling secret (for HTTP API auth)
	ticketURL       string
	ticket          string
	token           string // JWT token for v2.0 auth
	resumeID        string
	sessionID       string
	conn            *websocket.Conn
	mu              sync.Mutex
	logger          *slog.Logger
	msgChan         chan interface{}
	errChan         chan error
	closeChan       chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	msgID           atomic.Int64
}

// Config holds HPB client configuration
type Config struct {
	HPBURL          string // HPB WebSocket URL
	BackendURL      string // Nextcloud backend URL (for auth)
	Secret          string // HMAC secret for internal WebSocket authentication
	SignalingSecret string // Backend signaling secret (for HTTP API auth, different from internal secret)
	// Ticket-based auth v1.0 (for regular users, mutually exclusive with Secret)
	TicketURL string // Auth URL from signaling settings helloAuthParams
	Ticket    string // Ticket from signaling settings helloAuthParams
	// JWT token auth v2.0 (newer signaling)
	Token  string       // JWT token from signaling settings helloAuthParams["2.0"].token
	Logger *slog.Logger // Logger instance
}

// NewClient creates a new HPB client
func NewClient(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Derive backend URL from HPB URL if not provided
	if cfg.BackendURL == "" {
		// Extract origin from HPB URL (e.g., wss://cloud.example.com/... -> https://cloud.example.com/)
		url := cfg.HPBURL
		url = strings.Replace(url, "wss://", "https://", 1)
		url = strings.Replace(url, "ws://", "http://", 1)
		// Find the path and keep only the origin
		if idx := strings.Index(url, "/standalone-signaling"); idx > 0 {
			url = url[:idx]
		}
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		cfg.BackendURL = url
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		hpbURL:          cfg.HPBURL,
		backendURL:      cfg.BackendURL,
		secret:          cfg.Secret,
		signalingSecret: cfg.SignalingSecret,
		ticketURL:       cfg.TicketURL,
		ticket:          cfg.Ticket,
		token:           cfg.Token,
		logger:          cfg.Logger,
		msgChan:         make(chan interface{}, 100), // Bounded message queue
		errChan:         make(chan error, 10),
		closeChan:       make(chan struct{}),
		ctx:             ctx,
		cancel:          cancel,
	}
}

// Connect establishes WebSocket connection to HPB and starts message handling
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close any existing connection
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	// Reset closeChan if already closed (reconnection)
	select {
	case <-c.closeChan:
		c.closeChan = make(chan struct{})
	default:
	}

	// Connect to HPB
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, c.hpbURL, nil)
	if err != nil {
		c.logger.Error("failed to connect to HPB", "url", c.hpbURL, "error", err)
		return err
	}

	// Keep read deadlines alive from control frames too. Without this, quiet
	// rooms can hit read deadline even while ping/pong is healthy, causing
	// avoidable reconnect churn.
	conn.SetPongHandler(func(appData string) error {
		return conn.SetReadDeadline(time.Now().Add(hpbReadTimeout))
	})
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(hpbReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(hpbControlWriteTimeout))
	})
	_ = conn.SetReadDeadline(time.Now().Add(hpbReadTimeout))

	c.conn = conn
	c.logger.Info("connected to HPB", "url", c.hpbURL)

	// Send hello message
	if err := c.sendHello(); err != nil {
		c.conn.Close()
		c.conn = nil
		return err
	}

	// Start read/write goroutines
	c.wg.Add(2)
	go c.readLoop()
	go c.writeLoop()

	return nil
}

// generateNonce generates a random hex nonce for auth
func generateNonce() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// hmacSHA256 computes HMAC-SHA256 of data with secret
func hmacSHA256(secret, data string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// sendHello sends initial hello message to HPB with internal, ticket, or JWT auth
func (c *Client) sendHello() error {
	if c.token != "" {
		return c.sendHelloToken()
	}
	if c.ticket != "" {
		return c.sendHelloTicket()
	}
	return c.sendHelloInternal()
}

// sendHelloInternal sends hello with HMAC-based internal auth
func (c *Client) sendHelloInternal() error {
	nonce := generateNonce()
	token := hmacSHA256(c.secret, nonce)

	hello := HelloRequest{
		Type: "hello",
		ID:   c.nextMsgID(),
		Hello: HelloInner{
			Version: "2.0",
			Auth: HelloAuth{
				Type: "internal",
				Params: HelloAuthParams{
					Random:  nonce,
					Token:   token,
					Backend: c.backendURL,
				},
			},
		},
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return err
	}

	c.logger.Debug("sending hello to HPB (internal auth)", "backend", c.backendURL)
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// sendHelloTicket sends hello with ticket-based user auth
func (c *Client) sendHelloTicket() error {
	hello := map[string]interface{}{
		"type": "hello",
		"id":   c.nextMsgID(),
		"hello": map[string]interface{}{
			"version": "2.0",
			"auth": map[string]interface{}{
				"url": c.ticketURL,
				"params": map[string]interface{}{
					"ticket": c.ticket,
				},
			},
		},
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return err
	}

	c.logger.Debug("sending hello to HPB (ticket auth)", "url", c.ticketURL)
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// sendHelloToken sends hello with JWT token auth (v2.0)
func (c *Client) sendHelloToken() error {
	hello := map[string]interface{}{
		"type": "hello",
		"id":   c.nextMsgID(),
		"hello": map[string]interface{}{
			"version": "2.0",
			"auth": map[string]interface{}{
				"url": c.backendURL + "ocs/v2.php/apps/spreed/api/v3/signaling/backend",
				"params": map[string]interface{}{
					"token": c.token,
				},
			},
		},
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return err
	}

	c.logger.Debug("sending hello to HPB (JWT token auth)", "backend", c.backendURL)
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// nextMsgID returns the next message ID
func (c *Client) nextMsgID() string {
	return fmt.Sprintf("%d", c.msgID.Add(1))
}

// readLoop handles incoming messages from HPB
func (c *Client) readLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		if c.conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Keep a generous read deadline; pong/ping handlers refresh it.
		c.conn.SetReadDeadline(time.Now().Add(hpbReadTimeout))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "use of closed") {
				c.logger.Error("HPB read error", "error", err)
				select {
				case c.errChan <- err:
				default:
				}
			}
			return
		}

		// Parse message type to route appropriately
		if err := c.handleMessage(data); err != nil {
			c.logger.Error("failed to handle HPB message", "error", err)
		}
	}
}

// writeLoop handles outgoing messages to HPB (future: transcript delivery)
func (c *Client) writeLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// Send periodic ping
			if err := c.sendPing(); err != nil {
				c.logger.Error("failed to send ping", "error", err)
			}
		case <-c.closeChan:
			return
		}
	}
}

// sendPing sends a WebSocket-level ping frame (not an application-level message).
// The HPB signaling server has no "ping" message type handler.
func (c *Client) sendPing() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("connection closed")
	}

	return c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(hpbControlWriteTimeout))
}

// handleMessage routes incoming messages to appropriate handlers
func (c *Client) handleMessage(data []byte) error {
	// HPB sends plain JSON (no HMAC signature on incoming messages)
	// Try to detect if message has HMAC prefix (for mock servers)
	msgData := data
	if len(data) > 0 && data[0] != '{' && data[0] != '[' {
		// Might be HMAC-signed (format: "sig json"), try to extract JSON
		parts := strings.SplitN(string(data), " ", 2)
		if len(parts) >= 2 {
			msgData = []byte(parts[1])
		}
	}

	// Parse message type
	var baseMsg Message
	if err := json.Unmarshal(msgData, &baseMsg); err != nil {
		return err
	}

	c.logger.Debug("received HPB message", "type", baseMsg.Type)

	switch baseMsg.Type {
	case "hello":
		var msg HelloResponse
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onHello(&msg)

	case "welcome":
		c.logger.Debug("received welcome message")

	case "room":
		var msg RoomMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onRoom(&msg)

	case "event":
		var msg EventMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.logger.Debug("raw event data", "data", string(msgData))
		c.onEvent(&msg)

	case "message":
		var msg MessageMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onMessage(&msg)

	case "bye":
		var msg ByeMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onBye(&msg)

	case "error":
		var msg ErrorMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		if msg.Error.Code != "" {
			c.logger.Error("HPB error", "code", msg.Error.Code, "message", msg.Error.Message, "details", msg.Error.Details)
		} else {
			c.logger.Error("HPB error", "code", msg.Code, "message", msg.Message)
		}

	case "ping":
		// Server-initiated keep-alive — respond with pong
		pong := PongMessage{Type: "pong"}
		if err := c.SendMessage(pong); err != nil {
			c.logger.Error("failed to send pong", "error", err)
		}

	case "pong":
		// Keep-alive response, ignore

	default:
		c.logger.Debug("unknown HPB message type", "type", baseMsg.Type, "data", string(msgData))
	}

	return nil
}

// onHello handles hello response from HPB
func (c *Client) onHello(msg *HelloResponse) {
	c.mu.Lock()
	c.resumeID = msg.Hello.ResumeID
	c.sessionID = msg.Hello.SessionID
	c.mu.Unlock()

	c.logger.Info("HPB hello response", "resumeID", c.resumeID, "sessionID", c.sessionID)

	select {
	case c.msgChan <- msg:
	case <-c.ctx.Done():
	default:
		c.logger.Warn("HPB message channel full, dropping hello")
	}
}

// onRoom handles room join confirmation from HPB
func (c *Client) onRoom(msg *RoomMessage) {
	c.logger.Info("received room join response", "roomID", msg.Room.RoomID)

	select {
	case c.msgChan <- msg:
	case <-c.ctx.Done():
	default:
		c.logger.Warn("HPB message channel full, dropping room message")
	}
}

// onEvent handles room/participant events
func (c *Client) onEvent(msg *EventMessage) {
	c.logger.Debug("received event", "target", msg.Event.Target, "type", msg.Event.Type)

	select {
	case c.msgChan <- msg:
	case <-c.ctx.Done():
	default:
		c.logger.Warn("HPB message channel full, dropping event")
	}
}

// onMessage handles signaling message (SDP offer/answer, ICE candidates)
func (c *Client) onMessage(msg *MessageMessage) {
	senderID := ""
	if msg.Message.Sender != nil {
		senderID = msg.Message.Sender.SessionID
	}
	c.logger.Debug("received signaling message", "from", senderID, "data", msg.Message.Data)

	select {
	case c.msgChan <- msg:
	case <-c.ctx.Done():
	default:
		c.logger.Warn("HPB message channel full, dropping signaling message")
	}
}

// onBye handles bye message (participant leaving or room closing)
func (c *Client) onBye(msg *ByeMessage) {
	c.logger.Info("received bye", "sessionID", msg.SessionID, "reason", msg.Reason)

	select {
	case c.msgChan <- msg:
	case <-c.ctx.Done():
	default:
		c.logger.Warn("HPB message channel full, dropping bye message")
	}
}

// SendMessage sends a message to HPB (plain JSON)
func (c *Client) SendMessage(msg interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("connection closed")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.logger.Debug("HPB outgoing message", "json", string(data))

	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// JoinRoom sends a room join message to HPB signaling
func (c *Client) JoinRoom(roomToken string) error {
	msg := RoomJoinRequest{
		Type: "room",
		ID:   c.nextMsgID(),
		Room: RoomJoinInner{
			RoomID:    roomToken,
			SessionID: "", // Not needed for internal auth
		},
	}
	c.logger.Debug("joining room via HPB", "roomToken", roomToken)
	return c.SendMessage(msg)
}

// SendTranscript sends a transcript message to a specific session via HPB
func (c *Client) SendTranscript(recipientSessionID, text, langID, speakerSessionID string, final bool) error {
	msg := MessageMessage{
		Type: "message",
		ID:   c.nextMsgID(),
		Message: MessageEnvelope{
			Recipient: &MessageRecipient{
				Type:      "session",
				SessionID: recipientSessionID,
			},
			Data: map[string]interface{}{
				"type":             "transcript",
				"message":          text,
				"langId":           langID,
				"speakerSessionId": speakerSessionID,
				"final":            final,
			},
		},
	}
	return c.SendMessage(msg)
}

// SendRoomMessage sends a message to all participants in a room via the HPB backend API.
// This uses HTTP POST to the signaling server's backend endpoint, which broadcasts
// the message to all connected clients in the room.
func (c *Client) SendRoomMessage(roomToken string, data map[string]interface{}) error {
	// Build the HTTP API URL from the WebSocket URL (strip path, keep origin)
	apiURL := c.hpbURL
	apiURL = strings.Replace(apiURL, "wss://", "https://", 1)
	apiURL = strings.Replace(apiURL, "ws://", "http://", 1)
	// Strip path after host:port (e.g., /spreed) - API is at the root
	if idx := strings.Index(apiURL[8:], "/"); idx > 0 {
		apiURL = apiURL[:8+idx]
	}
	apiURL = strings.TrimRight(apiURL, "/")
	apiURL += "/api/v1/room/" + roomToken

	// Build request body
	body := map[string]interface{}{
		"type": "message",
		"message": map[string]interface{}{
			"data": data,
		},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal room message: %w", err)
	}

	// Generate HMAC-SHA256 signature (same as BackendNotifier in Talk PHP)
	// Uses the signaling backend secret, NOT the internal client secret
	random := generateNonce() // 64 hex chars
	mac := hmac.New(sha256.New, []byte(c.signalingSecret))
	mac.Write([]byte(random))
	mac.Write(bodyJSON)
	checksum := hex.EncodeToString(mac.Sum(nil))

	// Create HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Spreed-Signaling-Random", random)
	req.Header.Set("Spreed-Signaling-Checksum", checksum)
	req.Header.Set("Spreed-Signaling-Backend", c.backendURL)

	c.logger.Debug("sending room message via backend API", "url", apiURL, "body", string(bodyJSON))

	// Send request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("backend API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("backend API returned %d: %s", resp.StatusCode, string(respBody))
	}

	c.logger.Debug("backend API response", "status", resp.StatusCode, "body", string(respBody))
	return nil
}

// SendTranscriptViaBackend sends a transcript message to all room participants via the HPB backend API
func (c *Client) SendTranscriptViaBackend(roomToken, text, langID, speakerSessionID string, final bool) error {
	data := map[string]interface{}{
		"type":             "transcript",
		"message":          text,
		"langId":           langID,
		"speakerSessionId": speakerSessionID,
		"final":            final,
	}
	return c.SendRoomMessage(roomToken, data)
}

// MessageChan returns the channel for receiving HPB messages
func (c *Client) MessageChan() <-chan interface{} {
	return c.msgChan
}

// ErrorChan returns the channel for receiving errors
func (c *Client) ErrorChan() <-chan error {
	return c.errChan
}

// Close closes the HPB connection and cleans up
func (c *Client) Close() error {
	c.cancel()
	close(c.closeChan)

	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	c.wg.Wait()
	return nil
}

// IsConnected returns whether the HPB connection is active
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// SessionID returns the current session ID
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}
