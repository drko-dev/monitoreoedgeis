package rtsp

import (
	"errors"
	"time"
)

// State represents the connectivity status of an RTSP stream (Hito G).
type State string

const (
	StateConnecting State = "connecting"
	StateOnline     State = "online"
	StateDegraded   State = "degraded"
	StateOffline    State = "offline"
)

const (
	StreamRoleMain = "main"
	StreamRoleSub  = "sub"
)

var (
	ErrTimeout      = errors.New("rtsp: read timeout (stream silent)")
	ErrClosed       = errors.New("rtsp: connection closed")
	ErrAuthFailed   = errors.New("rtsp: digest authentication failed")
	ErrSetupFailed  = errors.New("rtsp: setup failed")
	ErrPlayFailed   = errors.New("rtsp: play failed")
	ErrNoVideoTrack = errors.New("rtsp: no video track in sdp")
)

// CameraStreamStatus captures the live connectivity state and network metrics
// for a single camera (G4, G10, G11). It is strictly sanitized: NO secrets.
type CameraStreamStatus struct {
	CandidateKey    string     `json:"candidate_key"`
	Status          State      `json:"status"`
	StreamRole      string     `json:"stream_role"`
	Codec           string     `json:"codec,omitempty"`
	Width           int        `json:"width,omitempty"`
	Height          int        `json:"height,omitempty"`
	FPS             float64    `json:"fps,omitempty"`
	ReconnectCount  int64      `json:"reconnect_count"`
	PacketsReceived int64      `json:"packets_received"`
	BytesReceived   int64      `json:"bytes_received"`
	LastPacketAt    *time.Time `json:"last_packet_at,omitempty"`
	LastErrorSafe   string     `json:"last_error_safe,omitempty"`
}

// Config holds timing and behavior parameters for RTSP stream supervisors.
type Config struct {
	StreamRole     string        // "sub" (default) or "main"
	PacketTimeout  time.Duration // Silence threshold (default 5s)
	InitialBackoff time.Duration // Reconnect initial backoff (default 1s)
	MaxBackoff     time.Duration // Reconnect maximum backoff (default 60s)
	DialTimeout    time.Duration // TCP dial / handshake timeout (default 5s)
	Enabled        bool          // Whether connectivity supervision is enabled
}

// DefaultConfig returns safe production defaults for Hito G.
func DefaultConfig() Config {
	return Config{
		StreamRole:     StreamRoleSub,
		PacketTimeout:  5 * time.Second,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     60 * time.Second,
		DialTimeout:    5 * time.Second,
		Enabled:        true,
	}
}
