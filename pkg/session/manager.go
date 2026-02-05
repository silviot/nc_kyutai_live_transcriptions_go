package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/audio"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/hpb"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/modal"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/webrtc"
)

// Speaker represents an active speaker in a room
type Speaker struct {
	SessionID   string
	UserID      string
	Name        string
	Transcriber *Transcriber
}

// Transcriber manages transcription for a single speaker
type Transcriber struct {
	sessionID    string
	peerConn     *webrtc.PeerConnection
	audioCache   *audio.ChunkBuffer
	modalClient  *modal.Client
	audioPipe    *audio.Pipeline
	audioInputCh chan []int16   // Raw audio frames from WebRTC
	audioOutCh   chan []float32 // Processed audio chunks for Modal
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// Room manages transcription for a single Nextcloud Talk room
type Room struct {
	RoomToken    string
	LanguageID   string
	HPBClient    *hpb.Client
	WebRTCMgr    *webrtc.Manager
	Speakers     map[string]*Speaker  // sessionID -> Speaker
	ModalClients map[string]*modal.Client // sessionID -> Modal client
	modalConfig  modal.Config // Modal client configuration
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	logger       *slog.Logger
}

// Manager manages multiple rooms
type Manager struct {
	rooms        map[string]*Room  // roomToken -> Room
	mu           sync.RWMutex
	logger       *slog.Logger
	hpbURL       string
	hpbSecret    string
	modalConfig  modal.Config
	maxSpeakers  int
}

// ManagerConfig holds configuration for the session manager
type ManagerConfig struct {
	HPBURL          string
	HPBSecret       string
	ModalWorkspace  string
	ModalKey        string
	ModalSecret     string
	MaxSpeakers     int
	Logger          *slog.Logger
}

// NewManager creates a new session manager
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.MaxSpeakers <= 0 {
		cfg.MaxSpeakers = 500
	}

	return &Manager{
		rooms:       make(map[string]*Room),
		logger:      cfg.Logger,
		hpbURL:      cfg.HPBURL,
		hpbSecret:   cfg.HPBSecret,
		maxSpeakers: cfg.MaxSpeakers,
		modalConfig: modal.Config{
			Workspace: cfg.ModalWorkspace,
			Key:       cfg.ModalKey,
			Secret:    cfg.ModalSecret,
			Logger:    cfg.Logger,
		},
	}
}

// JoinRoom starts transcription for a room
func (m *Manager) JoinRoom(ctx context.Context, roomToken, languageID string, turnServers []webrtc.TURNServer) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if room already exists
	if room, exists := m.rooms[roomToken]; exists {
		m.logger.Info("room already joined", "roomToken", roomToken)
		return room, nil
	}

	// Create HPB client
	hpbClient := hpb.NewClient(hpb.Config{
		HPBURL: m.hpbURL,
		Secret: m.hpbSecret,
		Logger: m.logger,
	})

	if err := hpbClient.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to HPB: %w", err)
	}

	// Join the room
	if err := hpbClient.JoinRoom(roomToken); err != nil {
		hpbClient.Close()
		return nil, fmt.Errorf("failed to join room: %w", err)
	}

	// Create WebRTC manager
	rtcConfig := webrtc.ConnectionConfig{
		STUN: []string{
			"stun:stun.l.google.com:19302",
			"stun:stun1.l.google.com:19302",
		},
		TURN: turnServers,
	}

	webrtcMgr, err := webrtc.NewManager(rtcConfig, m.logger)
	if err != nil {
		hpbClient.Close()
		return nil, fmt.Errorf("failed to create WebRTC manager: %w", err)
	}

	// Create room context
	roomCtx, cancel := context.WithCancel(context.Background())

	room := &Room{
		RoomToken:    roomToken,
		LanguageID:   languageID,
		HPBClient:    hpbClient,
		WebRTCMgr:    webrtcMgr,
		Speakers:     make(map[string]*Speaker),
		ModalClients: make(map[string]*modal.Client),
		modalConfig:  m.modalConfig,
		ctx:          roomCtx,
		cancel:       cancel,
		logger:       m.logger,
	}

	m.rooms[roomToken] = room

	// Start room orchestration loop
	room.wg.Add(1)
	go room.orchestrationLoop()

	m.logger.Info("room joined", "roomToken", roomToken, "languageID", languageID)

	return room, nil
}

// orchestrationLoop manages room-level coordination
func (r *Room) orchestrationLoop() {
	defer r.wg.Done()

	hpbMsgCh := r.HPBClient.MessageChan()

	for {
		select {
		case <-r.ctx.Done():
			return
		case msg := <-hpbMsgCh:
			r.handleHPBMessage(msg)
		}
	}
}

// handleHPBMessage routes HPB messages appropriately
func (r *Room) handleHPBMessage(msg interface{}) {
	switch m := msg.(type) {
	case *hpb.RoomMessage:
		r.handleRoomMessage(m)
	case *hpb.MessageMessage:
		r.handleSignalingMessage(m)
	case *hpb.ByeMessage:
		r.handleByeMessage(m)
	default:
		r.logger.Debug("unhandled HPB message type", "type", fmt.Sprintf("%T", msg))
	}
}

// handleRoomMessage processes room topology updates
func (r *Room) handleRoomMessage(msg *hpb.RoomMessage) {
	r.logger.Debug("room message received", "participants", len(msg.Participants))

	// Process participants
	for _, participant := range msg.Participants {
		if participant.Audio && participant.InCall {
			r.addSpeaker(participant.SessionID, participant.UserID, participant.Name)
		}
	}
}

// handleSignalingMessage processes WebRTC signaling (offers/answers/ICE)
func (r *Room) handleSignalingMessage(msg *hpb.MessageMessage) {
	// Extract SDP offer from message data (Janus format)
	if msg.Data == nil {
		return
	}

	// Convert data to map[string]interface{}
	dataJSON, err := json.Marshal(msg.Data)
	if err != nil {
		r.logger.Debug("failed to marshal message data", "from", msg.From, "error", err)
		return
	}

	var dataMap map[string]interface{}
	if err := json.Unmarshal(dataJSON, &dataMap); err != nil {
		r.logger.Debug("failed to unmarshal message data", "from", msg.From, "error", err)
		return
	}

	// Handle offer
	if offer, exists := dataMap["offer"]; exists {
		offerStr, ok := offer.(map[string]interface{})
		if !ok {
			return
		}

		sdp, ok := offerStr["sdp"].(string)
		if !ok {
			return
		}

		sessionID := msg.From
		r.handleOffer(sessionID, sdp)
	}

	// Handle ICE candidates
	if candidate, exists := dataMap["candidate"]; exists {
		sessionID := msg.From
		candStr, _ := json.Marshal(candidate)
		r.handleICECandidate(sessionID, string(candStr))
	}
}

// handleOffer creates an answer for the SDP offer
func (r *Room) handleOffer(sessionID, offerSDP string) {
	peer := r.WebRTCMgr.GetPeer(sessionID)
	if peer == nil {
		r.logger.Warn("peer not found for offer", "sessionID", sessionID)
		return
	}

	// Create answer
	answerSDP, err := peer.CreateAnswer(offerSDP)
	if err != nil {
		r.logger.Error("failed to create answer", "sessionID", sessionID, "error", err)
		return
	}

	// Send answer back via HPB
	answerMsg := hpb.MessageMessage{
		Type:      "message",
		To:        sessionID,
		RoomToken: r.RoomToken,
		Data: map[string]interface{}{
			"answer": map[string]interface{}{
				"type": "answer",
				"sdp":  answerSDP,
			},
		},
	}

	if err := r.HPBClient.SendMessage(answerMsg); err != nil {
		r.logger.Error("failed to send answer", "sessionID", sessionID, "error", err)
	}
}

// handleICECandidate adds an ICE candidate to the peer connection
func (r *Room) handleICECandidate(sessionID, candidate string) {
	peer := r.WebRTCMgr.GetPeer(sessionID)
	if peer == nil {
		return
	}

	if err := peer.AddICECandidate(candidate); err != nil {
		r.logger.Debug("failed to add ICE candidate", "sessionID", sessionID, "error", err)
	}
}

// handleByeMessage processes departure notifications
func (r *Room) handleByeMessage(msg *hpb.ByeMessage) {
	r.logger.Info("participant leaving", "sessionID", msg.SessionID)

	// Remove speaker and clean up resources
	r.removeSpeaker(msg.SessionID)
}

// addSpeaker adds a new speaker to the room
func (r *Room) addSpeaker(sessionID, userID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already exists
	if _, exists := r.Speakers[sessionID]; exists {
		return nil
	}

	// Check capacity
	if len(r.Speakers) >= 100 { // Reasonable limit per room
		r.logger.Warn("speaker capacity reached for room", "roomToken", r.RoomToken)
		return fmt.Errorf("room at capacity")
	}

	// Create WebRTC peer connection
	peerConn, err := r.WebRTCMgr.CreatePeer(r.ctx, sessionID)
	if err != nil {
		r.logger.Error("failed to create peer connection", "sessionID", sessionID, "error", err)
		return err
	}

	// Create Modal client with proper configuration
	modalClient := modal.NewClient(modal.Config{
		Workspace: r.modalConfig.Workspace,
		Key:       r.modalConfig.Key,
		Secret:    r.modalConfig.Secret,
		Logger:    r.logger,
	})

	// Create audio pipeline
	audioPipe, err := audio.NewPipeline(48000, 24000, r.logger)
	if err != nil {
		r.logger.Error("failed to create audio pipeline", "sessionID", sessionID, "error", err)
		r.WebRTCMgr.RemovePeer(sessionID)
		modalClient.Close()
		return err
	}

	transcriber := &Transcriber{
		sessionID:    sessionID,
		peerConn:     peerConn,
		audioCache:   audio.NewChunkBuffer(24000, 200, r.logger),
		modalClient:  modalClient,
		audioPipe:    audioPipe,
		audioInputCh: make(chan []int16, 100),   // Bounded: ~2s of audio frames
		audioOutCh:   make(chan []float32, 10),  // Bounded: 10 chunks of 200ms
	}

	// Create speaker record
	speaker := &Speaker{
		SessionID:   sessionID,
		UserID:      userID,
		Name:        name,
		Transcriber: transcriber,
	}

	r.Speakers[sessionID] = speaker
	r.ModalClients[sessionID] = modalClient

	// Start transcriber goroutine
	transcriber.ctx, transcriber.cancel = context.WithCancel(r.ctx)
	transcriber.wg.Add(1)
	go transcriber.run(r.HPBClient, r.RoomToken, r.logger)

	r.logger.Info("speaker added", "sessionID", sessionID, "name", name, "roomToken", r.RoomToken)

	return nil
}

// removeSpeaker removes a speaker from the room
func (r *Room) removeSpeaker(sessionID string) error {
	r.mu.Lock()
	speaker, exists := r.Speakers[sessionID]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("speaker not found: %s", sessionID)
	}
	delete(r.Speakers, sessionID)

	// Close Modal client
	if modalClient, ok := r.ModalClients[sessionID]; ok {
		modalClient.Close()
		delete(r.ModalClients, sessionID)
	}
	r.mu.Unlock()

	// Close WebRTC peer
	if err := r.WebRTCMgr.RemovePeer(sessionID); err != nil {
		r.logger.Error("failed to remove peer", "sessionID", sessionID, "error", err)
	}

	r.logger.Info("speaker removed", "sessionID", sessionID, "name", speaker.Name)

	return nil
}

// LeaveRoom closes the room and all active transcriptions
func (m *Manager) LeaveRoom(roomToken string) error {
	m.mu.Lock()
	room, exists := m.rooms[roomToken]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("room not found: %s", roomToken)
	}
	delete(m.rooms, roomToken)
	m.mu.Unlock()

	// Cancel room context (stops orchestration loop)
	room.cancel()

	// Close all speakers
	room.mu.Lock()
	for sessionID := range room.Speakers {
		if err := room.removeSpeaker(sessionID); err != nil {
			room.logger.Error("failed to remove speaker", "sessionID", sessionID, "error", err)
		}
	}
	room.mu.Unlock()

	// Close WebRTC manager
	if err := room.WebRTCMgr.Close(); err != nil {
		room.logger.Error("failed to close WebRTC manager", "error", err)
	}

	// Close HPB client
	if err := room.HPBClient.Close(); err != nil {
		room.logger.Error("failed to close HPB client", "error", err)
	}

	// Wait for all goroutines
	done := make(chan struct{})
	go func() {
		room.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		m.logger.Warn("room cleanup timeout", "roomToken", roomToken)
	}

	m.logger.Info("room left", "roomToken", roomToken)

	return nil
}

// RoomCount returns the number of active rooms
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// SpeakerCount returns the total number of active speakers across all rooms
func (m *Manager) SpeakerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, room := range m.rooms {
		room.mu.RLock()
		count += len(room.Speakers)
		room.mu.RUnlock()
	}

	return count
}

// Close shuts down all rooms and the manager
func (m *Manager) Close() error {
	m.mu.Lock()
	roomTokens := make([]string, 0, len(m.rooms))
	for token := range m.rooms {
		roomTokens = append(roomTokens, token)
	}
	m.mu.Unlock()

	for _, token := range roomTokens {
		if err := m.LeaveRoom(token); err != nil {
			m.logger.Error("failed to leave room during shutdown", "roomToken", token, "error", err)
		}
	}

	return nil
}
