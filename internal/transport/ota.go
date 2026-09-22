package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// OTARelease is the release descriptor returned by GET OTANextPath. It
// names the artifacts to fetch but carries no bytes of its own -- the URLs
// point at GitHub Releases, distributed outside this authenticated Edge
// channel (see internal/ota's Downloader).
type OTARelease struct {
	ReleaseID     string `json:"release_id"`
	Version       string `json:"version"`
	Architecture  string `json:"architecture"`
	ArtifactURL   string `json:"artifact_url"`
	SHA256SUMSURL string `json:"sha256sums_url"`
	SignatureURL  string `json:"signature_url"`
}

// GetNextOTARelease polls for the current eligible OTA release. A nil
// result with a nil error means no update is currently eligible.
func (c *Client) GetNextOTARelease(ctx context.Context, deviceID, credential string) (*OTARelease, error) {
	status, header, body, err := c.do(ctx, http.MethodGet, OTANextPath, deviceID, credential, nil)
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
	var release OTARelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("%w: decode ota release: %v", ErrUnexpectedStatus, err)
	}
	return &release, nil
}
