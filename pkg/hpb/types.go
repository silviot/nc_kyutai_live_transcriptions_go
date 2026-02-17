package hpb

// Message represents a generic HPB message
type Message struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	// Additional fields depend on message type
}

// HelloRequest is sent to initiate HPB session (outgoing)
type HelloRequest struct {
	Type  string     `json:"type"` // "hello"
	ID    string     `json:"id,omitempty"`
	Hello HelloInner `json:"hello"`
}

// HelloInner contains hello authentication details
type HelloInner struct {
	Version string    `json:"version"` // "2.0"
	Auth    HelloAuth `json:"auth"`
}

// HelloAuth contains authentication parameters
type HelloAuth struct {
	Type   string          `json:"type"` // "internal"
	Params HelloAuthParams `json:"params"`
}

// HelloAuthParams contains internal auth parameters
type HelloAuthParams struct {
	Random  string `json:"random"`  // nonce
	Token   string `json:"token"`   // HMAC(secret, nonce)
	Backend string `json:"backend"` // backend URL
}

// HelloResponse is received after successful hello (incoming)
type HelloResponse struct {
	Type  string            `json:"type"` // "hello"
	Hello HelloResponseData `json:"hello"`
}

// HelloResponseData contains session info from hello response
type HelloResponseData struct {
	SessionID string                 `json:"sessionid"`
	ResumeID  string                 `json:"resumeid"`
	Server    map[string]interface{} `json:"server,omitempty"` // Server info object
}

// InternalMessage is used for internal HPB commands
type InternalMessage struct {
	Type     string        `json:"type"` // "internal"
	ID       string        `json:"id,omitempty"`
	Internal InternalInner `json:"internal"`
}

// InternalInner contains the internal command
type InternalInner struct {
	Type   string      `json:"type"` // "incall", "addtarget", etc
	InCall *InCallData `json:"incall,omitempty"`
}

// InCallData contains incall status
type InCallData struct {
	InCall int `json:"incall"` // 1 = IN_CALL
}

// RoomMessage is the HPB room join response.
// HPB nests the room data under a "room" key: {"type":"room","room":{"roomid":"...",...}}
type RoomMessage struct {
	Type string       `json:"type"` // "room"
	ID   string       `json:"id,omitempty"`
	Room RoomInner    `json:"room"`
	// Legacy flat fields for backward compatibility with tests
	RoomToken    string        `json:"token,omitempty"`
}

// RoomInner contains the nested room data from HPB
type RoomInner struct {
	RoomID     string          `json:"roomid"`
	Properties *RoomProperties `json:"properties,omitempty"`
	Bandwidth  *RoomBandwidth  `json:"bandwidth,omitempty"`
}

// RoomBandwidth contains bandwidth limits from HPB
type RoomBandwidth struct {
	MaxStreamBitrate int `json:"maxstreambitrate,omitempty"`
	MaxScreenBitrate int `json:"maxscreenbitrate,omitempty"`
}

// RoomProperties contains room settings.
// Fields are interface{} because HPB sends numeric types (not strings/bools).
type RoomProperties struct {
	InCall   interface{} `json:"incall"`   // int flags or bool
	RoomType interface{} `json:"type"`     // int room type
	Name     string      `json:"name,omitempty"`
}

// Participant represents a room participant
type Participant struct {
	SessionID   string `json:"sessionid"`
	UserID      string `json:"userid"`
	Name        string `json:"name"`
	Audio       bool   `json:"audio"`
	Video       bool   `json:"video"`
	ScreenShare bool   `json:"screenshare"`
	InCall      bool   `json:"incall"`
}

// TURNServer represents TURN server configuration
type TURNServer struct {
	Urls       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// MessageMessage contains call signaling data from HPB
// HPB wraps the content in a nested "message" field:
// {"type": "message", "id": "42", "message": {"sender": {...}, "recipient": {...}, "data": {...}}}
type MessageMessage struct {
	Type    string          `json:"type"`         // "message"
	ID      string          `json:"id,omitempty"` // message correlation ID
	Message MessageEnvelope `json:"message"`
}

// MessageEnvelope is the inner content of a message
type MessageEnvelope struct {
	Sender    *MessageParty          `json:"sender,omitempty"`
	Recipient *MessageRecipient      `json:"recipient,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// MessageParty identifies a message sender
type MessageParty struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionid,omitempty"`
	UserID    string `json:"userid,omitempty"`
}

// MessageRecipient identifies who this message is for
type MessageRecipient struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionid,omitempty"`
	UserID    string `json:"userid,omitempty"`
}

// ByeMessage indicates a participant or session is leaving
type ByeMessage struct {
	Type      string `json:"type"` // "bye"
	SessionID string `json:"sessionid,omitempty"`
	RoomToken string `json:"room,omitempty"`
	Reason    string `json:"reason,omitempty"` // "disconnect", "timeout", etc
}

// ErrorMessage contains error information from HPB
type ErrorMessage struct {
	Type    string     `json:"type"` // "error"
	Error   ErrorInner `json:"error,omitempty"`
	Code    int        `json:"code,omitempty"`    // legacy
	Message string     `json:"message,omitempty"` // legacy
}

// ErrorInner contains error details
type ErrorInner struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// EventMessage contains room/participant events
type EventMessage struct {
	Type  string     `json:"type"` // "event"
	Event EventInner `json:"event"`
}

// EventInner contains event details.
// For target "room": uses Join/Leave/Change fields.
// For target "participants": uses Update field (RoomEventUpdate).
type EventInner struct {
	Target string `json:"target"` // "room", "participants"
	Type   string `json:"type"`   // "join", "leave", "update"

	// Used for target "room"
	Join   []EventSessionEntry `json:"join,omitempty"`
	Leave  []string            `json:"leave,omitempty"` // Session IDs leaving
	Change []EventSessionEntry `json:"change,omitempty"`

	// Used for target "participants"
	Update *RoomEventUpdate `json:"update,omitempty"`
}

// EventSessionEntry represents a session in a room join/leave/change event
type EventSessionEntry struct {
	SessionID     string                 `json:"sessionid"`
	UserID        string                 `json:"userid,omitempty"`
	RoomSessionID string                 `json:"roomsessionid,omitempty"` // OCS session ID
	User          map[string]interface{} `json:"user,omitempty"`
	Features      []string               `json:"features,omitempty"`
}

// RoomEventUpdate represents a participants update from the Nextcloud backend.
// Sent as event.update when target is "participants".
type RoomEventUpdate struct {
	RoomID  string                   `json:"roomid,omitempty"`
	InCall  interface{}              `json:"incall,omitempty"`  // Global incall status
	All     bool                     `json:"all,omitempty"`     // True if applies to all users
	Changed []map[string]interface{} `json:"changed,omitempty"` // Changed participants
	Users   []map[string]interface{} `json:"users,omitempty"`   // Full participant list
}

// WelcomeMessage is received on initial connection
type WelcomeMessage struct {
	Type    string `json:"type"` // "welcome"
	Version string `json:"version,omitempty"`
}

// PingMessage is a keep-alive message
type PingMessage struct {
	Type string `json:"type"` // "ping"
}

// PongMessage is a keep-alive response
type PongMessage struct {
	Type string `json:"type"` // "pong"
}

// CallFlag represents in-call status flags
const (
	CallFlagInCall = 1 // IN_CALL flag
)

// RoomJoinRequest is sent to join a room via HPB signaling
type RoomJoinRequest struct {
	Type string        `json:"type"` // "room"
	ID   string        `json:"id,omitempty"`
	Room RoomJoinInner `json:"room"`
}

// RoomJoinInner contains room join parameters
type RoomJoinInner struct {
	RoomID    string `json:"roomid"`              // Room token
	SessionID string `json:"sessionid,omitempty"` // Nextcloud session ID
}

// TranscriptMessage is kept for backward compatibility - use MessageMessage with proper envelope instead
