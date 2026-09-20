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
	StateAuthFailed State = "auth_failed"
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

// PacketSink receives raw RTP payloads for the VIDEO channel of a camera
// stream only — RTCP is filtered out in Supervisor.streamLoop before this
// interface is ever called, using the interleaved channel actually
// negotiated in SETUP (see Session.VideoChannel), never a hardcoded number.
//
// OnPacket must not block: enqueuing non-blockingly is the sink's own
// responsibility, and it must be safe to call concurrently with a
// SetPacketSink(nil) deregistering it (no send-on-closed-channel).
type PacketSink interface {
	OnPacket(candidateKey string, payload []byte, recvAt time.Time)
}

// StreamDescriptor is the minimal, non-sensitive metadata a video pipeline
// needs to decode a camera's stream. It deliberately excludes Username,
// Password, and any RTSP URI — only CameraTarget's safe fields plus SDP-
// derived codec parameters.
type StreamDescriptor struct {
	CandidateKey       string
	Codec              string
	Width              int
	Height             int
	FPS                float64
	StreamRole         string
	SpropParameterSets [][]byte
}

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
	TimeoutCount    int64      `json:"timeout_count"`
	StallCount      int64      `json:"stall_count"`
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
