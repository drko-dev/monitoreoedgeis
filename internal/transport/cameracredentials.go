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
// Confirmed against the SaaS source (monitoreoia):
// geocam/routers/camera_credentials.py, gateway_router.get("/camera-credentials").
const CameraCredentialsPath = "/api/v1/gateway/camera-credentials"

// CameraCredentialPayload is one camera credential as sent by the SaaS.
//
// The shape below mirrors the SaaS payload exactly
// (geocam/routers/camera_credentials.py, sync_camera_credentials):
//
//	{"id": 42, "name": "...", "scope": "device", "username": "...",
//	 "password": "...", "revision": 2, "candidate_keys": ["..."]}
//
// Two details matter and are easy to get wrong:
//
//   - ID is a NUMBER. camera_credentials.id is a BIGSERIAL primary key, so a
//     string field here fails to decode and would break every sync.
//   - Scope is LOWERCASE ("device" | "group"), matching the DB's
//     scope VARCHAR(16) values.
//
// Password arrives in plaintext over HTTPS (bearer-authenticated) and must
// never be logged; internal/cameracreds encrypts it before it ever touches
// disk.
type CameraCredentialPayload struct {
	// ID is the SaaS numeric credential id (BIGSERIAL).
	ID int64 `json:"id"`
	// Scope is "device" or "group" (lowercase). internal/cameracreds
	// normalizes it to its own Scope constants at this boundary; any other
	// value makes the whole payload malformed.
	Scope string `json:"scope"`
	// CandidateKeys are the stable candidate identities (Hito E
	// gateway_discovery_candidates.candidate_key) this credential applies to
	// — never an IP address. Exactly one entry for a DEVICE credential, N for
	// a GROUP credential assigned to N devices. The SaaS resolves DEVICE over
	// GROUP precedence before sending, so a candidate appears at most once.
	CandidateKeys []string `json:"candidate_keys"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	Revision      int      `json:"revision"`
}

// CameraCredentialsResponse is the full, authoritative snapshot of camera
// credentials currently active for this gateway.
//
// It is AUTHORITATIVE, not incremental: an entry's absence means the
// credential is revoked or no longer assigned to this gateway. A successful
// 200 with an empty list is therefore valid and means "this gateway has no
// active camera credentials". The SaaS expresses revocation by omission — it
// never sends a revoked entry — so there is no per-entry revoked flag here.
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
