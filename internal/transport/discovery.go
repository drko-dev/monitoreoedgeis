package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrRunNotFound is returned when a discovery run does not exist or does not belong to this gateway (HTTP 404).
	ErrRunNotFound = errors.New("transport: discovery run not found or not owned by this gateway")
	// ErrRunConflict is returned when a discovery run is not in a valid state for reporting or has conflicting data (HTTP 409).
	ErrRunConflict = errors.New("transport: discovery run conflict")
)

// DiscoveryNextResponse is the body returned by GET DiscoveryNextPath when a run is claimed.
type DiscoveryNextResponse struct {
	RunID int `json:"run_id"`
}

// DiscoveryCandidatePayload represents a single discovered device candidate sent to the SaaS.
// It strictly maps to the SaaS DiscoveryCandidatePayload allowlist.
type DiscoveryCandidatePayload struct {
	Protocol     string `json:"protocol,omitempty"`
	EndpointHost string `json:"endpoint_host,omitempty"`
	EndpointPort int    `json:"endpoint_port,omitempty"`
	EndpointPath string `json:"endpoint_path,omitempty"`
	EPRAddress   string `json:"epr_address,omitempty"`
	Types        string `json:"types,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Firmware     string `json:"firmware,omitempty"`
	DeviceType   string `json:"device_type,omitempty"`
	AuthRequired bool   `json:"auth_required"`
}

// DiscoveryReportRequest is the payload sent to ReportDiscoveryRun.
type DiscoveryReportRequest struct {
	Status       string                      `json:"status"` // "completed" or "failed"
	Candidates   []DiscoveryCandidatePayload `json:"candidates,omitempty"`
	ErrorCode    string                      `json:"error_code,omitempty"`
	ErrorMessage string                      `json:"error_message,omitempty"`
}

// DiscoveryReportResponse is the confirmation returned by the SaaS on successful report.
type DiscoveryReportResponse struct {
	Result         string `json:"result"`
	CandidateCount int    `json:"candidate_count,omitempty"`
}

// ClaimNextDiscoveryRun checks if there is a pending discovery run assigned to this gateway.
// Returns (nil, nil) if no run is pending (HTTP 204 No Content).
// Returns (&runID, nil) if a run was claimed (HTTP 200 OK).
func (c *Client) ClaimNextDiscoveryRun(ctx context.Context, deviceID, credential string) (*int, error) {
	status, header, body, err := c.do(ctx, http.MethodGet, DiscoveryNextPath, deviceID, credential, nil)
	if err != nil {
		return nil, err
	}

	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var resp DiscoveryNextResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("%w: decoding claim discovery run response: %v", ErrUnexpectedStatus, err)
		}
		return &resp.RunID, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case http.StatusTooManyRequests:
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(header)}
	default:
		return nil, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}

// ReportDiscoveryRun submits the results of a discovery run (candidates or failure) to the SaaS.
func (c *Client) ReportDiscoveryRun(ctx context.Context, deviceID, credential string, runID int, req DiscoveryReportRequest) error {
	path := fmt.Sprintf(DiscoveryReportPath, runID)
	status, header, body, err := c.do(ctx, http.MethodPost, path, deviceID, credential, req)
	if err != nil {
		return err
	}

	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: run %d not found or not owned by device %s", ErrRunNotFound, runID, deviceID)
	case http.StatusConflict:
		return fmt.Errorf("%w: run %d conflict: %s", ErrRunConflict, runID, string(body))
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (status %d): %s", ErrInvalidRequest, status, string(body))
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	default:
		return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}
