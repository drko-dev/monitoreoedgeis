package credentialrotation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/auditjournal"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeClient struct {
	mu         sync.Mutex
	requests   []transport.RotateKeyRequest
	rotateErrs []error
	active     int
	maxActive  int
	gate       chan struct{}
	verifyErr  error
	activeHash string
}

func (f *fakeClient) RotateKey(ctx context.Context, _, _ string, req transport.RotateKeyRequest) (transport.RotateResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	n := len(f.requests)
	var err error
	if n <= len(f.rotateErrs) {
		err = f.rotateErrs[n-1]
	}
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
			return transport.RotateResponse{}, ctx.Err()
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if err != nil {
		return transport.RotateResponse{}, err
	}
	f.mu.Lock()
	f.activeHash = req.DeviceKeyHash
	f.mu.Unlock()
	return transport.RotateResponse{DeviceID: "device"}, nil
}
func (f *fakeClient) Me(_ context.Context, _ string, credential string) (transport.MeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return transport.MeResponse{}, f.verifyErr
	}
	if f.activeHash == "" || credentials.HashCredential(credential) != f.activeHash {
		return transport.MeResponse{}, transport.ErrUnauthorized
	}
	return transport.MeResponse{}, nil
}

type auditCapture struct {
	mu      sync.Mutex
	records []auditjournal.Record
}

type failSaveStore struct {
	saves  int
	failAt int
	base   DiskStore
}

func (s *failSaveStore) Load(d string) (credentials.Credentials, error) { return s.base.Load(d) }
func (s *failSaveStore) Save(d string, c credentials.Credentials) error {
	s.saves++
	if s.saves == s.failAt {
		return errors.New("disk unavailable")
	}
	return s.base.Save(d, c)
}

func (a *auditCapture) Append(r auditjournal.Record) (auditjournal.Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, r)
	return r, nil
}

func TestRotationRetriesReuseIDAndSuccessCreatesNewAttempt(t *testing.T) {
	dir := t.TempDir()
	original := credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old-secret", CredentialVersion: 2, EnrolledAt: time.Now().Add(-time.Hour), Status: credentials.StatusEnrolled}
	if err := credentials.Save(dir, original); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{rotateErrs: []error{transport.ErrRetryableStatus}}
	audit := &auditCapture{}
	ids := []string{"rotation-one", "rotation-two"}
	nextID := func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	svc, err := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, Audit: audit, NewID: nextID, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }, Interval: 24 * time.Hour, Rand: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	if client.requests[0] != client.requests[1] {
		t.Fatalf("retry changed request: %#v != %#v", client.requests[0], client.requests[1])
	}
	if client.requests[2].RotationID != "rotation-two" || client.requests[2].RotationID == client.requests[0].RotationID {
		t.Fatalf("next logical rotation id=%q", client.requests[2].RotationID)
	}
	if client.requests[0].DeviceKeyHash == "old-secret" || client.requests[0].DeviceKeyHash == "" {
		t.Fatal("rotation did not send a credential hash")
	}
	got, err := credentials.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Credential == "old-secret" || got.CredentialVersion != 4 || got.LastSuccessfulRotationAt.IsZero() || got.NextRotationDueAt.IsZero() {
		t.Fatalf("credential state not advanced: %#v", got)
	}
	if got.PendingRotationCredential != "" || got.PendingRotationID != "" {
		t.Fatal("pending attempt was not cleared")
	}
	if len(audit.records) != 2 || audit.records[0].EventType != auditjournal.EventCredentialRotationSuccess {
		t.Fatalf("audit=%#v", audit.records)
	}
}

func TestRotationRetryAfterRestartKeepsPendingIDAndCredential(t *testing.T) {
	dir := t.TempDir()
	if err := credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled}); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{rotateErrs: []error{transport.ErrUnauthorized}}
	newID := func() (string, error) { return "stable-id", nil }
	svc, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, NewID: newID, RetryDelays: []time.Duration{0}})
	err := svc.Rotate(context.Background())
	if !errors.Is(err, transport.ErrUnauthorized) {
		t.Fatalf("error=%v", err)
	}
	pending, err := credentials.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PendingRotationID != "stable-id" || pending.PendingRotationCredential == "" || pending.Credential != "old" {
		t.Fatalf("pending state=%#v", pending)
	}
	client.rotateErrs = nil
	svc2, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, NewID: func() (string, error) { t.Fatal("retry generated another id"); return "", nil }})
	if err := svc2.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || client.requests[0] != client.requests[1] {
		t.Fatalf("retry request changed: %#v", client.requests)
	}
}

func TestRateLimitUsesBoundedConfiguredRetries(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	client := &fakeClient{rotateErrs: []error{transport.ErrRateLimited, transport.ErrRateLimited, transport.ErrRateLimited}}
	service, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, NewID: func() (string, error) { return "limited-id", nil }, RetryDelays: []time.Duration{0, 0}})
	err := service.Rotate(context.Background())
	if !errors.Is(err, transport.ErrRateLimited) {
		t.Fatalf("error=%v", err)
	}
	if len(client.requests) != 3 || client.requests[0] != client.requests[1] || client.requests[1] != client.requests[2] {
		t.Fatalf("rate-limit retries=%#v", client.requests)
	}
}

func TestManualAndAutomaticCallersSerializeOnOneService(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	gate := make(chan struct{})
	client := &fakeClient{gate: gate}
	var ids int
	svc, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, NewID: func() (string, error) { ids++; return string(rune('a' + ids)), nil }, RetryDelays: []time.Duration{0}})
	done := make(chan error, 2)
	go func() { done <- svc.Rotate(context.Background()) }()
	for {
		client.mu.Lock()
		n := len(client.requests)
		client.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	go func() { done <- svc.Rotate(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.maxActive != 1 {
		t.Fatalf("concurrent rotations=%d", client.maxActive)
	}
}

func TestRotationErrorsAndAuditNeverContainCredential(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old-secret", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	client := &fakeClient{rotateErrs: []error{transport.ErrUnauthorized}}
	audit := &auditCapture{}
	svc, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, Audit: audit, NewID: func() (string, error) { return "safe-id", nil }})
	err := svc.Rotate(context.Background())
	if err == nil || containsSecret(err.Error(), "old-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	if len(audit.records) != 1 || containsSecret(audit.records[0].SafeReason, "old-secret") {
		t.Fatalf("unsafe audit: %#v", audit.records)
	}
}

func TestFailureBackoffIsExponentialAndCapped(t *testing.T) {
	module := NewModule(ModuleOptions{RetryBase: 2 * time.Second, RetryMax: 5 * time.Second, Rand: func() float64 { return 1 }})
	if got := module.backoff(1); got != 3*time.Second {
		t.Fatalf("first backoff=%s", got)
	}
	if got := module.backoff(2); got != 5*time.Second {
		t.Fatalf("second backoff=%s", got)
	}
	if got := module.backoff(8); got != 5*time.Second {
		t.Fatalf("capped backoff=%s", got)
	}
}

func TestPostVerificationFailurePreservesPendingAttemptAndOldCredential(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old-secret", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	client := &fakeClient{verifyErr: errors.New("response echoed old-secret Authorization: Bearer enrollment-token")}
	audit := &auditCapture{}
	service, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, Audit: audit, NewID: func() (string, error) { return "stable", nil }})
	err := service.Rotate(context.Background())
	if err == nil || containsSecret(err.Error(), "old-secret") || containsSecret(err.Error(), "enrollment-token") {
		t.Fatalf("unsafe postverify error=%v", err)
	}
	stored, loadErr := credentials.Load(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if stored.Credential == "old-secret" || stored.Credential != stored.PendingRotationCredential || stored.PendingRotationID != "stable" {
		t.Fatalf("fail-safe state=%#v", stored)
	}
	if len(audit.records) != 1 || audit.records[0].EventType != auditjournal.EventCredentialRotationFailure {
		t.Fatalf("failure audit=%#v", audit.records)
	}
	client.verifyErr = nil
	restarted, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, Audit: audit, NewID: func() (string, error) { t.Fatal("recovery generated a new ID"); return "", nil }})
	if err := restarted.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("recovery resubmitted rotation instead of verifying adopted key: %d requests", len(client.requests))
	}
	stored, err = credentials.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PendingRotationID != "" || stored.LastSuccessfulRotationAt.IsZero() {
		t.Fatalf("recovery did not finalize state: %#v", stored)
	}
}

func TestLocalPersistenceFailureAfterSaaSAckRetainsPendingIdempotentAttempt(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	client := &fakeClient{}
	store := &failSaveStore{failAt: 2}
	service, _ := New(Options{DataDir: dir, Client: client, Store: store, NewID: func() (string, error) { return "persist-fail-id", nil }})
	err := service.Rotate(context.Background())
	if !errors.Is(err, ErrLocalPersistence) {
		t.Fatalf("error=%v", err)
	}
	state, loadErr := credentials.Load(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Credential != "old" || state.PendingRotationID != "persist-fail-id" || state.PendingRotationCredential == "" {
		t.Fatalf("fail-safe persisted state=%#v", state)
	}
	if len(client.requests) != 1 {
		t.Fatalf("request count=%d", len(client.requests))
	}
}

func TestRestartPendingCredentialProbesBeforeRetryingWithExpiredOldCredential(t *testing.T) {
	dir := t.TempDir()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "old", EnrolledAt: time.Now(), Status: credentials.StatusEnrolled})
	client := &fakeClient{}
	store := &failSaveStore{failAt: 2}
	service, _ := New(Options{DataDir: dir, Client: client, Store: store, NewID: func() (string, error) { return "recover-id", nil }})
	if err := service.Rotate(context.Background()); !errors.Is(err, ErrLocalPersistence) {
		t.Fatalf("initial error=%v", err)
	}
	state, err := credentials.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.activeHash = credentials.HashCredential(state.PendingRotationCredential) // SaaS accepted pending credential; old grace expired.
	client.mu.Unlock()
	client.rotateErrs = []error{transport.ErrUnauthorized}
	restarted, _ := New(Options{DataDir: dir, Client: client, Store: DiskStore{}, NewID: func() (string, error) { t.Fatal("generated new id"); return "", nil }})
	if err := restarted.Rotate(context.Background()); err != nil {
		t.Fatalf("recovery error=%v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("recovery retried RotateKey despite pending credential probe: %d requests", len(client.requests))
	}
	final, err := credentials.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingRotationID != "" || final.Credential != state.PendingRotationCredential {
		t.Fatalf("recovery did not adopt pending credential: %#v", final)
	}
}

func containsSecret(s, secret string) bool {
	return len(secret) > 0 && len(s) >= len(secret) && (s == secret || func() bool {
		for i := 0; i+len(secret) <= len(s); i++ {
			if s[i:i+len(secret)] == secret {
				return true
			}
		}
		return false
	}())
}
