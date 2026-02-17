package session

import (
	"encoding/json"
	"net/http"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/webrtc"
)

// TranscribeRequest matches the Nextcloud Talk ExApp transcription request format.
type TranscribeRequest struct {
	RoomToken   string              `json:"roomToken"`
	NcSessionID string              `json:"ncSessionId"`
	Enable      *bool               `json:"enable,omitempty"`   // nil defaults to true
	LangID      string              `json:"langId"`             // Nextcloud uses langId
	LanguageID  string              `json:"languageId"`         // Also accept languageId
	TURNServers []TURNServerRequest `json:"turnServers,omitempty"`
}

// TURNServerRequest represents TURN server config in API request
type TURNServerRequest struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// HandleTranscribeRequest handles POST /api/v1/call/transcribe
func (m *Manager) HandleTranscribeRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req TranscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// Validate
	if req.RoomToken == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "roomToken required"})
		return
	}

	// Resolve language (prefer langId, fall back to languageId)
	langID := req.LangID
	if langID == "" {
		langID = req.LanguageID
	}
	if langID == "" {
		langID = "en"
	}

	// Check enable flag (defaults to true if absent)
	enable := true
	if req.Enable != nil {
		enable = *req.Enable
	}

	// If disabling, leave the room
	if !enable {
		if err := m.LeaveRoom(req.RoomToken); err != nil {
			m.logger.Error("failed to leave room", "roomToken", req.RoomToken, "error", err)
			// Not an error if room not found (already left)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"enabled":  false,
			"language": langID,
		})
		return
	}

	// Validate language support
	if langID != "en" && langID != "fr" {
		m.logger.Warn("unsupported language requested, using default", "requested", langID)
		langID = "en"
	}

	// Convert TURN servers
	turnServers := make([]webrtc.TURNServer, len(req.TURNServers))
	for i, ts := range req.TURNServers {
		turnServers[i] = webrtc.TURNServer{
			URLs:       ts.URLs,
			Username:   ts.Username,
			Credential: ts.Credential,
		}
	}

	// Join room
	_, err := m.JoinRoom(r.Context(), req.RoomToken, langID, turnServers)
	if err != nil {
		m.logger.Error("failed to join room", "roomToken", req.RoomToken, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"enabled":  true,
		"language": langID,
	})
}

// HandleStopTranscriptionRequest handles DELETE /api/v1/call/transcribe/{roomToken}
func (m *Manager) HandleStopTranscriptionRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	roomToken := r.PathValue("roomToken")
	if roomToken == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "roomToken required"})
		return
	}

	if err := m.LeaveRoom(roomToken); err != nil {
		m.logger.Error("failed to leave room", "roomToken", roomToken, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "stopped",
		"roomToken": roomToken,
	})
}
