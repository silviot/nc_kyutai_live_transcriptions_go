package modal

import (
	"context"
	"encoding/base64"
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
	Text   string `json:"text"`
	Final  bool   `json:"final"`
	VADEnd bool   `json:"vad_end"`
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

	// Create basic auth header
	auth := c.key + ":" + c.secret
	encodedAuth := base64.StdEncoding.EncodeToString([]byte(auth))

	// Prepare headers with authentication
	headers := map[string][]string{
		"Authorization": {"Basic " + encodedAuth},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		c.logger.Error("failed to connect to Modal", "workspace", c.workspace, "error", err)
		return err
	}

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

		c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			if !isPeerClosedError(err) {
				c.logger.Error("Modal read error", "error", err)
				c.errCh <- err
			}
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
			return
		}

		// Handle text messages (JSON transcripts)
		if messageType == websocket.TextMessage {
			var transcript Transcript
			if err := json.Unmarshal(data, &transcript); err != nil {
				c.logger.Error("failed to parse transcript", "error", err, "data", string(data))
				continue
			}

			select {
			case c.transcriptCh <- transcript:
			case <-c.ctx.Done():
				return
			default:
				c.logger.Warn("transcript channel full, dropping result")
			}
		}
	}
}

// SendAudio sends audio data to Modal STT service
func (c *Client) SendAudio(audioData []float32) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.connected {
		return fmt.Errorf("not connected to Modal")
	}

	// Convert float32 to binary frame
	// Format: 4 bytes per sample (float32 little-endian)
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

// isPeerClosedError checks if error is due to peer closing connection
func isPeerClosedError(err error) bool {
	// Check for websocket closure or context cancellation
	return err == websocket.ErrCloseSent ||
		err.Error() == "websocket: close sent" ||
		err.Error() == "websocket: connection reset by peer"
}
