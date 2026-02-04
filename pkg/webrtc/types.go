package webrtc

// ConnectionConfig holds WebRTC configuration
type ConnectionConfig struct {
	STUN []string // STUN server URLs
	TURN []TURNServer
}

// TURNServer represents a TURN server
type TURNServer struct {
	URLs       []string
	Username   string
	Credential string
}

// AudioFrame represents a frame of audio data
type AudioFrame struct {
	Data      []float32 // Audio samples as float32 [-1.0, 1.0]
	SampleRate int       // Sample rate in Hz
	Channels   int       // Number of channels (1 = mono, 2 = stereo)
	Timestamp  int64     // Timestamp in milliseconds
}
