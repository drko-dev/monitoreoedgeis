// Package credentialrotation owns the single manual/automatic rotation core.
package credentialrotation

import (
	"context"
	"errors"
	"fmt"
	"math"
	rand "math/rand/v2"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/auditjournal"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type Client interface {
	RotateKey(context.Context, string, string, transport.RotateKeyRequest) (transport.RotateResponse, error)
	Me(context.Context, string, string) (transport.MeResponse, error)
}

type Store interface {
	Load(string) (credentials.Credentials, error)
	Save(string, credentials.Credentials) error
}

type DiskStore struct{}

func (DiskStore) Load(dir string) (credentials.Credentials, error) { return credentials.Load(dir) }
func (DiskStore) Save(dir string, c credentials.Credentials) error { return credentials.Save(dir, c) }

type Audit interface {
	Append(auditjournal.Record) (auditjournal.Record, error)
}

type Options struct {
	DataDir, EdgeID string
	Client          Client
	Store           Store
	Audit           Audit
	Now             func() time.Time
	NewID           func() (string, error)
	RetryDelays     []time.Duration
	Wait            func(context.Context, time.Duration) error
	Interval        time.Duration
	JitterWindow    time.Duration
	Rand            func() float64
}

type Service struct {
	opts Options
	mu   sync.Mutex
}

var (
	ErrLocalPersistence = errors.New("rotation: local persistence failure")
	ErrPostVerification = errors.New("rotation: post-rotation verification failure")
)

func New(opts Options) (*Service, error) {
	if opts.Client == nil || opts.Store == nil || opts.DataDir == "" {
		return nil, errors.New("rotation: client, store, and data directory are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		src := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
		opts.Rand = src.Float64
	}
	if opts.NewID == nil {
		opts.NewID = identity.NewUUIDv4
	}
	if opts.RetryDelays == nil {
		opts.RetryDelays = []time.Duration{time.Second, 2 * time.Second}
	}
	if opts.Wait == nil {
		opts.Wait = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Service{opts: opts}, nil
}

// Rotate serializes all callers, durably preparing one id+new credential before
// network I/O so a retry after a restart repeats the same idempotent request.
func (s *Service) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.opts.Store.Load(s.opts.DataDir)
	if err != nil {
		s.audit(credentials.Credentials{EdgeID: s.opts.EdgeID}, "", false, ErrLocalPersistence)
		return ErrLocalPersistence
	}
	if !c.IsEnrolled() {
		return errors.New("rotation: device is not enrolled")
	}
	if c.PendingRotationID == "" {
		c.PendingRotationID, err = s.opts.NewID()
		if err != nil {
			s.audit(credentials.Credentials{EdgeID: c.EdgeID, DeviceID: c.DeviceID}, "", false, err)
			return errors.New("rotation: could not create attempt id")
		}
		c.PendingRotationCredential, err = credentials.GenerateCredential()
		if err != nil {
			s.audit(c, c.PendingRotationID, false, ErrLocalPersistence)
			return errors.New("rotation: could not generate credential")
		}
		if err = s.opts.Store.Save(s.opts.DataDir, c); err != nil {
			s.audit(c, c.PendingRotationID, false, ErrLocalPersistence)
			return fmt.Errorf("%w before request: %v", ErrLocalPersistence, err)
		}
	}
	rotationID, next := c.PendingRotationID, c.PendingRotationCredential
	if _, verifyErr := s.opts.Client.Me(ctx, c.DeviceID, next); verifyErr == nil {
		// The server may have ACKed before local adoption failed. Probe the
		// pending credential first so recovery does not depend on old-key grace.
		// For a freshly prepared attempt this normally fails, then RotateKey is
		// submitted with the still-active credential below.
		if c.Credential != next {
			c.Credential = next
			if c.CredentialVersion < math.MaxInt {
				c.CredentialVersion++
			}
		}
		return s.finish(c, rotationID)
	}
	req := transport.RotateKeyRequest{DeviceKeyHash: credentials.HashCredential(next), RotationID: rotationID}
	var resp transport.RotateResponse
	for attempt := 0; ; attempt++ {
		resp, err = s.opts.Client.RotateKey(ctx, c.DeviceID, c.Credential, req)
		if err == nil {
			break
		}
		if errors.Is(err, transport.ErrUnauthorized) || !retryable(err) || attempt >= len(s.opts.RetryDelays) {
			s.audit(c, rotationID, false, err)
			return safeError(err)
		}
		if waitErr := s.opts.Wait(ctx, s.opts.RetryDelays[attempt]); waitErr != nil {
			s.audit(c, rotationID, false, waitErr)
			return safeError(waitErr)
		}
	}
	// Adopt the server-acknowledged credential atomically before verification,
	// matching the established manual flow and avoiding indefinite reliance on
	// the bounded previous-key grace period. Pending ID+credential survive until
	// verification succeeds, so a restart can safely complete this attempt.
	c.Credential = next
	if c.CredentialVersion < math.MaxInt {
		c.CredentialVersion++
	}
	if err := s.opts.Store.Save(s.opts.DataDir, c); err != nil {
		s.audit(c, rotationID, false, ErrLocalPersistence)
		return fmt.Errorf("%w after SaaS acknowledgement; state is indeterminate", ErrLocalPersistence)
	}
	if _, err = s.opts.Client.Me(ctx, resp.DeviceID, next); err != nil {
		s.audit(c, rotationID, false, ErrPostVerification)
		return fmt.Errorf("%w; state is indeterminate", ErrPostVerification)
	}
	return s.finish(c, rotationID)
}

func (s *Service) finish(c credentials.Credentials, rotationID string) error {
	c.LastSuccessfulRotationAt = s.opts.Now().UTC()
	if s.opts.Interval > 0 {
		c.NextRotationDueAt = c.LastSuccessfulRotationAt.Add(s.opts.Interval).Add(Jitter(s.opts.JitterWindow, s.opts.Rand))
	} else {
		c.NextRotationDueAt = time.Time{}
	}
	c.PendingRotationID, c.PendingRotationCredential = "", ""
	if err := s.opts.Store.Save(s.opts.DataDir, c); err != nil {
		s.audit(c, rotationID, false, ErrLocalPersistence)
		return fmt.Errorf("%w after verification", ErrLocalPersistence)
	}
	s.audit(c, rotationID, true, nil)
	return nil
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, transport.ErrConflict) || errors.Is(err, transport.ErrUnexpectedStatus) {
		return false
	}
	if errors.Is(err, transport.ErrRateLimited) || errors.Is(err, transport.ErrRetryableStatus) || errors.Is(err, transport.ErrSaaSUnavailable) || errors.Is(err, transport.ErrTimeout) {
		return true
	}
	return true // network/transient errors; transport errors do not contain secrets.
}

func safeError(err error) error {
	switch {
	case errors.Is(err, ErrLocalPersistence):
		return ErrLocalPersistence
	case errors.Is(err, ErrPostVerification):
		return ErrPostVerification
	case errors.Is(err, transport.ErrUnauthorized):
		return fmt.Errorf("%w: no re-enrollment attempted", transport.ErrUnauthorized)
	case errors.Is(err, transport.ErrConflict):
		return fmt.Errorf("%w: conflicting idempotency request", transport.ErrConflict)
	case errors.Is(err, transport.ErrRateLimited):
		return fmt.Errorf("%w: SaaS rate limited request", transport.ErrRateLimited)
	case errors.Is(err, transport.ErrConflict):
		return fmt.Errorf("%w: conflicting idempotency request", transport.ErrConflict)
	case errors.Is(err, transport.ErrRetryableStatus), errors.Is(err, transport.ErrSaaSUnavailable), errors.Is(err, transport.ErrTimeout):
		return errors.New("rotation: transient SaaS failure")
	case errors.Is(err, transport.ErrUnexpectedStatus):
		return errors.New("rotation: SaaS rejected request")
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("rotation: operation failed")
	}
}

func (s *Service) audit(c credentials.Credentials, id string, success bool, err error) {
	if s.opts.Audit == nil {
		return
	}
	typ, result := auditjournal.EventCredentialRotationFailure, auditjournal.ResultFailure
	if success {
		typ, result = auditjournal.EventCredentialRotationSuccess, auditjournal.ResultSuccess
	}
	reason := ""
	if err != nil {
		reason = safeError(err).Error()
	}
	_, _ = s.opts.Audit.Append(auditjournal.Record{EventType: typ, Result: result, EdgeID: c.EdgeID, DeviceID: c.DeviceID, RotationID: id, SafeReason: reason})
}

// DueAt derives schedule from existing credentials metadata. Initial enrollment
// anchors the first due date when no successful rotation timestamp exists.
func DueAt(c credentials.Credentials, interval time.Duration) time.Time {
	if !c.NextRotationDueAt.IsZero() {
		return c.NextRotationDueAt
	}
	anchor := c.LastSuccessfulRotationAt
	if anchor.IsZero() {
		anchor = c.EnrolledAt
	}
	if anchor.IsZero() {
		return time.Time{}
	}
	return anchor.Add(interval)
}

// Jitter returns a bounded delay in [0, window], never advances the due time.
func Jitter(window time.Duration, randFloat func() float64) time.Duration {
	if window <= 0 {
		return 0
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	v := randFloat()
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return time.Duration(float64(window) * v)
}
