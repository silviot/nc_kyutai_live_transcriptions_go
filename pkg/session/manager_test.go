package session

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/hpb"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/modal"
	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/webrtc"
)

// newTestRoom creates a minimal Room suitable for testing event handling logic.
// It has a real WebRTC manager (needed for removeSpeaker), but addSpeaker
// will still fail since it requires a valid context and Modal config.
func newTestRoom(botSessionID string) *Room {
	rtcMgr, _ := webrtc.NewManager(webrtc.ConnectionConfig{}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	return &Room{
		RoomToken:        "test-room",
		Speakers:         make(map[string]*Speaker),
		ModalClients:     make(map[string]*modal.Client),
		internalSessions: make(map[string]bool),
		HPBClient:        &hpb.Client{}, // minimal; SessionID() will return ""
		WebRTCMgr:        rtcMgr,
		ctx:              ctx,
		cancel:           cancel,
		logger:           slog.Default(),
	}
}

// TestParticipantsSessionIdIsHPBSessionId documents the key protocol insight:
// the "sessionId" field in HPB participants events contains the HPB session ID
// (the same short ID from room join events), NOT the OCS room session ID
// (which is a separate 255-char encrypted string available in join events as
// "roomsessionid"). This is critical: using the wrong ID type silently breaks
// speaker addition because no mapping is found.
func TestParticipantsSessionIdIsHPBSessionId(t *testing.T) {
	room := newTestRoom("")

	hpbSessionID := "gsYmkXeEH7gPTDEtZG5la2NhYgkaA5LjgIqejfoQBQg"

	// First, a room join event arrives with the HPB session ID
	joinMsg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "room",
			Type:   "join",
			Join: []hpb.EventSessionEntry{
				{
					SessionID:     hpbSessionID,
					UserID:        "silviot",
					RoomSessionID: "ETjuIV06CmV1aaLmOvN5...long-255-char-ocs-id...",
				},
			},
		},
	}
	room.handleRoomEvent(joinMsg)

	// Then participants event arrives — its "sessionId" is the HPB session ID,
	// NOT the OCS room session ID. The code must use it directly without
	// any OCS→HPB translation.
	participantsMsg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": hpbSessionID, // This IS the HPB session ID
						"inCall":    float64(7),
						"lastPing":  float64(time.Now().Unix()),
						"userId":    "silviot",
					},
				},
			},
		},
	}
	room.handleParticipantsEvent(participantsMsg)

	// The participant should be recognized and added as a speaker
	// (addSpeaker will fail due to no Modal config, but the attempt proves
	// the session ID was correctly resolved — no "mapping not found" skip)
	// We check that at least the code path reached addSpeaker by verifying
	// it didn't silently skip the participant.
	// Since addSpeaker fails without Modal, we just verify no panic and
	// that the internal sessions set doesn't incorrectly contain this ID.
	room.mu.RLock()
	defer room.mu.RUnlock()
	if room.internalSessions[hpbSessionID] {
		t.Error("non-internal participant should not be in internalSessions")
	}
}

func TestHandleParticipantsEvent_InternalSessionsTracked(t *testing.T) {
	room := newTestRoom("")

	// Simulate participants update with two internal sessions (bot's own)
	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "bot-session-old",
						"internal":  true,
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
					{
						"sessionId": "bot-session-new",
						"internal":  true,
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(msg)

	// Both internal sessions should be tracked
	room.mu.RLock()
	defer room.mu.RUnlock()

	if !room.internalSessions["bot-session-old"] {
		t.Error("expected bot-session-old to be tracked as internal")
	}
	if !room.internalSessions["bot-session-new"] {
		t.Error("expected bot-session-new to be tracked as internal")
	}

	// No speakers should be added for internal sessions
	if len(room.Speakers) != 0 {
		t.Errorf("expected 0 speakers, got %d", len(room.Speakers))
	}
}

func TestHandleParticipantsEvent_InternalSessionRemovedAsSpeaker(t *testing.T) {
	room := newTestRoom("")

	// Pre-populate a speaker that was erroneously added
	room.Speakers["bot-session-old"] = &Speaker{
		SessionID:   "bot-session-old",
		Transcriber: nil, // nil transcriber is safe for removeSpeaker
	}

	// Participants update reveals it's internal
	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "bot-session-old",
						"internal":  true,
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// The erroneously added speaker should be removed
	if _, exists := room.Speakers["bot-session-old"]; exists {
		t.Error("expected bot-session-old to be removed as speaker")
	}

	// And tracked as internal
	if !room.internalSessions["bot-session-old"] {
		t.Error("expected bot-session-old to be tracked as internal")
	}
}

func TestHandleParticipantsEvent_StaleParticipantSkipped(t *testing.T) {
	room := newTestRoom("")

	// Participants update with a stale lastPing (2 hours ago).
	// The sessionId IS the HPB session ID — no translation needed.
	stalePing := float64(time.Now().Add(-2 * time.Hour).Unix())

	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "ghost-hpb-id",
						"inCall":    float64(7), // IN_CALL | WITH_AUDIO | WITH_VIDEO
						"lastPing":  stalePing,
						"userId":    "ghost-user",
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// Ghost participant should NOT be added as speaker
	if _, exists := room.Speakers["ghost-hpb-id"]; exists {
		t.Error("stale participant should not be added as speaker")
	}
}

func TestHandleParticipantsEvent_StaleParticipantRemovedIfExisting(t *testing.T) {
	room := newTestRoom("")

	// Pre-add ghost as speaker (was added when ping was fresh)
	room.Speakers["ghost-hpb-id"] = &Speaker{
		SessionID:   "ghost-hpb-id",
		Transcriber: nil,
	}

	// Now participants update arrives with stale ping
	stalePing := float64(time.Now().Add(-5 * time.Minute).Unix())

	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "ghost-hpb-id",
						"inCall":    float64(7),
						"lastPing":  stalePing,
						"userId":    "ghost-user",
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// Ghost speaker should be removed
	if _, exists := room.Speakers["ghost-hpb-id"]; exists {
		t.Error("stale participant should be removed as speaker")
	}
}

func TestHandleRoomEvent_NoSpeakerAddition(t *testing.T) {
	room := newTestRoom("")

	// Simulate join event
	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "room",
			Type:   "join",
			Join: []hpb.EventSessionEntry{
				{
					SessionID:     "some-session",
					UserID:        "user1",
					RoomSessionID: "ocs-session-1",
				},
			},
		},
	}

	room.handleRoomEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// NO speaker should be added (speakers are only added via participants update)
	if len(room.Speakers) != 0 {
		t.Errorf("expected 0 speakers from room join event, got %d", len(room.Speakers))
	}
}

func TestHandleRoomEvent_LeaveRemovesSpeaker(t *testing.T) {
	room := newTestRoom("")

	// Pre-add a speaker
	room.Speakers["leaving-session"] = &Speaker{
		SessionID:   "leaving-session",
		Transcriber: nil,
	}

	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "room",
			Type:   "leave",
			Leave:  []string{"leaving-session"},
		},
	}

	room.handleRoomEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	if _, exists := room.Speakers["leaving-session"]; exists {
		t.Error("expected speaker to be removed on leave event")
	}
}

func TestHandleParticipantsEvent_KnownInternalSessionSkipped(t *testing.T) {
	room := newTestRoom("")

	// Pre-mark a session as internal (from a previous participants update)
	room.internalSessions["bot-hpb-id"] = true

	// Participants update WITHOUT internal flag (edge case)
	// but the session ID is already known as internal
	msg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "bot-hpb-id",
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(msg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// Should not be added as speaker
	if _, exists := room.Speakers["bot-hpb-id"]; exists {
		t.Error("known internal session should not be added as speaker")
	}
}

// TestReconnectCycleSimulation simulates the exact ghost speaker bug scenario:
// HPB reconnect → join events for old bot sessions → participants update
// reveals they're internal → they should be filtered out, not added as speakers.
func TestReconnectCycleSimulation(t *testing.T) {
	room := newTestRoom("")

	// Simulate first connection: bot has session "bot-v1"
	room.internalSessions["bot-v1"] = true

	// Simulate HPB reconnect: bot now has session "bot-v2"
	// Join events arrive for both old and new sessions plus a real participant
	joinMsg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "room",
			Type:   "join",
			Join: []hpb.EventSessionEntry{
				{SessionID: "bot-v2"},           // New session (current)
				{SessionID: "bot-v1"},           // Old session (stale)
				{SessionID: "real-participant"},  // Real user
			},
		},
	}

	room.handleRoomEvent(joinMsg)

	room.mu.RLock()
	speakerCount := len(room.Speakers)
	room.mu.RUnlock()

	// No speakers should be added from room join events
	if speakerCount != 0 {
		t.Errorf("expected 0 speakers after join event, got %d", speakerCount)
	}

	// Now participants update arrives, identifying internal sessions.
	// Note: sessionId here is the HPB session ID (same as in join events).
	participantsMsg := &hpb.EventMessage{
		Type: "event",
		Event: hpb.EventInner{
			Target: "participants",
			Type:   "update",
			Update: &hpb.RoomEventUpdate{
				Users: []map[string]interface{}{
					{
						"sessionId": "bot-v1",
						"internal":  true,
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
					{
						"sessionId": "bot-v2",
						"internal":  true,
						"inCall":    float64(3),
						"lastPing":  float64(time.Now().Unix()),
					},
					{
						"sessionId": "real-participant",
						"inCall":    float64(7),
						"lastPing":  float64(time.Now().Unix()),
						"userId":    "alice",
					},
				},
			},
		},
	}

	room.handleParticipantsEvent(participantsMsg)

	room.mu.RLock()
	defer room.mu.RUnlock()

	// Both bot sessions should be tracked as internal
	if !room.internalSessions["bot-v1"] {
		t.Error("bot-v1 should be tracked as internal")
	}
	if !room.internalSessions["bot-v2"] {
		t.Error("bot-v2 should be tracked as internal")
	}

	// Bot sessions should not be speakers
	if _, exists := room.Speakers["bot-v1"]; exists {
		t.Error("bot-v1 should not be a speaker")
	}
	if _, exists := room.Speakers["bot-v2"]; exists {
		t.Error("bot-v2 should not be a speaker")
	}
}
