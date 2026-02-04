package hpb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client manages WebSocket connection to HPB with reconnection and HMAC auth
type Client struct {
	hpbURL       string
	secret       string
	resumeID     string
	sessionID    string
	conn         *websocket.Conn
	mu           sync.Mutex
	logger       *slog.Logger
	msgChan      chan interface{}
	errChan      chan error
	closeChan    chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	reconnecting bool
}

// Config holds HPB client configuration
type Config struct {
	HPBURL string        // HPB WebSocket URL
	Secret string        // HMAC secret for authentication
	Logger *slog.Logger  // Logger instance
}

// NewClient creates a new HPB client
func NewClient(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		hpbURL:    cfg.HPBURL,
		secret:    cfg.Secret,
		logger:    cfg.Logger,
		msgChan:   make(chan interface{}, 100), // Bounded message queue
		errChan:   make(chan error, 10),
		closeChan: make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Connect establishes WebSocket connection to HPB and starts message handling
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Connect to HPB
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, c.hpbURL, nil)
	if err != nil {
		c.logger.Error("failed to connect to HPB", "url", c.hpbURL, "error", err)
		return err
	}

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

// sendHello sends initial hello message to HPB
func (c *Client) sendHello() error {
	hello := HelloMessage{
		Type:     "hello",
		ResumeID: c.resumeID,
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return err
	}

	// Add HMAC signature
	msg := c.addHMACSignature(data)

	c.logger.Debug("sending hello to HPB", "resumeID", c.resumeID)
	return c.conn.WriteMessage(websocket.TextMessage, msg)
}

// addHMACSignature adds HMAC-SHA256 signature to message
func (c *Client) addHMACSignature(data []byte) []byte {
	h := hmac.New(sha256.New, []byte(c.secret))
	h.Write(data)
	sig := h.Sum(nil)

	// Format: signature + space + data
	sig64 := fmt.Sprintf("%x", sig)
	return []byte(sig64 + " " + string(data))
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

		c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !strings.Contains(err.Error(), "context canceled") {
				c.logger.Error("HPB read error", "error", err)
				c.errChan <- err
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

// sendPing sends a keep-alive ping
func (c *Client) sendPing() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("connection closed")
	}

	msg := PingMessage{Type: "ping"}
	data, _ := json.Marshal(msg)
	signedMsg := c.addHMACSignature(data)

	return c.conn.WriteMessage(websocket.TextMessage, signedMsg)
}

// handleMessage routes incoming messages to appropriate handlers
func (c *Client) handleMessage(data []byte) error {
	// Remove HMAC signature (format: "sig data")
	parts := strings.SplitN(string(data), " ", 2)
	if len(parts) < 2 {
		return fmt.Errorf("invalid message format: missing HMAC")
	}
	msgData := []byte(parts[1])

	// Parse message type
	var baseMsg Message
	if err := json.Unmarshal(msgData, &baseMsg); err != nil {
		return err
	}

	c.logger.Debug("received HPB message", "type", baseMsg.Type)

	switch baseMsg.Type {
	case "hello":
		var msg HelloMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onHello(&msg)

	case "room":
		var msg RoomMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			return err
		}
		c.onRoom(&msg)

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
		c.logger.Error("HPB error", "code", msg.Code, "message", msg.Message)

	case "pong":
		// Keep-alive response, ignore

	default:
		c.logger.Debug("unknown HPB message type", "type", baseMsg.Type)
	}

	return nil
}

// onHello handles hello response from HPB
func (c *Client) onHello(msg *HelloMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.resumeID = msg.ResumeID
	c.sessionID = msg.SessionID
	c.logger.Info("HPB hello response", "resumeID", c.resumeID, "sessionID", c.sessionID)

	// Send copy to message channel
	c.msgChan <- msg
}

// onRoom handles room topology from HPB
func (c *Client) onRoom(msg *RoomMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("received room topology", "token", msg.RoomToken, "participants", len(msg.Participants),
		"stun_servers", len(msg.STUNServers), "turn_servers", len(msg.TURNServers))

	// Send copy to message channel
	c.msgChan <- msg
}

// onMessage handles signaling message (SDP offer/answer, ICE candidates)
func (c *Client) onMessage(msg *MessageMessage) {
	c.logger.Debug("received signaling message", "from", msg.From, "type", msg.Data)

	// Send copy to message channel
	c.msgChan <- msg
}

// onBye handles bye message (participant leaving or room closing)
func (c *Client) onBye(msg *ByeMessage) {
	c.logger.Info("received bye", "sessionID", msg.SessionID, "reason", msg.Reason)

	// Send copy to message channel
	c.msgChan <- msg
}

// SendMessage sends a signed message to HPB
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

	signedMsg := c.addHMACSignature(data)
	return c.conn.WriteMessage(websocket.TextMessage, signedMsg)
}

// JoinRoom sends incall message to join a room
func (c *Client) JoinRoom(roomToken string) error {
	msg := InCallMessage{
		Type:      "incall",
		RoomToken: roomToken,
		SessionID: c.sessionID,
	}
	return c.SendMessage(msg)
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
