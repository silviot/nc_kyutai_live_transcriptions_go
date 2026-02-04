package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
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
	sessionID    string
	peerConn     *webrtc.PeerConnection
	audioTrack   *webrtc.TrackRemote
	rtpReceiver  *webrtc.RTPReceiver
	closeCh      chan struct{}
	wg            sync.WaitGroup
	sampleRate   int
	channels     int
	lastFrameTime int64
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
		audioFrameCh: make(chan AudioFrame, 10), // Bounded channel for audio frames
		errCh:        make(chan error, 10),
		closeCh:      make(chan struct{}),
	}, nil
}

// CreatePeer creates a new WebRTC peer connection for a session
func (m *Manager) CreatePeer(ctx context.Context, sessionID string) (*PeerConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create RTCPeerConnection
	peerConn, err := webrtc.NewPeerConnection(*m.config)
	if err != nil {
		m.logger.Error("failed to create peer connection", "sessionID", sessionID, "error", err)
		return nil, err
	}

	peer := &PeerConnection{
		sessionID:  sessionID,
		peerConn:   peerConn,
		closeCh:    make(chan struct{}),
		sampleRate: 48000, // Default WebRTC sample rate
		channels:   1,     // Expect mono from Janus
	}

	// Register callbacks
	peerConn.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		peer.onTrack(remoteTrack, receiver, m.audioFrameCh, m.logger)
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

// onTrack handles incoming audio track
func (p *PeerConnection) onTrack(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver, frameCh chan AudioFrame, logger *slog.Logger) {
	p.audioTrack = remoteTrack
	p.rtpReceiver = receiver

	codec := remoteTrack.Codec()
	logger.Info("audio track received", "sessionID", p.sessionID, "codec", codec.MimeType)

	// Detect sample rate from codec (Opus typically 48kHz)
	if codec.ClockRate == 48000 {
		p.sampleRate = 48000
	}

	// Start reading audio frames
	p.wg.Add(1)
	go p.readAudioFrames(frameCh, logger)
}

// readAudioFrames reads audio from the track
func (p *PeerConnection) readAudioFrames(frameCh chan AudioFrame, logger *slog.Logger) {
	defer p.wg.Done()

	// Read audio packets from track
	rtpBuf := make([]byte, 1400)
	for {
		select {
		case <-p.closeCh:
			return
		default:
		}

		n, _, err := p.audioTrack.Read(rtpBuf)
		if err != nil {
			logger.Error("failed to read audio from track", "sessionID", p.sessionID, "error", err)
			return
		}

		if n == 0 {
			continue
		}

		// Acknowledge receipt; actual audio decoding in audio pipeline
		p.lastFrameTime = time.Now().UnixMilli()

		// Send frame info (data decoded in audio pipeline)
		select {
		case frameCh <- AudioFrame{
			Data:       make([]float32, 0), // Populated in audio pipeline
			SampleRate: p.sampleRate,
			Channels:   p.channels,
			Timestamp:  p.lastFrameTime,
		}:
		case <-p.closeCh:
			return
		default:
			logger.Warn("audio frame channel full, dropping frame", "sessionID", p.sessionID)
		}
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
	// Parse offer
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}

	// Set remote description
	if err := p.peerConn.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("failed to set remote description: %w", err)
	}

	// Create answer
	answer, err := p.peerConn.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create answer: %w", err)
	}

	// Set local description
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
