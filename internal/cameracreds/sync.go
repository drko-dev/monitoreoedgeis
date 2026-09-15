package cameracreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// Fetcher retrieves camera credentials from the SaaS. Satisfied by
// *transport.Client; replaced by a fake in tests.
type Fetcher interface {
	FetchCameraCredentials(ctx context.Context, deviceID, credential string) (transport.CameraCredentialsResponse, error)
}

// SyncOptions configures a Syncer.
type SyncOptions struct {
	Client     Fetcher
	Store      *Store
	DeviceID   string
	Credential string
	Log        *slog.Logger
}

// Syncer performs one fetch-decode-validate-apply cycle against the SaaS.
// It never mutates Store on a failed fetch or an invalid payload: the last
// good cache is always left exactly as it was.
type Syncer struct {
	opts SyncOptions
}

// NewSyncer builds a Syncer.
func NewSyncer(opts SyncOptions) (*Syncer, error) {
	if opts.Client == nil {
		return nil, errors.New("cameracreds: sync client is required")
	}
	if opts.Store == nil {
		return nil, errors.New("cameracreds: sync store is required")
	}
	if opts.DeviceID == "" || opts.Credential == "" {
		return nil, errors.New("cameracreds: device id and credential are required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Syncer{opts: opts}, nil
}

// Sync fetches the current camera-credentials snapshot, validates it, and
// applies it to the Store. The request/response body is never logged, only
// sanitized error classes and counts.
//
//   - A transport failure (SaaS unreachable, timeout, unauthorized, ...)
//     leaves the cache untouched and returns the error.
//   - A malformed payload (bad scope, missing fields) leaves the cache
//     untouched and returns an error — it never partially applies.
//   - A well-formed payload is merged via Store.Apply (revision/idempotency
//     and revoke semantics live there).
func (s *Syncer) Sync(ctx context.Context) error {
	resp, err := s.opts.Client.FetchCameraCredentials(ctx, s.opts.DeviceID, s.opts.Credential)
	if err != nil {
		s.opts.Log.Warn("cameracreds: sync fetch failed, keeping cached credentials",
			slog.String("error_class", classifyFetchError(err)))
		return err
	}

	creds, err := decodePayload(resp)
	if err != nil {
		s.opts.Log.Error("cameracreds: sync payload invalid, keeping cached credentials",
			slog.String("reason", "malformed_payload"))
		return err
	}

	changed, err := s.opts.Store.Apply(creds)
	if err != nil {
		s.opts.Log.Error("cameracreds: failed to persist synced credentials",
			slog.String("reason", "persist_failed"))
		return err
	}
	if changed {
		s.opts.Log.Info("cameracreds: credential cache updated", slog.Int("count", len(creds)))
	}
	return nil
}

// decodePayload validates every entry in resp and drops revoked ones. Any
// single invalid entry rejects the whole payload — never a partial apply.
func decodePayload(resp transport.CameraCredentialsResponse) ([]Credential, error) {
	out := make([]Credential, 0, len(resp.Credentials))
	for _, p := range resp.Credentials {
		if p.Revoked {
			continue
		}
		c := Credential{
			ID:            p.ID,
			Scope:         Scope(p.Scope),
			CandidateKeys: p.CandidateKeys,
			Username:      p.Username,
			Password:      p.Password,
			Revision:      p.Revision,
		}
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("cameracreds: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// classifyFetchError maps a Fetcher error to a stable, loggable class —
// never the raw error, which can carry response bodies or URLs.
func classifyFetchError(err error) string {
	switch {
	case errors.Is(err, transport.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, transport.ErrTimeout):
		return "timeout"
	case errors.Is(err, transport.ErrSaaSUnavailable):
		return "unreachable"
	default:
		var rl *transport.RateLimitError
		if errors.As(err, &rl) {
			return "rate_limited"
		}
		return "unexpected"
	}
}
