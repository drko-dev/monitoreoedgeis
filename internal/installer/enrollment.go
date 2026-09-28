package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
)

const (
	// CrockfordBase32Alphabet defines the 32 characters used in Crockford Base32.
	CrockfordBase32Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// NormalizeEnrollmentCode normalizes the input code into standard Crockford Base32:
// uppercase, stripped of hyphens and spaces, with lookalike substitution (O->0, I/L->1).
func NormalizeEnrollmentCode(raw string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(raw))
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")

	var b strings.Builder
	for _, r := range cleaned {
		switch r {
		case 'O':
			b.WriteRune('0')
		case 'I', 'L':
			b.WriteRune('1')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidateEnrollmentCode checks if normalized code is exactly 8 valid Crockford Base32 characters.
func ValidateEnrollmentCode(normalized string) bool {
	if len(normalized) != 8 {
		return false
	}
	for _, r := range normalized {
		if !strings.ContainsRune(CrockfordBase32Alphabet, r) {
			return false
		}
	}
	return true
}

// ClaimRequest holds the parameters passed from the UI/CLI to initiate a device claim.
type ClaimRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name,omitempty"`
	SaaSURL    string `json:"saas_url,omitempty"`
}

// ClaimResult is the safe response returned to callers and Wails UI.
// NEVER contains the local credential or API key.
type ClaimResult struct {
	DeviceID       string `json:"device_id"`
	OrganizationID int    `json:"organization_id"`
	DeviceKind     string `json:"device_kind"`
	Status         string `json:"status"`
	EdgeID         string `json:"edge_id"`
}

// ClaimPayload is the wire payload sent to SaaS POST /api/v1/edge/claim.
type ClaimPayload struct {
	Code           string `json:"code"`
	DeviceName     string `json:"device_name,omitempty"`
	EdgeID         string `json:"edge_id"`
	DeviceKeyHash  string `json:"device_key_hash"`
	ClaimRequestID string `json:"claim_request_id"`
	Platform       string `json:"platform"`
	Architecture   string `json:"architecture"`
	AgentVersion   string `json:"agent_version"`
}

// ClaimResponse is the decoded response from SaaS POST /api/v1/edge/claim.
type ClaimResponse struct {
	DeviceID       string `json:"device_id"`
	DeviceKind     string `json:"device_kind"`
	OrganizationID int    `json:"organization_id"`
	Status         string `json:"status"`
}

// EnrollmentProvider abstracts the HTTP claim communication with SaaS for testability.
type EnrollmentProvider interface {
	Claim(ctx context.Context, saasURL string, payload ClaimPayload) (*ClaimResponse, error)
}

// SaaSEnrollmentProvider is the standard production implementation of EnrollmentProvider.
type SaaSEnrollmentProvider struct {
	httpClient *http.Client
}

// NewSaaSEnrollmentProvider creates a new provider with the specified HTTP client.
func NewSaaSEnrollmentProvider(client *http.Client) *SaaSEnrollmentProvider {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}
	return &SaaSEnrollmentProvider{httpClient: client}
}

// Claim sends the claim payload to the SaaS endpoint and returns the result.
func (p *SaaSEnrollmentProvider) Claim(ctx context.Context, saasURL string, payload ClaimPayload) (*ClaimResponse, error) {
	baseURL := strings.TrimRight(saasURL, "/")
	endpoint := baseURL + "/api/v1/edge/claim"

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, &SafeError{
			Code:        "INTERNAL_ERROR",
			SafeMessage: "Failed to serialize claim request.",
			Recoverable: false,
			Details:     err.Error(),
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &SafeError{
			Code:        "NETWORK_ERROR",
			SafeMessage: "Failed to create HTTP request to SaaS.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, &SafeError{
			Code:        "NETWORK_ERROR",
			SafeMessage: "Unable to connect to the GEO CAM SaaS server.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return nil, &SafeError{
			Code:        "NETWORK_ERROR",
			SafeMessage: "Failed reading response from SaaS server.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var result ClaimResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, &SafeError{
				Code:        "SERVER_ERROR",
				SafeMessage: "Invalid response received from SaaS server.",
				Recoverable: true,
				Details:     err.Error(),
			}
		}
		return &result, nil

	case http.StatusUnauthorized:
		return nil, &SafeError{
			Code:        "INVALID_CODE",
			SafeMessage: "The enrollment code is invalid, expired, or has already been used.",
			Recoverable: true,
		}

	case http.StatusConflict:
		return nil, &SafeError{
			Code:        "EDGE_ID_CONFLICT",
			SafeMessage: "This Edge device ID is already registered to another device.",
			Recoverable: false,
		}

	case http.StatusTooManyRequests:
		return nil, &SafeError{
			Code:        "RATE_LIMITED",
			SafeMessage: "Too many claim attempts. Please wait a few moments and try again.",
			Recoverable: true,
		}

	default:
		msg := "SaaS server returned error code " + strconv.Itoa(resp.StatusCode)
		return nil, &SafeError{
			Code:        "SERVER_ERROR",
			SafeMessage: msg,
			Recoverable: true,
		}
	}
}

// ClaimDevice executes the end-to-end device enrollment wizard flow.
func (s *Service) ClaimDevice(ctx context.Context, req ClaimRequest) (*ClaimResult, error) {
	normCode := NormalizeEnrollmentCode(req.Code)
	if !ValidateEnrollmentCode(normCode) {
		return nil, &SafeError{
			Code:        "INVALID_CODE",
			SafeMessage: "Enrollment code must be 8 characters in Crockford format (XXXX-XXXX).",
			Recoverable: true,
		}
	}

	saasURL := strings.TrimSpace(req.SaaSURL)
	if saasURL == "" {
		saasURL = os.Getenv("GEOCAM_SAAS_URL")
	}
	if saasURL == "" {
		saasURL = "http://127.0.0.1:8000"
	}

	// 1. Resolve or create persistent identity (stable UUID v4)
	id, err := identity.Load(s.DataDir, "")
	if err != nil {
		return nil, &SafeError{
			Code:        "IDENTITY_ERROR",
			SafeMessage: "Failed to initialize device identity.",
			Recoverable: false,
			Details:     err.Error(),
		}
	}

	// 2. Generate local device credential (zero-knowledge)
	cred, err := credentials.GenerateCredential()
	if err != nil {
		return nil, &SafeError{
			Code:        "CRYPTO_ERROR",
			SafeMessage: "Failed to generate local device credential.",
			Recoverable: false,
			Details:     err.Error(),
		}
	}
	keyHash := credentials.HashCredential(cred)

	// 3. Prepare wire payload
	payload := ClaimPayload{
		Code:           normCode,
		DeviceName:     req.DeviceName,
		EdgeID:         id.EdgeID,
		DeviceKeyHash:  keyHash,
		ClaimRequestID: uuid.New().String(),
		Platform:       runtime.GOOS,
		Architecture:   runtime.GOARCH,
		AgentVersion:   agent.Version,
	}

	provider := s.EnrollmentProvider
	if provider == nil {
		provider = NewSaaSEnrollmentProvider(nil)
	}

	// 4. Send claim to SaaS
	resp, err := provider.Claim(ctx, saasURL, payload)
	if err != nil {
		return nil, err
	}

	// 5. Persist credentials atomically
	creds := credentials.Credentials{
		EdgeID:            id.EdgeID,
		DeviceID:          resp.DeviceID,
		Credential:        cred,
		CredentialVersion: 1,
		TenantID:          strconv.Itoa(resp.OrganizationID),
		EnrolledAt:        time.Now().UTC(),
		Status:            credentials.StatusEnrolled,
	}

	if err := credentials.Save(s.DataDir, creds); err != nil {
		return nil, &SafeError{
			Code:        "PERSISTENCE_ERROR",
			SafeMessage: "Failed to persist credentials to disk.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	// 6. Reload and verify
	reloaded, err := credentials.Load(s.DataDir)
	if err != nil || !reloaded.IsEnrolled() {
		return nil, &SafeError{
			Code:        "PERSISTENCE_ERROR",
			SafeMessage: "Persisted credentials verification failed.",
			Recoverable: true,
		}
	}

	return &ClaimResult{
		DeviceID:       resp.DeviceID,
		OrganizationID: resp.OrganizationID,
		DeviceKind:     resp.DeviceKind,
		Status:         resp.Status,
		EdgeID:         id.EdgeID,
	}, nil
}
