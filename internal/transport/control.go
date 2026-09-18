package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ControlCommand is an audited server-issued command. Payload is deliberately
// empty today; accepting arbitrary data would create a remote execution API.
type ControlCommand struct {
	ID          string         `json:"id"`
	CommandType string         `json:"command_type"`
	Payload     map[string]any `json:"payload"`
	ExpiresAt   string         `json:"expires_at"`
}

func (c *Client) ClaimNextControlCommand(ctx context.Context, deviceID, credential string) (*ControlCommand, error) {
	status, header, body, err := c.do(ctx, http.MethodGet, ControlNextPath, deviceID, credential, nil)
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
	if status == http.StatusRequestTimeout || status >= 500 {
		return nil, fmt.Errorf("%w (status %d)", ErrRetryableStatus, status)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
	var response struct {
		Command ControlCommand `json:"command"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%w: decode control command: %v", ErrUnexpectedStatus, err)
	}
	return &response.Command, nil
}

func (c *Client) ReportControlCommand(ctx context.Context, deviceID, credential, commandID, commandStatus string, result map[string]any, errorCode string) error {
	status, header, _, err := c.do(ctx, http.MethodPost, fmt.Sprintf(ControlReportPath, commandID), deviceID, credential, map[string]any{"status": commandStatus, "result": result, "error_code": errorCode})
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	}
	if status == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	}
	if status == http.StatusRequestTimeout || status >= 500 {
		return fmt.Errorf("%w (status %d)", ErrRetryableStatus, status)
	}
	return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
}
