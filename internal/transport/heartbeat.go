package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ErrRateLimited is returned for HTTP 429. Callers should honour the
// RetryAfter carried by RateLimitError rather than applying their own backoff
// when the server stated one.
var ErrRateLimited = errors.New("transport: rate limited by SaaS")

// RateLimitError carries the server-stated cooldown from a 429 response.
// RetryAfter is zero when the response omitted or malformed the header.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%v (retry after %s)", ErrRateLimited, e.RetryAfter)
	}
	return ErrRateLimited.Error()
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// HeartbeatSystem is the resource snapshot nested under "metrics" in a
// heartbeat. Every field is optional: a host that cannot measure something
// omits it rather than sending a zero that reads as a real measurement.
//
// It deliberately reuses the SaaS's existing "metrics" object — which already
// carried cpu_percent — rather than introducing a second, parallel telemetry
// container next to it.
type HeartbeatSystem struct {
	CPUPercent       *float64 `json:"cpu_percent,omitempty"`
	MemoryTotalBytes uint64   `json:"memory_total_bytes,omitempty"`
	MemoryUsedBytes  uint64   `json:"memory_used_bytes,omitempty"`
	DiskTotalBytes   uint64   `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes    uint64   `json:"disk_used_bytes,omitempty"`
	// TemperatureC sits with the other measurements rather than at the top
	// level, and is omitted entirely on a host with no thermal sensor.
	TemperatureC *float64 `json:"temperature_c,omitempty"`
}

// HeartbeatRequest is the body sent to HeartbeatPath. The SaaS validates it
// with Pydantic extra="forbid", so field names must match the server model
// exactly.
//
// It deliberately carries no tenant/site/organization: the SaaS derives all
// of those from the authenticated credential and must never take the Edge's
// word for them. EdgeID is sent only so the SaaS can *verify* it against the
// authenticated device — it is a cross-check, never an identity claim, and
// never a secret.
type HeartbeatRequest struct {
	EdgeID string `json:"edge_id,omitempty"`
	// EdgeVersion reuses the SaaS's existing agent-version field, which the
	// legacy Python agents already populate and the Edge admin UI already
	// renders. A second "agent_version" alongside it would have been the same
	// fact under two names.
	EdgeVersion    string `json:"edge_version,omitempty"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	Architecture   string `json:"architecture,omitempty"`
	ProcessingMode string `json:"processing_mode,omitempty"`
	// HealthStatus is the Edge's own view of itself (READY/DEGRADED/...).
	// Online vs offline is the SaaS's call, derived from arrival time — this
	// field never claims connectivity.
	HealthStatus string `json:"health_status,omitempty"`
	// BootID and SequenceNumber let the SaaS discard a stale snapshot that
	// overtakes a newer one; they reuse the existing legacy fields.
	BootID         string `json:"boot_id,omitempty"`
	SequenceNumber int64  `json:"sequence_number,omitempty"`
	// EdgeTimestamp is diagnostic only. The authoritative last_seen is the
	// SaaS's own clock at arrival.
	EdgeTimestamp string               `json:"edge_timestamp,omitempty"`
	System        HeartbeatSystem      `json:"metrics"`
	Cameras       []CameraStreamStatus `json:"cameras,omitempty"`
}

// CameraStreamStatus carries live camera RTSP connectivity telemetry (Milestone G).
type CameraStreamStatus struct {
	CandidateKey    string  `json:"candidate_key"`
	Status          string  `json:"status"`
	StreamRole      string  `json:"stream_role,omitempty"`
	Codec           string  `json:"codec,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	FPS             float64 `json:"fps,omitempty"`
	ReconnectCount  int64   `json:"reconnect_count"`
	PacketsReceived int64   `json:"packets_received"`
	BytesReceived   int64   `json:"bytes_received"`
	LastPacketAt    *string `json:"last_packet_at,omitempty"`
	LastErrorSafe   string  `json:"last_error_safe,omitempty"`
}

// Heartbeat posts one heartbeat authenticated with deviceID + credential.
//
// Error classification is the caller's scheduling signal:
//   - ErrUnauthorized (401/403): credential revoked or device disabled. Never
//     retry aggressively, never re-enroll, never discard the credential.
//   - *RateLimitError (429): honour RetryAfter.
//   - ErrTimeout / ErrSaaSUnavailable / ErrUnexpectedStatus (5xx): transient,
//     apply backoff.
//   - ErrInvalidRequest (422): the payload does not match the server model.
//     Retrying an identical body cannot help, so it is reported as its own
//     class rather than hidden inside the transient bucket.
func (c *Client) Heartbeat(ctx context.Context, deviceID, credential string, req HeartbeatRequest) error {
	status, header, body, err := c.do(ctx, http.MethodPost, HeartbeatPath, deviceID, credential, req)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case status == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	case status == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (status %d): %s", ErrInvalidRequest, status, string(body))
	default:
		return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}

// parseRetryAfter reads a Retry-After header in its delta-seconds form, the
// only form the SaaS emits. An absent, malformed or negative value yields 0,
// which tells the caller to fall back to its own backoff.
func parseRetryAfter(h http.Header) time.Duration {
	raw := h.Get("Retry-After")
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
