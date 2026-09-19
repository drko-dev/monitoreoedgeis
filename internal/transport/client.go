package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors, wrapped with %w so callers can use errors.Is. None of
// these ever include the enrollment token or the credential — only HTTP
// status codes and (for ErrInvalidRequest) the raw validation error body,
// which never echoes back secrets we didn't send in the first place.
var (
	// ErrTokenInvalid covers every 401 from the enroll endpoint: invalid,
	// expired, already-used or mismatched token are all indistinguishable
	// by design (anti-enumeration) — never inferred from response text.
	ErrTokenInvalid    = errors.New("transport: enrollment token invalid")
	ErrAlreadyEnrolled = errors.New("transport: edge_id already enrolled")
	ErrInvalidRequest  = errors.New("transport: request rejected as invalid by SaaS")
	ErrUnauthorized    = errors.New("transport: unauthorized (credential rejected or revoked)")
	ErrSaaSUnavailable = errors.New("transport: SaaS unreachable")
	ErrTimeout         = errors.New("transport: request timed out")
	ErrInsecureURL     = errors.New("transport: insecure SaaS URL rejected")
	// ErrRetryableStatus is a response the SaaS itself is telling us to
	// retry: 408 (client timed out), 429 (rate limited), or any 5xx (the
	// SaaS's own failure, not our request's). Distinct from
	// ErrUnexpectedStatus, which stays non-retryable — see PostFrame.
	ErrRetryableStatus  = errors.New("transport: SaaS reported a retryable failure")
	ErrUnexpectedStatus = errors.New("transport: unexpected response from SaaS")

	// ErrRateLimited is returned for HTTP 429. Callers should honour the
	// RetryAfter carried by RateLimitError rather than applying their own backoff
	// when the server stated one.
	ErrRateLimited = errors.New("transport: rate limited by SaaS")
)

// RateLimitError carries the server-stated cooldown from a 429 response.
// RetryAfter is zero when the response omitted or malformed the header.
// It implements Is to match both ErrRateLimited and ErrRetryableStatus.
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

func (e *RateLimitError) Is(target error) bool {
	return target == ErrRateLimited || target == ErrRetryableStatus
}

// parseRetryAfter reads a Retry-After header in its delta-seconds form, the
// only form the SaaS emits. An absent, malformed or negative value yields 0,
// which tells the caller to fall back to its own backoff.
func parseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
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

// DefaultTimeout is used when the caller does not set a positive timeout.
const DefaultTimeout = 10 * time.Second

// Client talks to the SaaS gateway-enrollment API over stdlib net/http.
type Client struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
	traffic    *TrafficMeter
}

// SetTrafficMeter installs an optional application-payload meter. Configure it
// before using the client concurrently. The meter itself is thread-safe.
func (c *Client) SetTrafficMeter(m *TrafficMeter) {
	c.traffic = m
}

// New builds a Client. baseURL must use https:// unless allowInsecureHTTP is
// true, in which case http:// is also accepted — this is the ONLY thing
// allowInsecureHTTP does. It never weakens TLS verification for an https://
// URL (no InsecureSkipVerify anywhere in this package). version is embedded
// in the User-Agent header.
func New(baseURL string, allowInsecureHTTP bool, timeout time.Duration, version string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("transport: SaaS URL is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("transport: invalid SaaS URL %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP {
			return nil, fmt.Errorf("%w: %q (set GEOCAM_ALLOW_INSECURE_HTTP=true to allow http:// in development)", ErrInsecureURL, baseURL)
		}
	default:
		return nil, fmt.Errorf("transport: unsupported SaaS URL scheme %q, want https (or http with GEOCAM_ALLOW_INSECURE_HTTP=true)", u.Scheme)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
		userAgent:  fmt.Sprintf("geocam-edge/%s", version),
	}, nil
}

// EnrollRequest is the body sent to EnrollPath. Pydantic-validated with
// extra="forbid" server-side: field names must match exactly, and
// DeviceKeyHash is the SHA-256 hex digest of a credential generated and kept
// entirely on this device — the plaintext credential is never sent.
type EnrollRequest struct {
	EnrollmentToken   string `json:"enrollment_token"`
	GatewayInstanceID string `json:"gateway_instance_id"` // required, 8-128 chars; we send edge_id
	DeviceKeyHash     string `json:"device_key_hash"`     // 64 hex chars, sha256(credential)
	AgentVersion      string `json:"agent_version,omitempty"`
	Platform          string `json:"platform,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
	EdgeID            string `json:"edge_id,omitempty"`
}

// EnrollResponse is the body returned by a successful enrollment. The SaaS
// deliberately does not expose organization_id/site_id/enrolled_at here —
// fetch those via an authenticated GET MePath once the credential is live.
type EnrollResponse struct {
	DeviceID   string `json:"device_id"`
	DeviceKind string `json:"device_kind"`
}

// MeResponse is the body returned by GET MePath.
type MeResponse struct {
	DeviceID   string `json:"device_id"`
	EdgeID     string `json:"edge_id"`
	DeviceKind string `json:"device_kind"`
	Status     string `json:"status"`
	Name       string `json:"name"`
	// organization_id/site_id are JSON *numbers* on the wire (the SaaS
	// derives them from the authenticated device row, where they are ints).
	// json.Number keeps them as text without forcing a numeric type here,
	// and decodes a null site_id to the zero value "" rather than erroring.
	OrganizationID   json.Number `json:"organization_id"`
	OrganizationName string      `json:"organization_name"`
	SiteID           json.Number `json:"site_id"`
	SiteName         string      `json:"site_name"`
}

// RotateKeyRequest is the body sent to RotateKeyPath, authenticated with the
// CURRENT (about-to-be-replaced) credential. DeviceKeyHash is sha256 of the
// NEW credential, generated locally and never sent in plaintext. RotationID
// is a client-generated UUID v4 idempotency key: retries of the same
// rotation attempt must reuse it (and the same DeviceKeyHash).
type RotateKeyRequest struct {
	DeviceKeyHash string `json:"device_key_hash"`
	RotationID    string `json:"rotation_id"`
}

// RotateResponse is the body returned by a successful key rotation. It
// carries no secrets — the new credential was generated by the caller, not
// the server.
type RotateResponse struct {
	DeviceID  string `json:"device_id"`
	EdgeID    string `json:"edge_id"`
	RotatedAt string `json:"rotated_at"`
}

// errorBody is the best-effort shape of a SaaS JSON error response.
// FastAPI/Pydantic uses "detail" — a string for most errors, but an array of
// validation-error objects for 422s. Only the string case is decoded here;
// for 422 the raw body is surfaced instead of a parsed field.
type errorBody struct {
	Detail string `json:"detail"`
}

// Enroll claims req.EnrollmentToken. req.DeviceKeyHash must be the SHA-256
// hex digest of a credential generated entirely on this device — the
// plaintext credential itself is never part of this request.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var resp EnrollResponse
	status, _, body, err := c.do(ctx, http.MethodPost, EnrollPath, "", "", req)
	if err != nil {
		return resp, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return resp, mapEnrollError(status, body)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return resp, fmt.Errorf("%w: decoding enroll response: %v", ErrUnexpectedStatus, err)
	}
	return resp, nil
}

// Me returns the authenticated edge's own metadata. deviceID and credential
// are both required: the SaaS's authenticate_edge_device() treats X-Device-Id
// as mandatory even when the credential arrives via Authorization: Bearer. A
// 401/403 response maps to ErrUnauthorized, the caller's signal to treat the
// stored credential as revoked.
func (c *Client) Me(ctx context.Context, deviceID, credential string) (MeResponse, error) {
	var resp MeResponse
	status, _, body, err := c.do(ctx, http.MethodGet, MePath, deviceID, credential, nil)
	if err != nil {
		return resp, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return resp, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	}
	if status != http.StatusOK {
		return resp, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return resp, fmt.Errorf("%w: decoding /edge/me response: %v", ErrUnexpectedStatus, err)
	}
	return resp, nil
}

// RotateKey submits a self-service credential rotation, authenticated with
// the CURRENT credential (deviceID + credential). req carries only the hash
// of the new credential and an idempotency key — the new credential itself
// is generated and held by the caller, never by this method.
func (c *Client) RotateKey(ctx context.Context, deviceID, credential string, req RotateKeyRequest) (RotateResponse, error) {
	var resp RotateResponse
	status, _, body, err := c.do(ctx, http.MethodPost, RotateKeyPath, deviceID, credential, req)
	if err != nil {
		return resp, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return resp, fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	}
	if status != http.StatusOK {
		return resp, fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return resp, fmt.Errorf("%w: decoding rotate-key response: %v", ErrUnexpectedStatus, err)
	}
	return resp, nil
}

// FrameMetadata holds optional transport headers for hybrid candidate frames
// (Milestone J7). When omitted or empty, standard cloud sampling headers
// are sent with zero protocol differences.
type FrameMetadata struct {
	ProcessingMode  string
	CandidateReason string
	CandidateScore  float64
	CorrelationID   string
}

// PostFrame uploads one sampled video frame to FramesPath (Milestone I).
// Unlike do(), the body is the raw JPEG (Content-Type: image/jpeg), not
// JSON — frame metadata travels as headers instead, since there is no JSON
// envelope to put it in. capturedAt is the pipeline's best-effort decode
// timestamp (processing.Frame.Timestamp), not a true camera capture time.
func (c *Client) PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error {
	return c.PostFrameWithMetadata(ctx, deviceID, credential, candidateKey, seq, capturedAt, jpeg, FrameMetadata{})
}

// PostFrameWithMetadata extends PostFrame with optional hybrid candidate headers (Milestone J7).
func (c *Client) PostFrameWithMetadata(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte, meta FrameMetadata) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+FramesPath, bytes.NewReader(jpeg))
	if err != nil {
		return fmt.Errorf("transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("X-Candidate-Key", candidateKey)
	req.Header.Set("X-Frame-Seq", strconv.FormatUint(seq, 10))
	req.Header.Set("X-Frame-Timestamp", capturedAt.UTC().Format(time.RFC3339Nano))

	if meta.ProcessingMode != "" {
		req.Header.Set("X-Processing-Mode", meta.ProcessingMode)
	}
	if meta.CandidateReason != "" {
		req.Header.Set("X-Candidate-Reason", meta.CandidateReason)
	}
	if meta.CandidateScore > 0 {
		req.Header.Set("X-Candidate-Score", strconv.FormatFloat(meta.CandidateScore, 'f', 4, 64))
	}
	if meta.CorrelationID != "" {
		req.Header.Set("X-Correlation-Id", meta.CorrelationID)
	}

	c.traffic.Record(TrafficFrames, int64(len(jpeg)), 0, 1)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: POST %s", ErrTimeout, FramesPath)
		}
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return fmt.Errorf("%w: POST %s", ErrTimeout, FramesPath)
		}
		// %w (not %v) on err: preserves context.Canceled in the chain when
		// the caller's own context was cancelled mid-request (e.g. Milestone
		// I6's drain loop shutting down), so errors.Is(result,
		// context.Canceled) still works for the caller — while
		// errors.Is(result, ErrSaaSUnavailable) keeps working too, since Go
		// supports multiple %w verbs in one Errorf.
		return fmt.Errorf("%w: POST %s: %w", ErrSaaSUnavailable, FramesPath, err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	c.traffic.Record(TrafficFrames, 0, n, 0)

	return classifyFrameStatusWithHeader(resp.StatusCode, resp.Header)
}

// classifyFrameStatus maps a PostFrame HTTP status to a sentinel error by
// status code alone — never by inferring from the response body — so
// cloudsink's I6 offline buffer can tell a transient failure (worth
// spooling for retry) from a permanent one (never worth retrying, buffered
// or not).
func classifyFrameStatus(status int) error {
	return classifyFrameStatusWithHeader(status, nil)
}

// classifyFrameStatusWithHeader maps an HTTP status and optional headers
// (notably Retry-After) to sentinel errors.
//
//   - 200/202: success, nil.
//   - 401/403: ErrUnauthorized — the credential itself is rejected; no
//     retry, buffered or not, will succeed until an operator fixes it.
//   - 429: &RateLimitError{RetryAfter: ...} (matches ErrRateLimited and
//     ErrRetryableStatus via Is).
//   - 408/5xx: ErrRetryableStatus — the SaaS is explicitly telling us
//     (or failing in a way that implies) this exact request may succeed
//     later.
//   - Any other 4xx (400/404/409/413/422/...): ErrInvalidRequest — the
//     request itself is permanently wrong; retrying it, buffered or not,
//     would just repeat the same rejection forever.
//   - Anything else (e.g. an unexpected 1xx/3xx): ErrUnexpectedStatus,
//     treated as non-retryable since this package cannot say what it means.
func classifyFrameStatusWithHeader(status int, header http.Header) error {
	switch {
	case status == http.StatusAccepted || status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, status)
	case status == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(header)}
	case status == http.StatusRequestTimeout || status >= 500:
		return fmt.Errorf("%w (status %d)", ErrRetryableStatus, status)
	case status >= 400:
		return fmt.Errorf("%w (status %d)", ErrInvalidRequest, status)
	default:
		return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}

// do issues one HTTP request and returns the raw status, response headers and
// body for the caller to interpret. deviceID (X-Device-Id) and credential
// (Authorization: Bearer) are both sent whenever non-empty — the SaaS
// requires deviceID as a separate header even though Bearer alone carries the
// credential. Neither value is ever logged or included in any returned error.
func (c *Client) do(ctx context.Context, method, path, deviceID, credential string, payload any) (int, http.Header, []byte, error) {
	var bodyReader io.Reader
	var requestBytes int64
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("transport: encode request: %w", err)
		}
		requestBytes = int64(len(data))
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if deviceID != "" {
		req.Header.Set("X-Device-Id", deviceID)
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}

	category := classifyTrafficPath(path)
	c.traffic.Record(category, requestBytes, 0, 1)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, nil, fmt.Errorf("%w: %s %s", ErrTimeout, method, path)
		}
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return 0, nil, nil, fmt.Errorf("%w: %s %s", ErrTimeout, method, path)
		}
		// %w on err (see PostFrame's identical fix): preserves
		// context.Canceled in the chain for errors.Is, alongside
		// ErrSaaSUnavailable.
		return 0, nil, nil, fmt.Errorf("%w: %s %s: %w", ErrSaaSUnavailable, method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("transport: read response body: %w", err)
	}
	c.traffic.Record(category, 0, int64(len(data)), 0)
	return resp.StatusCode, resp.Header, data, nil
}

// mapEnrollError classifies an enroll failure by HTTP status alone — the
// SaaS deliberately makes every 401 cause indistinguishable
// (invalid/expired/used/mismatched token), so no attempt is made to infer a
// more specific reason from response text.
func mapEnrollError(status int, body []byte) error {
	switch status {
	case http.StatusConflict:
		return fmt.Errorf("%w (status %d): %s", ErrAlreadyEnrolled, status, parseErrorBody(body).Detail)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (status %d): %s", ErrInvalidRequest, status, string(body))
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (status %d)", ErrTokenInvalid, status)
	default:
		return fmt.Errorf("%w: status %d", ErrUnexpectedStatus, status)
	}
}

func parseErrorBody(body []byte) errorBody {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // best-effort; invalid/empty body yields zero value
	return eb
}
