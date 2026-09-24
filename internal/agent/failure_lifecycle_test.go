package agent

// Hito W — W5 (SaaS unreachable) and W8 (reboot / process restart) at the
// agent level.
//
// The module-level tests in internal/heartbeat prove how a single module
// classifies and retries a failure. These prove what the *agent* promises
// around it: the process keeps serving /healthz, the durable identity and
// enrollment credential on disk are never touched, no re-enrollment is ever
// attempted, and a restart re-reads that durable state while every piece of
// per-run state starts over.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

const w5Credential = "edg_live_w5_agent_credential"

// w5SaaS is a local stand-in for the SaaS that can be switched between a
// healthy endpoint, a 5xx, and a credential rejection. It records enough of
// each request to prove what the Edge did or did not send.
type w5SaaS struct {
	server *httptest.Server

	reject atomic.Bool

	mu       sync.Mutex
	enrolls  int
	paths    []string
	bodies   []string
	auths    []string
	requests int
}

func newW5SaaS(t *testing.T) *w5SaaS {
	t.Helper()
	s := &w5SaaS{}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *w5SaaS) url() string { return s.server.URL }

func (s *w5SaaS) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))

	s.mu.Lock()
	s.requests++
	s.paths = append(s.paths, r.URL.Path)
	s.bodies = append(s.bodies, string(body))
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	if r.URL.Path == transport.EnrollPath {
		s.enrolls++
	}
	s.mu.Unlock()

	if s.reject.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"credential revoked"}`))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"maintenance"}`))
}

func (s *w5SaaS) state() (requests, enrolls int, paths, bodies, auths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.enrolls,
		append([]string(nil), s.paths...),
		append([]string(nil), s.bodies...),
		append([]string(nil), s.auths...)
}

// watchHeartbeats decodes every heartbeat the fake SaaS records from this
// moment on and forwards it to out. Starting from the current request count is
// what lets a two-phase restart test read "process B's first heartbeat" rather
// than replaying process A's.
func (s *w5SaaS) watchHeartbeats(t *testing.T, out chan<- transport.HeartbeatRequest) {
	t.Helper()

	s.mu.Lock()
	seen := len(s.bodies)
	s.mu.Unlock()

	ticker := time.NewTicker(5 * time.Millisecond)
	deadline := time.After(20 * time.Second)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				return
			case <-ticker.C:
				s.mu.Lock()
				bodies := append([]string(nil), s.bodies...)
				paths := append([]string(nil), s.paths...)
				s.mu.Unlock()
				for seen < len(bodies) {
					i := seen
					seen++
					if paths[i] != transport.HeartbeatPath {
						continue
					}
					var req transport.HeartbeatRequest
					if err := json.Unmarshal([]byte(bodies[i]), &req); err != nil {
						continue
					}
					select {
					case out <- req:
					default:
					}
				}
			}
		}
	}()
}

// w5Config builds a hermetic agent config: no LAN discovery, no camera
// connectivity, an ephemeral health port, and nothing that reaches the real
// network except the local fake SaaS.
//
// It goes through the real config.Load() so every unrelated default the module
// wiring depends on (backlog bounds, buffer bounds, timeouts) is populated the
// way production populates it, then overrides only the handful of fields whose
// production validation bounds are wider than a test is willing to wait for.
func w5Config(t *testing.T, dataDir, saasURL string) *config.Config {
	t.Helper()

	// Scrub ambient dev-shell overrides first: a stray GEOCAM_* in the
	// developer's environment must not decide what this test exercises.
	for _, key := range []string{
		"GEOCAM_EDGE_ID",
		"GEOCAM_SERVICE_MODE",
		"GEOCAM_SAAS_URL",
		"GEOCAM_ALLOW_INSECURE_HTTP",
		"GEOCAM_SAAS_TIMEOUT",
		"GEOCAM_HEARTBEAT_INTERVAL",
		"GEOCAM_HEALTH_ADDR",
		"GEOCAM_PROCESSING_MODE",
		"GEOCAM_CLOUD_BUFFER_MAX_BYTES",
		"GEOCAM_CLOUD_BUFFER_MAX_FRAMES",
		"GEOCAM_VIDEO_PIPELINE_ENABLED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("GEOCAM_DATA_DIR", dataDir)
	t.Setenv("GEOCAM_LOG_LEVEL", "error")
	t.Setenv("GEOCAM_DISCOVERY_ENABLED", "false")
	t.Setenv("GEOCAM_CONNECTIVITY_ENABLED", "false")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	cfg.SaaSURL = saasURL
	cfg.AllowInsecureHTTP = true
	cfg.SaaSTimeout = 500 * time.Millisecond
	cfg.HeartbeatInterval = 50 * time.Millisecond
	cfg.HealthAddr = "127.0.0.1:0"
	cfg.EdgeID = ""
	return cfg
}

func w5Enroll(t *testing.T, dir string) credentials.Credentials {
	t.Helper()
	ident, err := identity.Load(dir, "")
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	creds := credentials.Credentials{
		EdgeID:            ident.EdgeID,
		DeviceID:          "device-w5",
		Credential:        w5Credential,
		CredentialVersion: 1,
		TenantID:          "tenant-w5",
		SiteID:            "site-w5",
		EnrolledAt:        time.Now().UTC().Truncate(time.Second),
		Status:            credentials.StatusEnrolled,
	}
	if err := credentials.Save(dir, creds); err != nil {
		t.Fatalf("credentials.Save: %v", err)
	}
	return creds
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// startAgent runs an agent until the returned stop function is called, and
// fails the test if Run does not return cleanly.
func startAgent(t *testing.T, a *Agent) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v, want nil on cancellation", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancellation; shutdown is deadlocked")
		}
	}
	t.Cleanup(stop)
	return stop
}

func healthEndpoints(t *testing.T, a *Agent) (healthz, readyz int, statusBody string) {
	t.Helper()
	srv := httptest.NewServer(health.Handler(a.Health()))
	defer srv.Close()

	healthz = getStatus(t, srv.URL+"/healthz")
	readyz = getStatus(t, srv.URL+"/readyz")

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /status: %v", err)
	}
	return healthz, readyz, string(body)
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// A SaaS that is down must not take the Edge down, must not clear identity or
// the enrollment credential, and must not trigger re-enrollment.
func TestW5_AgentSurvivesSaaSOfflineAndKeepsDurableState(t *testing.T) {
	saas := newW5SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	identityPath := filepath.Join(dataDir, "identity.json")
	credentialsPath := filepath.Join(dataDir, "credentials.json")
	identityBefore := readFileOrFail(t, identityPath)
	credentialsBefore := readFileOrFail(t, credentialsPath)

	a := New(cfg)
	stop := startAgent(t, a)

	waitForState(t, a, health.StateReady)

	// Wait until the heartbeat has genuinely failed more than once.
	w5WaitFor(t, "at least two failed heartbeats", 10*time.Second, func() bool {
		snap := a.Health().Snapshot()
		return snap.Heartbeat != nil && snap.Heartbeat.ConsecutiveFailures >= 2
	})

	// A SaaS outage is not the Edge's failure: the agent stays READY and the
	// local health surface stays answerable.
	if got := a.Health().State(); got != health.StateReady {
		t.Errorf("agent state during a SaaS outage = %q, want %q", got, health.StateReady)
	}
	healthz, readyz, statusBody := healthEndpoints(t, a)
	if healthz != http.StatusOK {
		t.Errorf("GET /healthz during a SaaS outage = %d, want 200", healthz)
	}
	if readyz != http.StatusOK {
		t.Errorf("GET /readyz during a SaaS outage = %d, want 200", readyz)
	}

	heartbeatStatus := a.Health().Snapshot().Heartbeat
	if heartbeatStatus == nil {
		t.Fatal("/status does not report the heartbeat module during an outage")
	}
	if heartbeatStatus.State != "degraded" {
		t.Errorf("heartbeat state during an outage = %q, want %q", heartbeatStatus.State, "degraded")
	}
	if heartbeatStatus.LastError != "server_error" {
		t.Errorf("heartbeat last_error = %q for a 5xx SaaS, want %q",
			heartbeatStatus.LastError, "server_error")
	}

	// The durable enrollment state is untouched, byte for byte.
	if got := readFileOrFail(t, identityPath); string(got) != string(identityBefore) {
		t.Errorf("identity.json changed during a SaaS outage:\nbefore: %s\nafter:  %s", identityBefore, got)
	}
	if got := readFileOrFail(t, credentialsPath); string(got) != string(credentialsBefore) {
		t.Errorf("credentials.json changed during a SaaS outage:\nbefore: %s\nafter:  %s", credentialsBefore, got)
	}

	requests, enrolls, paths, bodies, auths := saas.state()
	if requests == 0 {
		t.Fatal("the fake SaaS received no requests; the outage was never exercised")
	}
	if enrolls != 0 {
		t.Errorf("the Edge attempted enrollment %d times during a SaaS outage, want 0", enrolls)
	}
	for i, path := range paths {
		if path == transport.EnrollPath {
			t.Errorf("request %d hit the enrollment endpoint during a SaaS outage", i)
		}
		if auths[i] != "Bearer "+w5Credential {
			t.Errorf("request %d carried Authorization %q, want the stored credential", i, auths[i])
		}
	}

	// Neither the status document served to operators nor the heartbeat bodies
	// sent to the SaaS carry the credential or the tenant/site identifiers.
	for _, forbidden := range []string{w5Credential, "Bearer ", "Authorization"} {
		if strings.Contains(statusBody, forbidden) {
			t.Errorf("/status contains %q:\n%s", forbidden, statusBody)
		}
	}
	for i, body := range bodies {
		for _, forbidden := range []string{w5Credential, "tenant-w5", "site-w5"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("heartbeat body %d contains %q:\n%s", i, forbidden, body)
			}
		}
	}

	stop()
}

// A revoked credential must degrade the agent (it is genuinely not doing its
// job) without deleting anything or enrolling again.
func TestW5_RevokedCredentialDegradesTheAgentButDeletesNothing(t *testing.T) {
	saas := newW5SaaS(t)
	saas.reject.Store(true)

	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	identityPath := filepath.Join(dataDir, "identity.json")
	credentialsPath := filepath.Join(dataDir, "credentials.json")
	identityBefore := readFileOrFail(t, identityPath)
	credentialsBefore := readFileOrFail(t, credentialsPath)

	a := New(cfg)
	stop := startAgent(t, a)

	w5WaitFor(t, "the agent to report DEGRADED after a 401", 10*time.Second, func() bool {
		return a.Health().State() == health.StateDegraded
	})

	snap := a.Health().Snapshot()
	if snap.Heartbeat == nil || snap.Heartbeat.State != "unauthorized" {
		t.Fatalf("heartbeat state = %+v, want unauthorized", snap.Heartbeat)
	}
	if snap.Heartbeat.LastError != "unauthorized" {
		t.Errorf("heartbeat last_error = %q, want %q", snap.Heartbeat.LastError, "unauthorized")
	}

	healthz, readyz, statusBody := healthEndpoints(t, a)
	if healthz != http.StatusOK {
		t.Errorf("GET /healthz with a revoked credential = %d, want 200 (the process is healthy)", healthz)
	}
	if readyz != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz with a revoked credential = %d, want 503 (the Edge is not doing its job)", readyz)
	}
	if strings.Contains(statusBody, w5Credential) {
		t.Errorf("/status leaked the credential:\n%s", statusBody)
	}

	if got := readFileOrFail(t, identityPath); string(got) != string(identityBefore) {
		t.Errorf("identity.json changed after a 401:\nbefore: %s\nafter:  %s", identityBefore, got)
	}
	if got := readFileOrFail(t, credentialsPath); string(got) != string(credentialsBefore) {
		t.Errorf("credentials.json changed after a 401:\nbefore: %s\nafter:  %s", credentialsBefore, got)
	}

	_, enrolls, _, _, _ := saas.state()
	if enrolls != 0 {
		t.Errorf("a 401 triggered %d enrollment attempts, want 0 (recovery is an operator action)", enrolls)
	}

	// Still enrolled locally: the Edge never gave up its identity.
	loaded, err := credentials.Load(dataDir)
	if err != nil {
		t.Fatalf("credentials.Load: %v", err)
	}
	if !loaded.IsEnrolled() || loaded.Credential != w5Credential {
		t.Errorf("stored credential after a 401 = %+v, want the original enrolled credential", loaded)
	}

	stop()
}

// Process B using the same data directory as process A: the same edge_id and
// the same persisted credential, a new boot identifier, a heartbeat sequence
// that starts over, and an uptime that starts from zero.
func TestW8_RestartReusesDurableIdentityAndResetsPerRunState(t *testing.T) {
	saas := newW5SaaS(t)

	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	identityPath := filepath.Join(dataDir, "identity.json")
	credentialsPath := filepath.Join(dataDir, "credentials.json")
	identityBefore := readFileOrFail(t, identityPath)
	credentialsBefore := readFileOrFail(t, credentialsPath)

	// --- Process A -------------------------------------------------------
	heartbeatsA := make(chan transport.HeartbeatRequest, 8)
	saas.watchHeartbeats(t, heartbeatsA)

	agentA := New(cfg)
	stopA := startAgent(t, agentA)
	waitForState(t, agentA, health.StateReady)

	firstA := w5WaitHeartbeat(t, heartbeatsA, "process A")
	if firstA.SequenceNumber != 1 {
		t.Errorf("process A first sequence_number = %d, want 1", firstA.SequenceNumber)
	}
	if firstA.BootID == "" {
		t.Fatal("process A sent an empty boot_id")
	}

	// Let process A accumulate uptime so "reset" is measurable.
	time.Sleep(1200 * time.Millisecond)
	uptimeA := agentA.Health().Snapshot().UptimeSeconds
	if uptimeA < 1 {
		t.Fatalf("process A uptime_seconds = %d after ~1.2s, want >= 1", uptimeA)
	}
	edgeIDFromStatusA := agentA.Health().Snapshot().EdgeID

	stopA()

	// --- Process B, same data directory ----------------------------------
	heartbeatsB := make(chan transport.HeartbeatRequest, 8)
	saas.watchHeartbeats(t, heartbeatsB)

	agentB := New(cfg)
	stopB := startAgent(t, agentB)

	// Startup must not deadlock: READY has to be reached promptly.
	waitForState(t, agentB, health.StateReady)
	uptimeB := agentB.Health().Snapshot().UptimeSeconds

	firstB := w5WaitHeartbeat(t, heartbeatsB, "process B")
	stopB()

	// Durable identity: identical edge_id, straight from disk as well as from
	// the running agent.
	persisted, err := identity.Load(dataDir, "")
	if err != nil {
		t.Fatalf("identity.Load after restart: %v", err)
	}
	if persisted.EdgeID != enrolled.EdgeID {
		t.Errorf("edge_id after restart = %q, want the persisted %q",
			persisted.EdgeID, enrolled.EdgeID)
	}
	if firstB.EdgeID != firstA.EdgeID {
		t.Errorf("heartbeat edge_id across restart: %q -> %q, want it stable",
			firstA.EdgeID, firstB.EdgeID)
	}
	if agentB.Health().Snapshot().EdgeID != edgeIDFromStatusA {
		t.Errorf("reported edge_id across restart changed: %q -> %q",
			edgeIDFromStatusA, agentB.Health().Snapshot().EdgeID)
	}
	if got := readFileOrFail(t, identityPath); string(got) != string(identityBefore) {
		t.Errorf("identity.json was rewritten by the restart:\nbefore: %s\nafter:  %s", identityBefore, got)
	}

	// Durable credential: the same secret, still on disk.
	if got := readFileOrFail(t, credentialsPath); string(got) != string(credentialsBefore) {
		t.Errorf("credentials.json changed across restart:\nbefore: %s\nafter:  %s", credentialsBefore, got)
	}
	reloaded, err := credentials.Load(dataDir)
	if err != nil {
		t.Fatalf("credentials.Load after restart: %v", err)
	}
	if reloaded.Credential != w5Credential || !reloaded.IsEnrolled() {
		t.Errorf("credential after restart = %+v, want the same enrolled credential", reloaded)
	}

	// Per-run state: a new boot identifier, a sequence that starts over, and an
	// uptime that is not inherited.
	if firstB.BootID == "" {
		t.Fatal("process B sent an empty boot_id")
	}
	if firstB.BootID == firstA.BootID {
		t.Errorf("boot_id is identical across the restart (%q); it does not name a run", firstB.BootID)
	}
	if firstB.SequenceNumber != 1 {
		t.Errorf("process B first sequence_number = %d, want 1 (the pair (boot_id, sequence) is what disambiguates)",
			firstB.SequenceNumber)
	}
	if uptimeB >= uptimeA {
		t.Errorf("uptime_seconds after restart = %d, want it reset below process A's %d", uptimeB, uptimeA)
	}

	// Hito Z G1-B: internal/cameracreds is now wired into the agent runtime
	// for an enrolled Edge with a SaaS URL configured (see
	// newCameraCredsModule) — which this test's config is, via w5Enroll.
	// camera_master.key existing here is therefore expected product
	// behavior, not an invented side effect: LoadOrCreateMasterKey creates
	// it eagerly (never lazily on first successful sync), exactly like
	// identity.json/credentials.json are durable local state.
	//
	// camera_credentials.json is deliberately NOT asserted here: w5SaaS
	// (see newW5SaaS.handle) answers every request with 503, so the
	// camera-credentials sync never succeeds and Store.Apply — the only
	// thing that writes that file — never runs. That is Syncer.Sync's
	// correct, documented contract (a transport failure leaves the cache
	// exactly as it was), not a gap in this wiring.
	//
	// See TestW8_UnenrolledRestartNeverCreatesCameraCredentialState for the
	// complementary case, where neither file must ever appear.
	if _, err := os.Stat(filepath.Join(dataDir, "camera_master.key")); err != nil {
		t.Errorf("camera_master.key does not exist after a restart of an enrolled agent with camera credentials enabled: %v", err)
	}
}

// Hito Z G1-B: an unenrolled Edge, or one with no SaaS URL configured, must
// still not fabricate any camera-credential state — the exact behavior
// TestW8_RestartReusesDurableIdentityAndResetsPerRunState asserted for
// every Edge before camera credentials existed, now scoped to the case
// where it still applies.
func TestW8_UnenrolledRestartNeverCreatesCameraCredentialState(t *testing.T) {
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, "") // no SaaS URL configured, never enrolled

	a := New(cfg)
	stop := startAgent(t, a)
	defer stop()
	// Nothing about being unenrolled with no SaaS URL is itself a startup
	// fault (heartbeat/discovery/camera-creds are all just skipped for it):
	// the agent reaches Ready normally.
	waitForState(t, a, health.StateReady)

	for _, name := range []string{"camera_credentials.json", "camera_master.key"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			t.Errorf("%s exists for an unenrolled/offline agent, but camera credentials must not be fabricated for it", name)
		}
	}
}

// A second agent run must not be started twice inside one process: the module
// manager is a single lifecycle, and the agent's own health surface must be
// able to report the state of the one it has.
func TestW8_AgentRunReportsOneLifecycleAndStopsCleanly(t *testing.T) {
	saas := newW5SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	a := New(cfg)
	stop := startAgent(t, a)
	waitForState(t, a, health.StateReady)

	snap := a.Health().Snapshot()
	if len(snap.Modules) == 0 {
		t.Fatal("/status reports no modules after startup")
	}
	if got := snap.Modules["health-http"]; got == "" {
		t.Errorf("health-http module is not reported: %v", snap.Modules)
	}

	// Every module must appear exactly once under a single state string — the
	// map keyed by module name is the structural guarantee that a module was
	// registered (and therefore started) once.
	seen := map[string]int{}
	for name := range snap.Modules {
		seen[name]++
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("module %q appears %d times", name, count)
		}
	}

	stop()
	if got := a.Health().State(); got != health.StateStopping {
		t.Errorf("final agent state = %q, want %q", got, health.StateStopping)
	}
}

// Hito Z G1-B section 13: a corrupt camera_master.key is fatal for camera
// credentials specifically, but must never be silently regenerated (that
// would permanently orphan any already-encrypted camera_credentials.json),
// and must never take down the rest of the Edge's local surface.
func TestG1B_CorruptCameraMasterKeyNeverRegeneratedAndHealthStaysUp(t *testing.T) {
	saas := newW5SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	keyPath := filepath.Join(dataDir, "camera_master.key")
	if err := os.WriteFile(keyPath, []byte("not-a-valid-32-byte-key"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	corruptBefore := readFileOrFail(t, keyPath)

	a := New(cfg)
	stop := startAgent(t, a)
	defer stop()

	// A corrupt camera_master.key is a startup fault (fail-closed, per
	// spec section 1): the agent reports DEGRADED, never READY, until a
	// restart with a healthy key clears it. "health local debe seguir
	// accesible" means the health HTTP surface itself keeps serving — not
	// that the overall reported state is READY.
	waitForState(t, a, health.StateDegraded)

	if a.cameraCredsErr == nil {
		t.Error("cameraCredsErr = nil, want a wrapped ErrCorruptMasterKey")
	} else if !errors.Is(a.cameraCredsErr, cameracreds.ErrCorruptMasterKey) {
		t.Errorf("cameraCredsErr = %v, want it to wrap cameracreds.ErrCorruptMasterKey", a.cameraCredsErr)
	}
	if a.cameraCredsErr != nil && strings.Contains(a.cameraCredsErr.Error(), w5Credential) {
		t.Error("cameraCredsErr leaks the enrollment credential")
	}
	if a.CameraCredsProvider() != nil {
		t.Error("CameraCredsProvider() is non-nil after a corrupt master key; camera credentials must fail closed entirely")
	}

	if got := readFileOrFail(t, keyPath); string(got) != string(corruptBefore) {
		t.Errorf("camera_master.key was rewritten instead of left corrupt:\nbefore: %s\nafter:  %s", corruptBefore, got)
	}

	// Health/local surface stays reachable for diagnosis.
	snap := a.Health().Snapshot()
	if snap.Modules["health-http"] == "" {
		t.Error("health-http module is not reported; local health surface must stay up")
	}
}

// Hito Z G1-B section 13: a corrupt camera_credentials.json must never be
// silently deleted and recreated — that would discard real assignments the
// SaaS believes are still active until the next successful sync overwrites
// them, and would hide a real integrity problem.
func TestG1B_CorruptCameraCredentialsCacheNeverDeleted(t *testing.T) {
	saas := newW5SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	// A valid master key, but a cache file that is not valid JSON at all —
	// OpenStore must reject it outright rather than guess a fallback shape.
	if _, err := cameracreds.LoadOrCreateMasterKey(dataDir); err != nil {
		t.Fatalf("setup: LoadOrCreateMasterKey: %v", err)
	}
	credsPath := filepath.Join(dataDir, "camera_credentials.json")
	if err := os.WriteFile(credsPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	corruptBefore := readFileOrFail(t, credsPath)

	a := New(cfg)
	stop := startAgent(t, a)
	defer stop()

	// Same fail-closed contract as the corrupt-master-key case above.
	waitForState(t, a, health.StateDegraded)

	if a.cameraCredsErr == nil {
		t.Error("cameraCredsErr = nil, want a wrapped ErrCorrupt")
	} else if !errors.Is(a.cameraCredsErr, cameracreds.ErrCorrupt) {
		t.Errorf("cameraCredsErr = %v, want it to wrap cameracreds.ErrCorrupt", a.cameraCredsErr)
	}
	if a.CameraCredsProvider() != nil {
		t.Error("CameraCredsProvider() is non-nil after a corrupt credentials cache; camera credentials must fail closed entirely")
	}

	if got := readFileOrFail(t, credsPath); string(got) != string(corruptBefore) {
		t.Errorf("camera_credentials.json was rewritten instead of left corrupt:\nbefore: %s\nafter:  %s", corruptBefore, got)
	}

	snap := a.Health().Snapshot()
	if snap.Modules["health-http"] == "" {
		t.Error("health-http module is not reported; local health surface must stay up")
	}
}

func w5WaitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func w5WaitHeartbeat(t *testing.T, ch <-chan transport.HeartbeatRequest, who string) transport.HeartbeatRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never sent a heartbeat", who)
		return transport.HeartbeatRequest{}
	}
}
