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

// RoomMessage contains room topology and configuration
type RoomMessage struct {
	Type         string        `json:"type"` // "room"
	RoomToken    string        `json:"token,omitempty"`
	SessionID    string        `json:"sessionid,omitempty"`
	ResumeID     string        `json:"resumeid,omitempty"`
	Properties   RoomProperties `json:"properties,omitempty"`
	Participants []Participant `json:"participants,omitempty"`
	STUNServers  []string      `json:"stunservers,omitempty"`
	TURNServers  []TURNServer  `json:"turnservers,omitempty"`
}

// RoomProperties contains room settings
type RoomProperties struct {
	InCall   bool   `json:"incall"`
	RoomType string `json:"type"` // "room", "group", etc
	Breakout *int   `json:"breakout,omitempty"`
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

// MessageMessage contains call signaling data
type MessageMessage struct {
	Type      string                 `json:"type"` // "message"
	From      string                 `json:"from,omitempty"`
	To        string                 `json:"to,omitempty"`
	RoomType  string                 `json:"roomType,omitempty"`
	RoomToken string                 `json:"roomToken,omitempty"`
	Recipient *MessageRecipient      `json:"recipient,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Sid       string                 `json:"sid,omitempty"`
	Refresh   string                 `json:"refresh,omitempty"`
	Token     string                 `json:"token,omitempty"`
	URLPath   string                 `json:"url_path,omitempty"`
	URLQuery  string                 `json:"url_query,omitempty"`
	URLTarget string                 `json:"url_target,omitempty"`
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

// EventInner contains event details
type EventInner struct {
	Target string                 `json:"target"` // "room", "participants"
	Type   string                 `json:"type"`   // "join", "leave", "update"
	Update map[string]interface{} `json:"update,omitempty"`
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
