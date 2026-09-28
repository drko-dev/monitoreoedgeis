package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CameraOnboardingPath is the device-facing endpoint an authenticated Edge
// (device_kind == "edge") uses to onboard one single-source IP camera it
// already discovered and validated locally (ONVIF + RTSP).
//
// Confirmed against the SaaS source (monitoreoia):
// geocam/routers/edge_camera_onboarding.py, router.post("").
const CameraOnboardingPath = "/api/v1/edge/camera-onboarding"

// CameraOnboardingRequest is this Edge's request to onboard one camera. It
// deliberately carries no organization_id/device_id: the SaaS resolves both
// from the authenticated device, and rejects any such field outright
// (extra="forbid" on its Pydantic model).
type CameraOnboardingRequest struct {
	CandidateKey   string `json:"candidate_key"`
	CameraName     string `json:"camera_name"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	Model          string `json:"model,omitempty"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CameraOnboardingResult mirrors geocam/routers/edge_camera_onboarding.py's
// _public_operation: never a password, never a raw org/device id beyond
// what the Edge itself already knows.
type CameraOnboardingResult struct {
	OperationID  int64  `json:"operation_id"`
	CameraID     int64  `json:"camera_id"`
	CredentialID int64  `json:"credential_id"`
	CandidateKey string `json:"candidate_key"`
	// Status is "created", "rotated", or "replayed" (an exact idempotent
	// retry) -- see the SaaS doc, docs/saas/20-edge-camera-onboarding.md.
	Status string `json:"status"`
}

type cameraOnboardingEnvelope struct {
	Onboarding CameraOnboardingResult `json:"onboarding"`
}

// OnboardingRejection is a typed, safe rejection the SaaS returned (409/403)
// -- never a raw HTTP status alone, so callers can render a specific,
// non-generic message.
type OnboardingRejection struct {
	StatusCode int
	Detail     string
}

func (e *OnboardingRejection) Error() string {
	return fmt.Sprintf("camera onboarding rejected (status %d): %s", e.StatusCode, e.Detail)
}

// OnboardCamera calls the SaaS's authoritative onboarding endpoint. A
// 401/403 for a wrong device_kind maps to ErrUnauthorized; any other 4xx
// (quota, slot, idempotency conflict, revoked credential, offboarding) comes
// back as *OnboardingRejection so the caller can show the SaaS's own detail
// message rather than inventing one.
func (c *Client) OnboardCamera(ctx context.Context, deviceID, credential string, req CameraOnboardingRequest) (CameraOnboardingResult, error) {
	var result CameraOnboardingResult
	status, header, body, err := c.do(ctx, http.MethodPost, CameraOnboardingPath, deviceID, credential, req)
	if err != nil {
		return result, err
	}

	switch status {
	case http.StatusOK, http.StatusCreated:
		var envelope cameraOnboardingEnvelope
		if jsonErr := json.Unmarshal(body, &envelope); jsonErr != nil {
			return result, fmt.Errorf("%w: decoding camera onboarding response: %v", ErrUnexpectedStatus, jsonErr)
		}
		return envelope.Onboarding, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return result, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case http.StatusTooManyRequests:
		return result, &RateLimitError{RetryAfter: parseRetryAfter(header)}
	case http.StatusConflict, http.StatusUnprocessableEntity, http.StatusNotFound:
		return result, &OnboardingRejection{StatusCode: status, Detail: errorDetail(body)}
	default:
		return result, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}

// CancelCameraOnboarding reverses exactly what OnboardCamera's operationID
// created (see the SaaS doc's "Rollback" section). Idempotent: an already
// rolled back or unknown operationID is not an error.
func (c *Client) CancelCameraOnboarding(ctx context.Context, deviceID, credential string, operationID int64) error {
	path := fmt.Sprintf("%s/%d", CameraOnboardingPath, operationID)
	status, header, body, err := c.do(ctx, http.MethodDelete, path, deviceID, credential, nil)
	if err != nil {
		return err
	}

	switch status {
	case http.StatusOK, http.StatusNotFound:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	default:
		return fmt.Errorf("%w: status %d, %s", ErrUnexpectedStatus, status, errorDetail(body))
	}
}

// errorDetail extracts FastAPI's {"detail": "..."} shape, falling back to
// the raw (bounded) body when it doesn't parse -- never panics on a
// malformed body.
func errorDetail(body []byte) string {
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Detail != "" {
		return envelope.Detail
	}
	const maxLen = 200
	if len(body) > maxLen {
		return string(body[:maxLen])
	}
	return string(body)
}
