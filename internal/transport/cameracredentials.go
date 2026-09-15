package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CameraCredentialsPath is the device-facing endpoint that returns this
// gateway's currently active camera credentials (DEVICE- and GROUP-scoped).
//
// ASSUMED path — the SaaS team is building this endpoint in parallel and the
// exact route was not confirmed against SaaS source at the time this was
// written (unlike the other paths in this file). Adjust this one constant
// if the real endpoint differs; nothing else in internal/cameracreds needs
// to change.
const CameraCredentialsPath = "/api/v1/gateway/camera-credentials"

// CameraCredentialPayload is one camera credential as sent by the SaaS.
// Password arrives in plaintext over HTTPS (bearer-authenticated) and must
// never be logged; internal/cameracreds encrypts it before it ever touches
// disk.
type CameraCredentialPayload struct {
	ID    string `json:"id"`
	Scope string `json:"scope"` // "DEVICE" or "GROUP"
	// CandidateKeys are the stable_identity/group-id strings this credential
	// applies to: exactly one entry for a DEVICE credential, N for a GROUP
	// credential assigned to N devices.
	CandidateKeys []string `json:"candidate_keys"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	Revision      int      `json:"revision"`
	// Revoked, when true, means this entry must be removed from the local
	// cache even if the SaaS still lists it (e.g. mid-revocation window).
	Revoked bool `json:"revoked,omitempty"`
}

// CameraCredentialsResponse is the full, authoritative set of camera
// credentials currently active for this gateway.
type CameraCredentialsResponse struct {
	Credentials []CameraCredentialPayload `json:"credentials"`
}

// FetchCameraCredentials retrieves this gateway's camera credentials. A
// 401/403 maps to ErrUnauthorized, the same signal used elsewhere in this
// client for a rejected/revoked enrollment credential.
func (c *Client) FetchCameraCredentials(ctx context.Context, deviceID, credential string) (CameraCredentialsResponse, error) {
	var resp CameraCredentialsResponse
	status, header, body, err := c.do(ctx, http.MethodGet, CameraCredentialsPath, deviceID, credential, nil)
	if err != nil {
		return resp, err
	}

	switch status {
	case http.StatusOK:
		if jsonErr := json.Unmarshal(body, &resp); jsonErr != nil {
			return resp, fmt.Errorf("%w: decoding camera credentials response: %v", ErrUnexpectedStatus, jsonErr)
		}
		return resp, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return resp, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case http.StatusTooManyRequests:
		return resp, &RateLimitError{RetryAfter: parseRetryAfter(header)}
	default:
		return resp, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}
