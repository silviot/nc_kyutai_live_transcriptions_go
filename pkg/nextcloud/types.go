package nextcloud

import "encoding/json"

// OCSResponse represents a generic OCS API response
type OCSResponse struct {
	OCS struct {
		Meta OCSMeta         `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

// OCSMeta contains OCS response metadata
type OCSMeta struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statuscode"`
	Message    string `json:"message"`
}

// Participant represents a room participant
type Participant struct {
	SessionID       string `json:"sessionId"`
	UserID          string `json:"actorId"`
	ActorType       string `json:"actorType"`
	DisplayName     string `json:"displayName"`
	ParticipantType int    `json:"participantType"`
	InCall          int    `json:"inCall"`
}

// SignalingSettings contains HPB signaling configuration
type SignalingSettings struct {
	Server          string                       `json:"server"`
	Ticket          string                       `json:"ticket"`
	STUNServers     []STUNServer                 `json:"stunservers"`
	TURNServers     []TURNServer                 `json:"turnservers"`
	HelloAuthParams map[string]HelloAuthParamsV2 `json:"helloAuthParams"`
	SIPDialIn       interface{}                  `json:"sipDialIn"`
	Federation      interface{}                  `json:"federation"`
}

// HelloAuthParamsV2 contains hello auth params for signaling
// For v1.0: userid + ticket fields
// For v2.0: token field (JWT)
type HelloAuthParamsV2 struct {
	URL    string `json:"url,omitempty"`
	Ticket string `json:"ticket,omitempty"`
	UserID string `json:"userid,omitempty"`
	Token  string `json:"token,omitempty"` // JWT token for v2.0
}

// STUNServer represents a STUN server configuration
type STUNServer struct {
	URLs []string `json:"urls"`
}

// TURNServer represents a TURN server configuration
type TURNServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

// RoomInfo represents room information
type RoomInfo struct {
	Token           string `json:"token"`
	Type            int    `json:"type"`
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description"`
	ParticipantType int    `json:"participantType"`
	SessionID       string `json:"sessionId"`
	ActorID         string `json:"actorId"`
	ActorType       string `json:"actorType"`
	InCall          int    `json:"inCall"`
	CanEnableSIP    bool   `json:"canEnableSIP"`
}

// CallJoinResponse represents the response when joining a call
type CallJoinResponse struct {
	// Empty response typically
}

// TranscriptionInfo represents transcription status
type TranscriptionInfo struct {
	Enabled    bool   `json:"enabled"`
	LanguageID string `json:"languageId"`
}
