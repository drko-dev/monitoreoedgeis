package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// RemoteConfig is the versioned desired-configuration document assigned by
// SaaS (Hito O). Payload is opaque here -- its schema (FPS, ROI, models,
// processing mode, ...) belongs to the runtime adapter that consumes it
// (IA2), not to this transport layer or to internal/remoteconfig's core
// engine. Version must be a monotonically comparable integer.
type RemoteConfig struct {
	Version int64           `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// GetDesiredConfig polls for the current desired remote-config document.
// A nil result with a nil error means no config is currently assigned.
func (c *Client) GetDesiredConfig(ctx context.Context, deviceID, credential string) (*RemoteConfig, error) {
	status, header, body, err := c.do(ctx, http.MethodGet, RemoteConfigNextPath, deviceID, credential, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	}
	if status == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(header)}
	}
	if isRetryableStatus(status) {
		return nil, fmt.Errorf("%w (status %d)", ErrRetryableStatus, status)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
	var response struct {
		Config RemoteConfig `json:"config"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%w: decode remote config: %v", ErrUnexpectedStatus, err)
	}
	return &response.Config, nil
}

// AckRemoteConfig reports the terminal outcome of applying one config
// version: status is one of "applied", "failed", or "rolled_back".
func (c *Client) AckRemoteConfig(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error {
	respStatus, header, _, err := c.do(ctx, http.MethodPost, RemoteConfigAckPath, deviceID, credential, map[string]any{
		"version":    version,
		"status":     status,
		"error_code": errorCode,
	})
	if err != nil {
		return err
	}
	if respStatus == http.StatusOK {
		return nil
	}
	if respStatus == http.StatusUnauthorized || respStatus == http.StatusForbidden {
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, respStatus)
	}
	if respStatus == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	}
	if isRetryableStatus(respStatus) {
		return fmt.Errorf("%w (status %d)", ErrRetryableStatus, respStatus)
	}
	return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, respStatus)
}
