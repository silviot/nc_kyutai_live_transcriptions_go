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
	sessionID      string
	peerConn       *webrtc.PeerConnection
	audioCache     *audio.ChunkBuffer
	modalClient    *modal.Client
	audioPipe      *audio.Pipeline
	audioInputCh   chan []float32                // Decoded float32 PCM from WebRTC (48kHz)
	audioOutCh     chan []float32                // Processed audio chunks for Modal (24kHz)
	broadcast      func(text string, final bool) // Callback to broadcast transcript to room participants
	pendingText    string                        // Accumulated tokens for current utterance
	lastBroadcast  time.Time                     // Last time a non-final broadcast was sent
	broadcastDirty bool                          // Whether there's unsent accumulated text
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// Room manages transcription for a single Nextcloud Talk room
type Room struct {
	RoomToken    string
	LanguageID   string
	HPBClient    *hpb.Client
	WebRTCMgr    *webrtc.Manager
	Speakers     map[string]*Speaker      // HPB sessionID -> Speaker
	ModalClients map[string]*modal.Client // HPB sessionID -> Modal client
	modalConfig  modal.Config             // Modal client configuration
	// internalSessions tracks HPB session IDs that belong to this bot
	// (identified via "internal":true in participants updates).
	// Used to avoid adding our own sessions as speakers after HPB reconnects.
	internalSessions map[string]bool
	mu               sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	logger           *slog.Logger
}

// Manager manages multiple rooms
type Manager struct {
	rooms              map[string]*Room // roomToken -> Room
	mu                 sync.RWMutex
	logger             *slog.Logger
	hpbURL             string
	hpbSecret          string // Internal client secret
	hpbSignalingSecret string // Backend signaling secret
	hpbBackendURL      string
	modalConfig        modal.Config
	maxSpeakers        int
}

// ManagerConfig holds configuration for the session manager
type ManagerConfig struct {
	HPBURL             string
	HPBSecret          string // Internal client secret (for WebSocket auth)
	HPBSignalingSecret string // Backend signaling secret (for HTTP API auth)
	HPBBackendURL      string // Nextcloud backend URL for HPB auth
	STTStreamURL       string // Optional explicit STT WebSocket URL
	ModalWorkspace     string
	ModalKey           string
	ModalSecret        string
	MaxSpeakers        int
	Logger             *slog.Logger
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
		rooms:              make(map[string]*Room),
		logger:             cfg.Logger,
		hpbURL:             cfg.HPBURL,
		hpbSecret:          cfg.HPBSecret,
		hpbSignalingSecret: cfg.HPBSignalingSecret,
		hpbBackendURL:      cfg.HPBBackendURL,
		maxSpeakers:        cfg.MaxSpeakers,
		modalConfig: modal.Config{
			StreamURL: cfg.STTStreamURL,
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
		HPBURL:          m.hpbURL,
		BackendURL:      m.hpbBackendURL,
		Secret:          m.hpbSecret,
		SignalingSecret: m.hpbSignalingSecret,
		Logger:          m.logger,
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
		RoomToken:        roomToken,
		LanguageID:       languageID,
		HPBClient:        hpbClient,
		WebRTCMgr:        webrtcMgr,
		Speakers:         make(map[string]*Speaker),
		ModalClients:     make(map[string]*modal.Client),
		modalConfig:      m.modalConfig,
		internalSessions: make(map[string]bool),
		ctx:              roomCtx,
		cancel:           cancel,
		logger:           m.logger,
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
	hpbErrCh := r.HPBClient.ErrorChan()

	for {
		select {
		case <-r.ctx.Done():
			return
		case msg := <-hpbMsgCh:
			r.handleHPBMessage(msg)
		case err := <-hpbErrCh:
			r.logger.Warn("HPB connection error, reconnecting", "error", err)
			r.reconnectHPB()
		}
	}
}

// reconnectHPB attempts to reconnect to HPB with backoff
func (r *Room) reconnectHPB() {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}

		time.Sleep(backoff)

		r.logger.Info("attempting HPB reconnection", "roomToken", r.RoomToken)
		if err := r.HPBClient.Connect(r.ctx); err != nil {
			r.logger.Error("HPB reconnection failed", "error", err, "nextBackoff", backoff*2)
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Re-join the room after reconnecting
		if err := r.HPBClient.JoinRoom(r.RoomToken); err != nil {
			r.logger.Error("failed to rejoin room after HPB reconnection", "error", err)
			continue
		}

		r.logger.Info("HPB reconnected and room rejoined", "roomToken", r.RoomToken)
		return
	}
}

// handleHPBMessage routes HPB messages appropriately
func (r *Room) handleHPBMessage(msg interface{}) {
	switch m := msg.(type) {
	case *hpb.RoomMessage:
		r.handleRoomMessage(m)
	case *hpb.EventMessage:
		r.handleEventMessage(m)
	case *hpb.MessageMessage:
		r.handleSignalingMessage(m)
	case *hpb.ByeMessage:
		r.handleByeMessage(m)
	case *hpb.HelloResponse:
		r.logger.Debug("hello response received (already processed by HPB client)")
	default:
		r.logger.Debug("unhandled HPB message type", "type", fmt.Sprintf("%T", msg))
	}
}

// handleRoomMessage processes room join confirmation.
// Note: HPB room responses do NOT include participants. Participants arrive
// via separate "event" messages with target "participants".
func (r *Room) handleRoomMessage(msg *hpb.RoomMessage) {
	r.logger.Info("room join confirmed", "roomID", msg.Room.RoomID)
}

// handleEventMessage processes events from HPB.
// target "room": join/leave notifications (session IDs only)
// target "participants": full user updates with inCall flags from the Nextcloud backend
func (r *Room) handleEventMessage(msg *hpb.EventMessage) {
	r.logger.Debug("event received", "target", msg.Event.Target, "type", msg.Event.Type)

	switch msg.Event.Target {
	case "room":
		r.handleRoomEvent(msg)
	case "participants":
		r.handleParticipantsEvent(msg)
	default:
		r.logger.Debug("unhandled event target", "target", msg.Event.Target)
	}
}

// handleRoomEvent handles room-level join/leave events.
// Only maintains session ID mappings here; speaker addition is handled by
// handleParticipantsEvent which has the "internal" flag to correctly
// identify and skip our own bot sessions (important after HPB reconnects
// where the bot gets a new session ID but old sessions remain visible).
func (r *Room) handleRoomEvent(msg *hpb.EventMessage) {
	for _, entry := range msg.Event.Join {
		r.logger.Info("session joined room", "sessionID", entry.SessionID, "userID", entry.UserID)

		// Track our own current session as internal
		if entry.SessionID == r.HPBClient.SessionID() {
			r.mu.Lock()
			r.internalSessions[entry.SessionID] = true
			r.mu.Unlock()
		}
	}
	// Handle leaves
	for _, sessionID := range msg.Event.Leave {
		r.logger.Info("session left room", "sessionID", sessionID)
		r.removeSpeaker(sessionID)
	}
}

// stalePingThreshold is the maximum age of a participant's lastPing before
// they are considered stale and ignored. This prevents creating Modal
// connections for ghost participants that the HPB still reports as in-call.
// Set generously: Nextcloud browsers ping every ~30s but this can drift,
// and after HPB reconnects the participant list may contain slightly stale
// pings. 10 minutes is safe — truly stale ghosts will have pings hours old.
const stalePingThreshold = 10 * time.Minute

// handleParticipantsEvent handles participants update from Nextcloud backend.
// This is the main way we learn about who is in the call with audio.
func (r *Room) handleParticipantsEvent(msg *hpb.EventMessage) {
	update := msg.Event.Update
	if update == nil {
		r.logger.Debug("participants event with nil update")
		return
	}

	// Check if everyone left the call
	if update.All {
		if incall, ok := update.InCall.(float64); ok && incall == 0 {
			r.logger.Info("call ended for everyone")
			return
		}
	}

	users := update.Users
	if len(users) == 0 {
		users = update.Changed
	}

	r.logger.Info("participants update", "userCount", len(users))

	now := time.Now()

	for _, user := range users {
		// The "sessionId" in participants events is the HPB session ID
		// (same format as entry.SessionID in room join events), NOT the
		// OCS room session ID (which is a separate 255-char string).
		sessionID, _ := user["sessionId"].(string)
		if sessionID == "" {
			continue
		}

		userID, _ := user["userId"].(string)
		internal, _ := user["internal"].(bool)

		// Track and skip internal sessions (our own bot sessions).
		// This is critical after HPB reconnects: the bot gets a new session ID
		// but old bot sessions may still be visible in the room.
		if internal {
			r.mu.Lock()
			r.internalSessions[sessionID] = true
			r.mu.Unlock()
			r.removeSpeaker(sessionID)
			continue
		}

		// Check if this is a known internal session (defense in depth)
		r.mu.RLock()
		isInternal := r.internalSessions[sessionID]
		r.mu.RUnlock()
		if isInternal {
			continue
		}

		// Check lastPing staleness: skip participants whose ping is too old.
		// Ghost participants (e.g. browser crashed without clean disconnect)
		// stay in the HPB participant list but stop sending pings.
		if lastPing, ok := user["lastPing"].(float64); ok && lastPing > 0 {
			pingTime := time.Unix(int64(lastPing), 0)
			pingAge := now.Sub(pingTime)
			if pingAge > stalePingThreshold {
				r.logger.Warn("skipping stale participant",
					"sessionID", sessionID, "lastPing", int64(lastPing),
					"pingAge", pingAge.Round(time.Second))
				r.removeSpeaker(sessionID)
				continue
			}
		}

		// Parse inCall flags
		var inCallFlags int
		switch v := user["inCall"].(type) {
		case float64:
			inCallFlags = int(v)
		case int:
			inCallFlags = v
		}

		inCall := inCallFlags&hpb.CallFlagInCall != 0
		withAudio := inCallFlags&2 != 0 // WITH_AUDIO = 2

		displayName, _ := user["displayname"].(string)
		if displayName == "" {
			displayName = userID
		}

		r.logger.Debug("participant status",
			"sessionID", sessionID, "userID", userID, "inCall", inCallFlags,
			"isInCall", inCall, "withAudio", withAudio)

		if inCallFlags == 0 {
			r.removeSpeaker(sessionID)
		} else if inCall && withAudio {
			r.addSpeaker(sessionID, userID, displayName)
		}
	}
}

// handleSignalingMessage processes WebRTC signaling (offers/answers/ICE)
// MCU messages use format: {type: "offer"/"answer", from: sessionID, payload: {type, sdp}, roomType: "video"}
func (r *Room) handleSignalingMessage(msg *hpb.MessageMessage) {
	data := msg.Message.Data
	if data == nil {
		return
	}

	senderID := ""
	if msg.Message.Sender != nil {
		senderID = msg.Message.Sender.SessionID
	}

	msgType, _ := data["type"].(string)
	r.logger.Debug("signaling message", "type", msgType, "from", senderID, "dataKeys", fmt.Sprintf("%v", mapKeys(data)))

	switch msgType {
	case "offer":
		// MCU sends offer with payload containing the SDP
		payload, ok := data["payload"].(map[string]interface{})
		if !ok {
			r.logger.Warn("offer missing payload")
			return
		}
		sdp, ok := payload["sdp"].(string)
		if !ok {
			r.logger.Warn("offer payload missing sdp")
			return
		}
		// Use "from" field as the publisher session ID for subscriber connections
		fromID, _ := data["from"].(string)
		if fromID != "" {
			senderID = fromID
		}
		r.handleOffer(senderID, sdp)

	case "candidate":
		// ICE candidate from MCU
		payload, ok := data["candidate"].(map[string]interface{})
		if !ok {
			// Try payload field
			payload, ok = data["payload"].(map[string]interface{})
		}
		if ok {
			candStr, _ := json.Marshal(payload)
			fromID, _ := data["from"].(string)
			if fromID != "" {
				senderID = fromID
			}
			r.handleICECandidate(senderID, string(candStr))
		}

	default:
		r.logger.Debug("unhandled signaling message type", "type", msgType, "data", data)
	}
}

// mapKeys returns the keys of a map for debug logging
func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
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

	// Send answer back via HPB using MCU message format
	answerMsg := hpb.MessageMessage{
		Type: "message",
		Message: hpb.MessageEnvelope{
			Recipient: &hpb.MessageRecipient{
				Type:      "session",
				SessionID: sessionID,
			},
			Data: map[string]interface{}{
				"type":     "answer",
				"roomType": "video",
				"to":       sessionID,
				"payload": map[string]interface{}{
					"type": "answer",
					"sdp":  answerSDP,
				},
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
		StreamURL: r.modalConfig.StreamURL,
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

	langID := r.LanguageID
	// Use the bot's own HPB session ID as speakerSessionId so that every
	// browser (including the actual speaker's) has a matching CallParticipantModel
	// and can display the transcript. Using the real speaker's ID would cause
	// the speaker's own browser to drop the transcript (no model for self).
	botSessionID := r.HPBClient.SessionID()
	transcriber := &Transcriber{
		sessionID:    sessionID,
		peerConn:     peerConn,
		audioCache:   audio.NewChunkBuffer(24000, 80, r.logger), // 80ms chunks to match Modal Rust server expectations
		modalClient:  modalClient,
		audioPipe:    audioPipe,
		audioInputCh: make(chan []float32, 500), // Bounded: ~10s of audio frames
		audioOutCh:   make(chan []float32, 200), // Bounded: ~16s of 80ms chunks
		broadcast: func(text string, final bool) {
			// Broadcast transcript to all room participants via HPB backend API.
			// This sends an HTTP POST to the signaling server which broadcasts
			// the message as a room event to all connected clients.
			r.logger.Info("broadcasting transcript", "text", text, "speaker", sessionID, "bot", botSessionID, "room", r.RoomToken)
			if err := r.HPBClient.SendTranscriptViaBackend(r.RoomToken, text, langID, botSessionID, final); err != nil {
				r.logger.Error("failed to broadcast transcript via backend API", "error", err)
			}
		},
	}

	// Wire WebRTC decoded audio to transcriber input
	peerConn.SetOnAudio(func(samples []float32) {
		select {
		case transcriber.audioInputCh <- samples:
		default:
			// Drop if full (backpressure)
		}
	})

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

	// Send requestoffer to subscribe to participant's audio via MCU
	requestOffer := hpb.MessageMessage{
		Type: "message",
		Message: hpb.MessageEnvelope{
			Recipient: &hpb.MessageRecipient{
				Type:      "session",
				SessionID: sessionID,
			},
			Data: map[string]interface{}{
				"type":     "requestoffer",
				"roomType": "video",
			},
		},
	}
	if err := r.HPBClient.SendMessage(requestOffer); err != nil {
		r.logger.Error("failed to send requestoffer", "sessionID", sessionID, "error", err)
	} else {
		r.logger.Info("sent requestoffer to participant", "sessionID", sessionID)
	}

	return nil
}

// removeSpeaker removes a speaker from the room.
// Caller must NOT hold r.mu.
func (r *Room) removeSpeaker(sessionID string) error {
	r.mu.Lock()
	speaker, exists := r.Speakers[sessionID]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("speaker not found: %s", sessionID)
	}
	delete(r.Speakers, sessionID)
	delete(r.ModalClients, sessionID)
	r.mu.Unlock()

	// Cancel transcriber context and wait for goroutines to exit.
	// This also closes the modal client via the deferred Close in run().
	if speaker.Transcriber != nil {
		speaker.Transcriber.Close()
	}

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

	// Collect speaker IDs under lock, then remove without lock (removeSpeaker locks internally)
	room.mu.RLock()
	sessionIDs := make([]string, 0, len(room.Speakers))
	for sessionID := range room.Speakers {
		sessionIDs = append(sessionIDs, sessionID)
	}
	room.mu.RUnlock()

	for _, sessionID := range sessionIDs {
		if err := room.removeSpeaker(sessionID); err != nil {
			room.logger.Error("failed to remove speaker", "sessionID", sessionID, "error", err)
		}
	}

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
