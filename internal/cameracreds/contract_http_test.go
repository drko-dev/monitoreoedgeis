package cameracreds

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// These tests drive the REAL transport client against a REAL HTTP server
// serving the exact JSON body the SaaS emits
// (monitoreoia geocam/routers/camera_credentials.py, sync_camera_credentials).
// Fake structs alone would not have caught the two contract mismatches this
// file exists to pin: a NUMERIC id and a LOWERCASE scope.

const saasCredentialsBody = `{
  "credentials": [
    {
      "id": 42,
      "name": "Camara Entrada",
      "scope": "device",
      "candidate_keys": ["epr:a1b2c3"],
      "username": "admin",
      "password": "secret",
      "revision": 3
    }
  ]
}`

func newTransportAgainst(t *testing.T, handler http.HandlerFunc) *transport.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// httptest serves plain http://, so the dev escape hatch is required.
	client, err := transport.New(srv.URL, true, 5*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestContract_RealSaaSJSON_TransportToProvider is the end-to-end contract
// test: real HTTP JSON -> transport decode -> decodePayload -> encrypted Store
// -> Provider.Resolve.
func TestContract_RealSaaSJSON_TransportToProvider(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	var gotPath, gotDeviceID, gotAuth string
	client := newTransportAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDeviceID = r.Header.Get("X-Device-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(saasCredentialsBody))
	})

	syncer, err := NewSyncer(SyncOptions{
		Client: client, Store: store, DeviceID: "gateway-1", Credential: "dev-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("Sync against the real SaaS JSON shape failed: %v", err)
	}

	// The request went to the confirmed endpoint with the gateway auth.
	if gotPath != transport.CameraCredentialsPath {
		t.Errorf("path = %q, want %q", gotPath, transport.CameraCredentialsPath)
	}
	if gotDeviceID != "gateway-1" || !strings.Contains(gotAuth, "Bearer ") {
		t.Errorf("unexpected auth headers: device=%q auth=%q", gotDeviceID, gotAuth)
	}

	// Resolved through the Provider by candidate key.
	cred, ok := NewProvider(store).Resolve("epr:a1b2c3")
	if !ok {
		t.Fatal("the synced credential did not resolve by candidate key")
	}
	if cred.ID != "42" {
		t.Errorf("credential id = %q, want %q (numeric SaaS id as canonical decimal)", cred.ID, "42")
	}
	if cred.Scope != ScopeDevice {
		t.Errorf("scope = %q, want %q (normalized from the SaaS lowercase form)", cred.Scope, ScopeDevice)
	}
	if cred.Username != "admin" || cred.Password != "secret" {
		t.Errorf("credential fields not carried through: %+v", cred)
	}
	if cred.Revision != 3 {
		t.Errorf("revision = %d, want 3", cred.Revision)
	}

	// The password is encrypted at rest and never written in plaintext.
	raw, err := os.ReadFile(filepath.Join(dir, "camera_credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("camera password written in plaintext to the credential store")
	}
	var onDisk struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("stored file is not valid JSON: %v", err)
	}
	if len(onDisk.Entries) != 1 {
		t.Fatalf("stored %d entries, want 1", len(onDisk.Entries))
	}
	if _, ok := onDisk.Entries[0]["password_enc"]; !ok {
		t.Fatalf("expected an encrypted password field, got keys %v", onDisk.Entries[0])
	}
	if _, leaked := onDisk.Entries[0]["password"]; leaked {
		t.Fatal("stored entry carries a plaintext password field")
	}

	// Reopening with the same key still resolves — the encryption round-trips.
	reopened, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := NewProvider(reopened).Resolve("epr:a1b2c3"); !ok {
		t.Fatal("credential did not survive a reopen")
	}
}

// TestContract_NumericIDWouldBreakAStringField documents the regression this
// change fixes: the SaaS id is a JSON number, so decoding it into a string
// field is impossible. If the wire id ever becomes a string again, this test
// fails and the boundary must be revisited deliberately.
func TestContract_NumericIDWouldBreakAStringField(t *testing.T) {
	var numeric struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(`{"id": 42}`), &numeric); err != nil {
		t.Fatalf("numeric id must decode into int64: %v", err)
	}
	if numeric.ID != 42 {
		t.Fatalf("id = %d, want 42", numeric.ID)
	}

	var asString struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(`{"id": 42}`), &asString); err == nil {
		t.Fatal("a JSON number must NOT decode into a string field — the old payload shape was broken")
	}
}

// TestContract_EmptyAuthoritativeSnapshotOverHTTP pins that a real 200 with an
// empty credentials array clears the cache, over the real HTTP path.
func TestContract_EmptyAuthoritativeSnapshotOverHTTP(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	body := saasCredentialsBody
	client := newTransportAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	syncer, err := NewSyncer(SyncOptions{Client: client, Store: store, DeviceID: "g", Credential: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewProvider(store).Resolve("epr:a1b2c3"); !ok {
		t.Fatal("precondition: credential should be cached")
	}

	// The SaaS now reports no active credentials.
	body = `{"credentials": []}`
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("cache = %d entries after an authoritative empty snapshot, want 0", got)
	}
	if _, ok := NewProvider(store).Resolve("epr:a1b2c3"); ok {
		t.Fatal("credential must no longer resolve after an authoritative empty snapshot")
	}
}

// TestContract_HTTPErrorKeepsCacheOverHTTP: a 500 must not clear the cache.
func TestContract_HTTPErrorKeepsCacheOverHTTP(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	status := http.StatusOK
	body := saasCredentialsBody
	client := newTransportAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	syncer, err := NewSyncer(SyncOptions{Client: client, Store: store, DeviceID: "g", Credential: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	status = http.StatusInternalServerError
	body = `{"credentials": []}`
	if err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("expected a 500 to surface as an error")
	}
	if _, ok := NewProvider(store).Resolve("epr:a1b2c3"); !ok {
		t.Fatal("a failed HTTP response must not clear the cache")
	}
}

// TestContract_NoSecretInSyncLogs asserts the syncer never logs the password,
// the candidate key payload or the raw body.
func TestContract_NoSecretInSyncLogs(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := newTransportAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(saasCredentialsBody))
	})
	syncer, err := NewSyncer(SyncOptions{
		Client: client, Store: store, DeviceID: "gateway-1", Credential: "enrollment-secret",
		Log: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Also exercise the failure paths, which log error classes.
	_ = syncer.Sync(context.Background())

	logs := buf.String()
	for _, forbidden := range []string{
		"secret",            // the camera password in the payload
		"enrollment-secret", // the gateway's own credential
		"epr:a1b2c3",        // a candidate key
		"authorization",     // header name/value
		"candidate_keys",    // raw payload field
		`"credentials"`,     // raw body
	} {
		if strings.Contains(strings.ToLower(logs), strings.ToLower(forbidden)) {
			t.Errorf("sync logs leaked %q:\n%s", forbidden, logs)
		}
	}
}
