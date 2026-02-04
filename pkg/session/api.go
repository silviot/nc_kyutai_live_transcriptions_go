package session

import (
	"encoding/json"
	"net/http"

	"github.com/silviot/nc_kyutai_live_transcriptions_go/pkg/webrtc"
)

// TranscribeRequest is the incoming request to start transcription
type TranscribeRequest struct {
	RoomToken  string `json:"roomToken"`
	LanguageID string `json:"languageId"`
	TURNServers []TURNServerRequest `json:"turnServers,omitempty"`
}

// TURNServerRequest represents TURN server config in API request
type TURNServerRequest struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// TranscribeResponse is the response after starting transcription
type TranscribeResponse struct {
	Status    string `json:"status"`
	RoomToken string `json:"roomToken"`
	Message   string `json:"message,omitempty"`
}

// HandleTranscribeRequest handles POST /api/v1/call/transcribe
func (m *Manager) HandleTranscribeRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

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

	if req.LanguageID == "" {
		req.LanguageID = "en"
	}

	// Validate language support
	if req.LanguageID != "en" && req.LanguageID != "fr" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unsupported language"})
		return
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
	_, err := m.JoinRoom(r.Context(), req.RoomToken, req.LanguageID, turnServers)
	if err != nil {
		m.logger.Error("failed to join room", "roomToken", req.RoomToken, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(TranscribeResponse{
		Status:    "started",
		RoomToken: req.RoomToken,
		Message:   "transcription started",
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

