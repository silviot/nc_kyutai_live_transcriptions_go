package hpb

// Message represents a generic HPB message
type Message struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	// Additional fields depend on message type
}

// HelloMessage is sent to initiate HPB session
type HelloMessage struct {
	Type      string `json:"type"` // "hello"
	ResumeID  string `json:"resumeid,omitempty"`
	SessionID string `json:"sessionid,omitempty"`
}

// InCallMessage indicates we are ready for a call in a specific room
type InCallMessage struct {
	Type      string `json:"type"` // "incall"
	RoomToken string `json:"room,omitempty"`
	SessionID string `json:"sessionid,omitempty"`
}

// RoomMessage contains room topology and configuration
type RoomMessage struct {
	Type      string           `json:"type"` // "room"
	RoomToken string           `json:"token,omitempty"`
	SessionID string           `json:"sessionid,omitempty"`
	ResumeID  string           `json:"resumeid,omitempty"`
	Properties RoomProperties   `json:"properties,omitempty"`
	Participants []Participant   `json:"participants,omitempty"`
	STUNServers []string        `json:"stunservers,omitempty"`
	TURNServers []TURNServer    `json:"turnservers,omitempty"`
}

// RoomProperties contains room settings
type RoomProperties struct {
	InCall       bool   `json:"incall"`
	RoomType     string `json:"type"` // "room", "group", etc
	Breakout     *int   `json:"breakout,omitempty"`
}

// Participant represents a room participant
type Participant struct {
	SessionID  string `json:"sessionid"`
	UserID     string `json:"userid"`
	Name       string `json:"name"`
	Audio      bool   `json:"audio"`
	Video      bool   `json:"video"`
	ScreenShare bool  `json:"screenshare"`
	InCall     bool   `json:"incall"`
}

// TURNServer represents TURN server configuration
type TURNServer struct {
	Urls       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// MessageMessage contains call signaling data
type MessageMessage struct {
	Type       string          `json:"type"` // "message"
	From       string          `json:"from,omitempty"`
	To         string          `json:"to,omitempty"`
	RoomType   string          `json:"roomType,omitempty"`
	RoomToken  string          `json:"roomToken,omitempty"`
	Recipient  *MessageRecipient `json:"recipient,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
	Sid        string          `json:"sid,omitempty"`
	Refresh    string          `json:"refresh,omitempty"`
	Token      string          `json:"token,omitempty"`
	URLPath    string          `json:"url_path,omitempty"`
	URLQuery   string          `json:"url_query,omitempty"`
	URLTarget  string          `json:"url_target,omitempty"`
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
	Type    string `json:"type"` // "error"
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// RequestOfferMessage is a request to initiate a WebRTC offer
type RequestOfferMessage struct {
	Type    string `json:"type"`  // "requestoffer"
	SessionID string `json:"sessionid,omitempty"`
}

// RequestAnswerMessage is a request to provide a WebRTC answer
type RequestAnswerMessage struct {
	Type    string `json:"type"`  // "requestanswer"
	SessionID string `json:"sessionid,omitempty"`
	Offer   string `json:"offer,omitempty"` // SDP offer
}

// PingMessage is a keep-alive message
type PingMessage struct {
	Type string `json:"type"` // "ping"
}

// PongMessage is a keep-alive response
type PongMessage struct {
	Type string `json:"type"` // "pong"
}
