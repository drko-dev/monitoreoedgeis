package cameracreds

// Hito W — W7 (bad credentials), Edge→SaaS half.
//
// A revoked or rejected Edge credential must never cost the Edge its cached
// camera credentials. The existing tests prove that with a fake Fetcher; this
// one drives the real transport client against a local SaaS that answers 401,
// and checks the durable cache file byte for byte.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// TestW7_SaaSAuthRejectionLeavesTheCredentialCacheUntouched drives the real
// transport: a 401 from the SaaS must return ErrUnauthorized, hit the endpoint
// exactly once, and leave both the in-memory cache and the encrypted file on
// disk exactly as they were.
func TestW7_SaaSAuthRejectionLeavesTheCredentialCacheUntouched(t *testing.T) {
	const cameraPassword = "camera-lan-secret"

	dataDir := t.TempDir()
	masterKey := testKey(t)
	store, err := OpenStore(dataDir, masterKey)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	// Seed a cached credential, as a previous successful sync would have.
	if _, err := store.Apply([]Credential{{
		ID:            "cred-1",
		Scope:         ScopeDevice,
		CandidateKeys: []string{"dev-1"},
		Username:      "admin",
		Password:      cameraPassword,
		Revision:      1,
	}}); err != nil {
		t.Fatalf("seed Apply: %v", err)
	}

	credentialsFile := credentialsPath(dataDir)
	before := readAll(t, credentialsFile)
	if strings.Contains(string(before), cameraPassword) {
		t.Fatal("the seeded cache stores the camera password in plaintext on disk")
	}

	var requests atomic.Int32
	var sawAuthorization atomic.Value
	saas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		sawAuthorization.Store(r.Header.Get("Authorization"))
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"credential revoked"}`))
	}))
	defer saas.Close()

	client, err := transport.New(saas.URL, true, 2*time.Second, "w7-test")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}

	syncer, err := NewSyncer(SyncOptions{
		Client:     client,
		Store:      store,
		DeviceID:   "device-w7",
		Credential: "edg_live_w7_revoked",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = syncer.Sync(ctx)
	if err == nil {
		t.Fatal("Sync succeeded against a SaaS that rejected the credential")
	}
	if !errors.Is(err, transport.ErrUnauthorized) {
		t.Errorf("Sync error = %v, want it to wrap transport.ErrUnauthorized", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the SaaS received %d requests for one sync, want 1 (no retry loop on a 401)", got)
	}
	if got, _ := sawAuthorization.Load().(string); got != "Bearer edg_live_w7_revoked" {
		t.Errorf("Authorization header = %q, want the stored Edge credential", got)
	}

	// The in-memory cache is unchanged.
	entries := store.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("cache holds %d entries after a 401, want 1", len(entries))
	}
	if entries[0].Password != cameraPassword || entries[0].Revision != 1 {
		t.Errorf("cached credential changed after a 401: %+v", entries[0])
	}
	if got := len(store.Snapshot()); got != 1 {
		t.Errorf("cache size = %d, want 1", got)
	}
	if _, ok := NewProvider(store).Resolve("dev-1", ""); !ok {
		t.Error("the cached credential no longer resolves after a 401")
	}

	// The durable file is unchanged, byte for byte.
	after := readAll(t, credentialsFile)
	if string(after) != string(before) {
		t.Errorf("camera credentials file changed after a 401:\nbefore: %s\nafter:  %s", before, after)
	}
	if strings.Contains(string(after), cameraPassword) {
		t.Error("the camera password appears in plaintext on disk")
	}

	// Reopening from disk still yields the original credential, so nothing
	// about the rejection was persisted.
	reopened, err := OpenStore(dataDir, masterKey)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedEntries := reopened.Snapshot()
	if len(reopenedEntries) != 1 || reopenedEntries[0].Password != cameraPassword {
		t.Errorf("credential after reopening the store = %+v, want the original", reopenedEntries)
	}
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
