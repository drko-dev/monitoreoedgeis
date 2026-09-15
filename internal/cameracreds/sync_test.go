package cameracreds

import (
	"context"
	"errors"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeFetcher struct {
	resp transport.CameraCredentialsResponse
	err  error
}

func (f *fakeFetcher) FetchCameraCredentials(ctx context.Context, deviceID, credential string) (transport.CameraCredentialsResponse, error) {
	return f.resp, f.err
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSyncer_Success(t *testing.T) {
	store := newTestStore(t)
	fetcher := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{
			{ID: "c1", Scope: "DEVICE", CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1},
		},
	}}
	syncer, err := NewSyncer(SyncOptions{Client: fetcher, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := NewProvider(store).Resolve("dev-1", "")
	if !ok || got.Password != "pass" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestSyncer_SaaSUnreachable_KeepsCache(t *testing.T) {
	store := newTestStore(t)
	// Seed a cache entry via a first successful sync.
	ok := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{
		{ID: "c1", Scope: "DEVICE", CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1},
	}}}
	syncer, err := NewSyncer(SyncOptions{Client: ok, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	down := &fakeFetcher{err: transport.ErrSaaSUnavailable}
	syncer2, err := NewSyncer(SyncOptions{Client: down, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer2.Sync(context.Background()); !errors.Is(err, transport.ErrSaaSUnavailable) {
		t.Fatalf("expected ErrSaaSUnavailable, got %v", err)
	}

	got, present := NewProvider(store).Resolve("dev-1", "")
	if !present || got.Password != "pass" {
		t.Fatalf("cache lost after SaaS outage: %+v present=%v", got, present)
	}
}

func TestSyncer_MalformedPayload_KeepsCache(t *testing.T) {
	store := newTestStore(t)
	good := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{
		{ID: "c1", Scope: "DEVICE", CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1},
	}}}
	syncer, err := NewSyncer(SyncOptions{Client: good, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Malformed: missing username, and an invalid scope.
	bad := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{
		{ID: "c2", Scope: "BOGUS", CandidateKeys: []string{"dev-2"}, Username: "", Password: "x", Revision: 1},
	}}}
	syncer2, err := NewSyncer(SyncOptions{Client: bad, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer2.Sync(context.Background()); err == nil {
		t.Fatal("expected error for malformed payload")
	}

	got, present := NewProvider(store).Resolve("dev-1", "")
	if !present || got.Password != "pass" {
		t.Fatalf("cache corrupted by malformed payload: %+v present=%v", got, present)
	}
	if _, present := NewProvider(store).Resolve("dev-2", ""); present {
		t.Fatal("malformed entry must not have been applied")
	}
}

func TestSyncer_DecodesCandidateKeysArray(t *testing.T) {
	// Real SaaS payload shape: candidate_keys as an array (a DEVICE
	// credential still carries a 1-element array).
	store := newTestStore(t)
	fetcher := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{
			{ID: "cred-1", Scope: "DEVICE", Username: "admin", Password: "p4ss", Revision: 7,
				CandidateKeys: []string{"MAC:00:11:22:33:44:55", "SN:ABC123"}},
		},
	}}
	syncer, err := NewSyncer(SyncOptions{Client: fetcher, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider := NewProvider(store)
	for _, key := range []string{"MAC:00:11:22:33:44:55", "SN:ABC123"} {
		got, ok := provider.Resolve(key, "")
		if !ok || got.ID != "cred-1" {
			t.Fatalf("candidate_key %q did not resolve to cred-1: got=%+v ok=%v", key, got, ok)
		}
	}
}

func TestSyncer_RevokeRemovesCredential(t *testing.T) {
	store := newTestStore(t)
	present := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{
		{ID: "c1", Scope: "DEVICE", CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1},
	}}}
	syncer, err := NewSyncer(SyncOptions{Client: present, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	revoked := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{
		{ID: "c1", Scope: "DEVICE", CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1, Revoked: true},
	}}}
	syncer2, err := NewSyncer(SyncOptions{Client: revoked, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer2.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := NewProvider(store).Resolve("dev-1", ""); ok {
		t.Fatal("revoked credential should have been removed from cache")
	}
}
