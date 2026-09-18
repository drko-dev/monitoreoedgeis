package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// LocalEvent is the narrow Edge-to-SaaS event contract. Producers must not
// provide organization, camera IDs, or local filesystem paths.
type LocalEvent struct {
	EventUUID     string  `json:"event_uuid"`
	CandidateKey  string  `json:"candidate_key"`
	Class         string  `json:"class"`
	Confidence    float64 `json:"confidence"`
	BBox          any     `json:"bbox"`
	Timestamp     string  `json:"occurred_at"`
	CorrelationID string  `json:"correlation_id,omitempty"`
}

// LocalEventSender is intentionally narrower than Client. It is the only
// transport surface the durable local-event backlog needs.
type LocalEventSender interface {
	PostLocalEvent(context.Context, string, string, LocalEvent) error
	PutLocalEventEvidence(context.Context, string, string, string, string, string, []byte, string, int64) error
}

func (c *Client) PostLocalEvent(ctx context.Context, deviceID, credential string, event LocalEvent) error {
	status, header, _, err := c.do(ctx, http.MethodPost, LocalEventsPath, deviceID, credential, event)
	if err != nil {
		return err
	}
	return classifyLocalEventStatus(status, header)
}

// PutLocalEventEvidence uploads raw JPEG or supported clip bytes. checksum is
// hex SHA-256 and size is the exact byte count persisted by the backlog.
func (c *Client) PutLocalEventEvidence(ctx context.Context, deviceID, credential, eventUUID, candidateKey, kind string, body []byte, checksum string, size int64) error {
	if kind != "capture" && kind != "clip" {
		return fmt.Errorf("%w: unsupported evidence kind %q", ErrInvalidRequest, kind)
	}
	path := fmt.Sprintf("%s/%s/evidence/%s", LocalEventsPath, eventUUID, kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", map[string]string{"capture": "image/jpeg", "clip": "video/mp4"}[kind])
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("X-Candidate-Key", candidateKey)
	req.Header.Set("X-Evidence-SHA256", checksum)
	req.Header.Set("X-Evidence-Size", strconv.FormatInt(size, 10))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: PUT %s", ErrTimeout, path)
		}
		return fmt.Errorf("%w: PUT %s: %w", ErrSaaSUnavailable, path, err)
	}
	defer resp.Body.Close()
	return classifyLocalEventStatus(resp.StatusCode, resp.Header)
}

func classifyLocalEventStatus(status int, header http.Header) error {
	if status == http.StatusCreated || status == http.StatusOK {
		return nil
	}
	return classifyFrameStatusWithHeader(status, header)
}

// MarshalLocalEvent is useful to durable stores that need the exact metadata
// document without importing net/http.
func MarshalLocalEvent(event LocalEvent) ([]byte, error) { return json.Marshal(event) }
