package cameracreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

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

	// OnSuccess, if set, is called at the end of every Sync that returns
	// nil — after fetch, validation, and Store.Apply have already
	// completed, never under Store's lock. It fires even when the applied
	// snapshot did not change anything (the reconciler it drives, Hito Z
	// G1-B, is idempotent), and it never fires on a transport failure,
	// unauthorized response, malformed payload, or persist failure — those
	// paths return early and keep the last-good cache untouched.
	OnSuccess func()
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

	stats, err := s.opts.Store.Apply(creds)
	if err != nil {
		s.opts.Log.Error("cameracreds: failed to persist synced credentials",
			slog.String("reason", "persist_failed"))
		return err
	}
	if stats.Changed() {
		// Counts only — never an id, candidate key, username or password.
		s.opts.Log.Info("cameracreds: credential cache updated",
			slog.Int("added", stats.Added),
			slog.Int("updated", stats.Updated),
			slog.Int("removed", stats.Removed),
			slog.Int("active", len(creds)))
	}
	if s.opts.OnSuccess != nil {
		s.opts.OnSuccess()
	}
	return nil
}

// decodePayload validates every entry in resp and converts it from the SaaS
// wire shape to this package's canonical representation. Any single invalid
// entry rejects the whole payload — never a partial apply.
//
// The conversion is deliberately explicit, because two wire details differ
// from the internal representation and getting either wrong breaks every
// sync:
//
//   - the SaaS id is a NUMBER (BIGSERIAL), rendered here as its canonical
//     decimal string;
//   - the SaaS scope is LOWERCASE ("device" | "group"), normalized here to
//     ScopeDevice/ScopeGroup. Any other value is rejected.
//
// Revocation needs no special case: the SaaS expresses it by omission, so an
// entry that is absent from an authoritative snapshot is removed by
// Store.Apply. There is no revoked flag on the wire.
func decodePayload(resp transport.CameraCredentialsResponse) ([]Credential, error) {
	out := make([]Credential, 0, len(resp.Credentials))
	for _, p := range resp.Credentials {
		scope, err := parseScope(p.Scope)
		if err != nil {
			return nil, fmt.Errorf("cameracreds: credential id %d: %w", p.ID, err)
		}
		c := Credential{
			ID:            strconv.FormatInt(p.ID, 10),
			Scope:         scope,
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
