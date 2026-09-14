package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// enrolledConfig returns a config whose data dir already holds a valid
// credential, so newHeartbeatModule builds a real module instead of skipping.
func enrolledConfig(t *testing.T, saasURL string) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.SaaSURL = saasURL
	cfg.AllowInsecureHTTP = true
	cfg.SaaSTimeout = 2 * time.Second
	cfg.HeartbeatInterval = 5 * time.Second

	err := credentials.Save(cfg.DataDir, credentials.Credentials{
		EdgeID:            cfg.EdgeID,
		DeviceID:          "device-test",
		Credential:        "secret-credential",
		CredentialVersion: 1,
		TenantID:          "tenant-test",
		SiteID:            "site-test",
		EnrolledAt:        time.Now().UTC(),
		Status:            credentials.StatusEnrolled,
	})
	if err != nil {
		t.Fatalf("setup: saving credentials: %v", err)
	}
	return cfg
}

// firstHeartbeat starts one heartbeat module against srv and returns the first
// payload it sends.
func firstHeartbeat(t *testing.T, cfg *config.Config, got <-chan transport.HeartbeatRequest) transport.HeartbeatRequest {
	t.Helper()

	a := New(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case req := <-got:
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run() did not return after context cancellation")
		}
		return req
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat arrived")
		return transport.HeartbeatRequest{}
	}
}

// A boot ID names one *process run*. Deriving it from the Edge ID — which is
// deliberately stable across restarts — would make it constant, and a SaaS
// that discards stale snapshots by (boot_id, sequence_number) would then see
// a restarted Edge replay sequence 1 under the same boot and reject it as
// stale. In K3s that leaves a recreated pod stuck OFFLINE forever, so this is
// worth pinning down.
func TestBootIDIsUniquePerRunAndNotTheEdgeID(t *testing.T) {
	got := make(chan transport.HeartbeatRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req transport.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case got <- req:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Two separate agent runs standing in for a restart. They share an Edge ID
	// but must not share a boot ID.
	first := firstHeartbeat(t, enrolledConfig(t, srv.URL), got)
	second := firstHeartbeat(t, enrolledConfig(t, srv.URL), got)

	if first.BootID == "" || second.BootID == "" {
		t.Fatalf("BootID is empty (first=%q second=%q), want a generated id", first.BootID, second.BootID)
	}
	if first.BootID == second.BootID {
		t.Errorf("BootID is identical across runs (%q); it does not identify a boot", first.BootID)
	}
	for _, r := range []transport.HeartbeatRequest{first, second} {
		if r.BootID == r.EdgeID {
			t.Errorf("BootID = EdgeID = %q; the Edge ID is stable across restarts and cannot name a boot", r.BootID)
		}
	}

	// Each run must start its own sequence, so the pair (boot_id, sequence)
	// is what disambiguates — not the sequence alone.
	if first.SequenceNumber != 1 || second.SequenceNumber != 1 {
		t.Errorf("SequenceNumber = (%d, %d), want each run to start at 1",
			first.SequenceNumber, second.SequenceNumber)
	}
}

// The heartbeat must never carry tenant or site: the SaaS derives both from
// the authenticated credential and must not take the Edge's word for them.
// The credential itself must never appear in the body either.
func TestHeartbeatBodyCarriesNoTenantSiteOrCredential(t *testing.T) {
	bodies := make(chan string, 4)
	got := make(chan transport.HeartbeatRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req transport.HeartbeatRequest
		_ = json.Unmarshal(raw, &req)
		select {
		case bodies <- string(raw):
			got <- req
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	firstHeartbeat(t, enrolledConfig(t, srv.URL), got)

	body := <-bodies
	for _, forbidden := range []string{"secret-credential", "tenant-test", "site-test", "tenant_id", "site_id"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("heartbeat body contains %q:\n%s", forbidden, body)
		}
	}
}
