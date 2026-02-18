package modal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Transcript represents a transcript result from Modal STT
type Transcript struct {
	Type    string `json:"type"`    // "token", "vad_end", "error"
	Text    string `json:"text"`    // Token text (for type="token")
	Message string `json:"message"` // Error message (for type="error")
	Final   bool   `json:"final"`   // Computed: true if vad_end
	VADEnd  bool   `json:"vad_end"` // True if this is a vad_end marker
}

// Client manages WebSocket connection to Modal STT service
type Client struct {
	workspace  string
	key        string
	secret     string
	conn       *websocket.Conn
	mu         sync.Mutex
	logger     *slog.Logger
	transcriptCh chan Transcript
	errCh      chan error
	closeCh    chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	connected  bool
}

// Config holds Modal client configuration
type Config struct {
	Workspace string       // Modal workspace name
	Key       string       // Modal API key
	Secret    string       // Modal API secret
	Logger    *slog.Logger // Logger instance
}

// NewClient creates a new Modal STT client
func NewClient(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		workspace:    cfg.Workspace,
		key:          cfg.Key,
		secret:       cfg.Secret,
		logger:       cfg.Logger,
		transcriptCh: make(chan Transcript, 50), // Bounded transcript queue
		errCh:        make(chan error, 10),
		closeCh:      make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Connect establishes WebSocket connection to Modal endpoint
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build WebSocket URL
	wsURL := fmt.Sprintf("wss://%s--kyutai-stt-rust-kyutaisttrustservice-serve.modal.run/v1/stream", c.workspace)

	// Prepare headers with Modal authentication (not Basic auth)
	headers := map[string][]string{
		"Modal-Key":    {c.key},
		"Modal-Secret": {c.secret},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 60 * time.Second, // Modal cold start can take time
	}

	c.logger.Info("connecting to Modal", "url", wsURL)
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		c.logger.Error("failed to connect to Modal", "workspace", c.workspace, "error", err)
		return err
	}

	// Set up ping handler that uses WriteControl (safe for concurrent use)
	// instead of the default handler which uses WriteMessage (NOT safe).
	// Without this, the pong response conflicts with SendAudio writes,
	// causing the server to close the connection.
	conn.SetPingHandler(func(appData string) error {
		c.logger.Debug("Modal ping received, sending pong")
		err := conn.WriteControl(
			websocket.PongMessage,
			[]byte(appData),
			time.Now().Add(10*time.Second),
		)
		if err != nil {
			c.logger.Debug("pong write error", "error", err)
		}
		return err
	})

	c.conn = conn
	c.connected = true
	c.logger.Info("connected to Modal STT service", "workspace", c.workspace)

	// Start read loop
	c.wg.Add(1)
	go c.readLoop()

	return nil
}

// readLoop handles incoming transcripts from Modal
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

		c.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			c.logger.Error("Modal read error", "error", err)
			select {
			case c.errCh <- err:
			default:
			}
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
			return
		}

		c.logger.Debug("Modal message received", "type", messageType, "len", len(data))

		// Handle text messages (JSON transcripts)
		if messageType == websocket.TextMessage {
			var msg map[string]interface{}
			if err := json.Unmarshal(data, &msg); err != nil {
				c.logger.Error("failed to parse Modal message", "error", err, "data", string(data))
				continue
			}

			msgType, _ := msg["type"].(string)
			switch msgType {
			case "token":
				text, _ := msg["text"].(string)
				transcript := Transcript{
					Type:   "token",
					Text:   text,
					Final:  false,
					VADEnd: false,
				}
				select {
				case c.transcriptCh <- transcript:
				case <-c.ctx.Done():
					return
				default:
					c.logger.Warn("transcript channel full, dropping result")
				}

			case "vad_end":
				transcript := Transcript{
					Type:   "vad_end",
					Final:  true,
					VADEnd: true,
				}
				select {
				case c.transcriptCh <- transcript:
				case <-c.ctx.Done():
					return
				default:
					c.logger.Warn("transcript channel full, dropping result")
				}

			case "error":
				errMsg, _ := msg["message"].(string)
				c.logger.Error("Modal error", "message", errMsg)
				select {
				case c.errCh <- fmt.Errorf("modal error: %s", errMsg):
				default:
				}

			default:
				c.logger.Debug("unknown Modal message type", "type", msgType, "data", string(data))
			}
		}
	}
}

// SendAudio sends audio data to Modal STT service as float32 (little-endian)
func (c *Client) SendAudio(audioData []float32) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.connected {
		return fmt.Errorf("not connected to Modal")
	}

	// Convert float32 to binary frame (4 bytes per sample, float32 little-endian)
	// Modal Rust proxy expects float32 audio at 24kHz
	binaryData := make([]byte, len(audioData)*4)
	for i, sample := range audioData {
		bits := math.Float32bits(sample)
		binary.LittleEndian.PutUint32(binaryData[i*4:], bits)
	}

	return c.conn.WriteMessage(websocket.BinaryMessage, binaryData)
}

// TranscriptChan returns the channel for receiving transcripts
func (c *Client) TranscriptChan() <-chan Transcript {
	return c.transcriptCh
}

// ErrorChan returns the channel for receiving errors
func (c *Client) ErrorChan() <-chan error {
	return c.errCh
}

// IsConnected returns whether the Modal connection is active
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Close closes the Modal connection and cleans up
func (c *Client) Close() error {
	c.cancel()

	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.connected = false
	}
	c.mu.Unlock()

	c.wg.Wait()
	return nil
}

// IsNormalClose returns true if the error represents a normal WebSocket close (code 1000).
// This indicates the server intentionally closed the connection (e.g. idle timeout)
// and the client should NOT attempt reconnection.
func IsNormalClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure)
}
