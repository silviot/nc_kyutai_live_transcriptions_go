package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"gopkg.in/hraban/opus.v2"
)

// Manager handles WebRTC peer connections and audio extraction
type Manager struct {
	config       *webrtc.Configuration
	logger       *slog.Logger
	peers        map[string]*PeerConnection
	mu           sync.RWMutex
	audioFrameCh chan AudioFrame
	errCh        chan error
	closeCh      chan struct{}
}

// PeerConnection wraps a WebRTC RTCPeerConnection
type PeerConnection struct {
	sessionID     string
	peerConn      *webrtc.PeerConnection
	audioTrack    *webrtc.TrackRemote
	rtpReceiver   *webrtc.RTPReceiver
	closeCh       chan struct{}
	wg            sync.WaitGroup
	sampleRate    int
	channels      int
	lastFrameTime int64
	onAudio       func([]float32) // Callback for decoded audio
}

// NewManager creates a new WebRTC manager
func NewManager(cfg ConnectionConfig, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Build WebRTC configuration from ConnectionConfig
	rtcConfig := webrtc.Configuration{}

	// Add STUN servers
	for _, stunURL := range cfg.STUN {
		rtcConfig.ICEServers = append(rtcConfig.ICEServers, webrtc.ICEServer{
			URLs: []string{stunURL},
		})
	}

	// Add TURN servers
	for _, turn := range cfg.TURN {
		rtcConfig.ICEServers = append(rtcConfig.ICEServers, webrtc.ICEServer{
			URLs:       turn.URLs,
			Username:   turn.Username,
			Credential: turn.Credential,
		})
	}

	return &Manager{
		config:       &rtcConfig,
		logger:       logger,
		peers:        make(map[string]*PeerConnection),
		audioFrameCh: make(chan AudioFrame, 10),
		errCh:        make(chan error, 10),
		closeCh:      make(chan struct{}),
	}, nil
}

// CreatePeer creates a new WebRTC peer connection for a session
func (m *Manager) CreatePeer(ctx context.Context, sessionID string) (*PeerConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create RTCPeerConnection with increased buffer sizes to avoid
	// "mux: failed to read from packetio.Buffer short buffer" errors
	se := webrtc.SettingEngine{}
	se.SetReceiveMTU(16384)
	// Increase replay protection window for SRTP
	se.SetSRTPReplayProtectionWindow(1024)

	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	peerConn, err := api.NewPeerConnection(*m.config)
	if err != nil {
		m.logger.Error("failed to create peer connection", "sessionID", sessionID, "error", err)
		return nil, err
	}

	peer := &PeerConnection{
		sessionID:  sessionID,
		peerConn:   peerConn,
		closeCh:    make(chan struct{}),
		sampleRate: 48000,
		channels:   1,
	}

	// Register callbacks
	peerConn.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		peer.onTrack(remoteTrack, receiver, m.logger)
	})

	peerConn.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		peer.onICEConnectionStateChange(state, m.logger)
	})

	peerConn.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		peer.onConnectionStateChange(state, m.logger)
	})

	peerConn.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			m.logger.Debug("ICE candidate discovered", "sessionID", sessionID)
		}
	})

	m.peers[sessionID] = peer
	m.logger.Info("peer connection created", "sessionID", sessionID)

	return peer, nil
}

// SetOnAudio sets the callback for decoded audio data
func (p *PeerConnection) SetOnAudio(cb func([]float32)) {
	p.onAudio = cb
}

// onTrack handles incoming tracks (audio and video)
func (p *PeerConnection) onTrack(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver, logger *slog.Logger) {
	codec := remoteTrack.Codec()
	logger.Info("track received",
		"sessionID", p.sessionID,
		"codec", codec.MimeType,
		"clockRate", codec.ClockRate,
		"channels", codec.Channels,
		"kind", remoteTrack.Kind().String(),
	)

	// Only process audio tracks - ignore video (VP8, etc.)
	if remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
		logger.Debug("ignoring non-audio track", "sessionID", p.sessionID, "codec", codec.MimeType)
		return
	}

	// Only accept Opus codec
	if codec.MimeType != "audio/opus" {
		logger.Warn("ignoring non-opus audio track", "sessionID", p.sessionID, "codec", codec.MimeType)
		return
	}

	p.audioTrack = remoteTrack
	p.rtpReceiver = receiver

	if codec.ClockRate > 0 {
		p.sampleRate = int(codec.ClockRate)
	}
	if codec.Channels > 0 {
		p.channels = int(codec.Channels)
	}

	// Start reading and decoding audio frames
	p.wg.Add(1)
	go p.readAndDecodeAudio(logger)
}

// readAndDecodeAudio reads RTP packets, decodes Opus to PCM, and delivers via callback
func (p *PeerConnection) readAndDecodeAudio(logger *slog.Logger) {
	defer p.wg.Done()

	// Create Opus decoder (48kHz stereo - SDP declares opus/48000/2)
	channels := p.channels
	if channels < 1 {
		channels = 2 // Default to stereo per SDP convention
	}
	opusDecoder, err := opus.NewDecoder(48000, channels)
	if err != nil {
		logger.Error("failed to create Opus decoder", "sessionID", p.sessionID, "error", err, "channels", channels)
		return
	}
	logger.Debug("Opus decoder created", "sessionID", p.sessionID, "channels", channels)

	// Buffer for decoded PCM (max 120ms at 48kHz * channels = 5760*channels samples)
	pcmBuf := make([]float32, 5760*channels)
	frameCount := 0

	for {
		select {
		case <-p.closeCh:
			return
		default:
		}

		// Read RTP packet
		rtpPacket, _, err := p.audioTrack.ReadRTP()
		if err != nil {
			if p.isClosed() {
				return
			}
			logger.Error("failed to read RTP", "sessionID", p.sessionID, "error", err)
			return
		}

		if len(rtpPacket.Payload) == 0 {
			continue
		}

		frameCount++
		if frameCount <= 5 || frameCount%100 == 0 {
			logger.Debug("RTP packet", "sessionID", p.sessionID, "seq", rtpPacket.SequenceNumber,
				"ts", rtpPacket.Timestamp, "payloadLen", len(rtpPacket.Payload),
				"pt", rtpPacket.PayloadType, "frameCount", frameCount)
		}

		// Decode Opus payload to float32 PCM
		n, err := opusDecoder.DecodeFloat32(rtpPacket.Payload, pcmBuf)
		if err != nil {
			logger.Debug("opus decode error", "sessionID", p.sessionID, "error", err, "payloadLen", len(rtpPacket.Payload))
			continue
		}

		if n == 0 {
			continue
		}

		p.lastFrameTime = time.Now().UnixMilli()

		if frameCount <= 5 || frameCount%500 == 0 {
			// Log audio values for debugging
			var peak float32
			total := n * channels
			for i := 0; i < total; i++ {
				v := pcmBuf[i]
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
			logger.Debug("decoded audio frame", "sessionID", p.sessionID, "samplesPerCh", n, "channels", channels, "frameCount", frameCount, "peak", peak)
		}

		// Clamp decoded samples to [-1, 1] range.
		// Opus DecodeFloat32 can produce values outside this range during
		// transients or decoder state warmup.
		total := n * channels
		for i := 0; i < total; i++ {
			if pcmBuf[i] > 1.0 {
				pcmBuf[i] = 1.0
			} else if pcmBuf[i] < -1.0 {
				pcmBuf[i] = -1.0
			}
		}

		// Deliver decoded PCM via callback (mono)
		if p.onAudio != nil {
			if channels == 1 {
				// Mono: just copy directly
				samples := make([]float32, n)
				copy(samples, pcmBuf[:n])
				p.onAudio(samples)
			} else {
				// Stereo: downmix to mono by averaging L/R channels
				// pcmBuf is interleaved: L0, R0, L1, R1, ...
				// n is samples per channel, total values = n * channels
				mono := make([]float32, n)
				for i := 0; i < n; i++ {
					var sum float32
					for ch := 0; ch < channels; ch++ {
						sum += pcmBuf[i*channels+ch]
					}
					mono[i] = sum / float32(channels)
				}
				p.onAudio(mono)
			}
		}
	}
}

func (p *PeerConnection) isClosed() bool {
	select {
	case <-p.closeCh:
		return true
	default:
		return false
	}
}

// onICEConnectionStateChange handles ICE connection state changes
func (p *PeerConnection) onICEConnectionStateChange(state webrtc.ICEConnectionState, logger *slog.Logger) {
	logger.Info("ICE connection state changed", "sessionID", p.sessionID, "state", state.String())
}

// onConnectionStateChange handles peer connection state changes
func (p *PeerConnection) onConnectionStateChange(state webrtc.PeerConnectionState, logger *slog.Logger) {
	logger.Info("peer connection state changed", "sessionID", p.sessionID, "state", state.String())
}

// CreateAnswer creates an SDP answer for a given offer
func (p *PeerConnection) CreateAnswer(offerSDP string) (string, error) {
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}

	if err := p.peerConn.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("failed to set remote description: %w", err)
	}

	answer, err := p.peerConn.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create answer: %w", err)
	}

	if err := p.peerConn.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("failed to set local description: %w", err)
	}

	return answer.SDP, nil
}

// AddICECandidate adds an ICE candidate to the peer connection
func (p *PeerConnection) AddICECandidate(candidate string) error {
	var c webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidate), &c); err != nil {
		return fmt.Errorf("failed to parse ICE candidate: %w", err)
	}

	return p.peerConn.AddICECandidate(c)
}

// Close closes the peer connection and cleans up resources
func (p *PeerConnection) Close() error {
	close(p.closeCh)
	p.wg.Wait()

	if p.peerConn != nil {
		return p.peerConn.Close()
	}

	return nil
}

// GetPeer returns a peer by session ID
func (m *Manager) GetPeer(sessionID string) *PeerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.peers[sessionID]
}

// RemovePeer removes and closes a peer connection
func (m *Manager) RemovePeer(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	peer, exists := m.peers[sessionID]
	if !exists {
		return fmt.Errorf("peer not found: %s", sessionID)
	}

	if err := peer.Close(); err != nil {
		m.logger.Error("failed to close peer", "sessionID", sessionID, "error", err)
	}

	delete(m.peers, sessionID)
	m.logger.Info("peer removed", "sessionID", sessionID)

	return nil
}

// AudioFrameChan returns the channel for receiving audio frames
func (m *Manager) AudioFrameChan() <-chan AudioFrame {
	return m.audioFrameCh
}

// ErrorChan returns the channel for receiving errors
func (m *Manager) ErrorChan() <-chan error {
	return m.errCh
}

// Close closes all peer connections and the manager
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for sessionID, peer := range m.peers {
		if err := peer.Close(); err != nil {
			m.logger.Error("failed to close peer during shutdown", "sessionID", sessionID, "error", err)
		}
	}
	m.peers = make(map[string]*PeerConnection)

	return nil
}

// PeerCount returns the number of active peer connections
func (m *Manager) PeerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers)
}
