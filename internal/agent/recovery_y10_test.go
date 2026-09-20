package agent

// Hito Y — Y10 (health recovery) at the agent level.
//
// W5 already proved that a 401 marks the agent DEGRADED and that nothing on disk
// is destroyed. What was never proved — because it was never implemented — is
// the other half: once the cause disappears, the agent must say so again. The
// aggregate state used to be written imperatively and never recomputed, so a
// re-enabled device left the Edge DEGRADED and /readyz at 503 until the process
// was restarted. That also made the appliance's own post-update readiness gate
// (wait-ready.sh polls /readyz, update.sh rolls back on timeout) fail a release
// that was actually fine.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// y10SaaS is a switchable stand-in for the SaaS. The W5 fake can only reject or
// return a 503, so it can prove the Edge degrades but never that it recovers;
// this one has a healthy mode, which is what a recovery assertion needs.
type y10SaaS struct {
	server *httptest.Server

	// reject makes every request a 401 (credential revoked/device disabled).
	reject atomic.Bool
	// outage makes every request a 503 (SaaS unreachable/degraded).
	outage atomic.Bool

	mu       sync.Mutex
	enrolls  int
	requests int
}

func newY10SaaS(t *testing.T) *y10SaaS {
	t.Helper()
	s := &y10SaaS{}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *y10SaaS) url() string { return s.server.URL }

func (s *y10SaaS) handle(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<16))

	s.mu.Lock()
	s.requests++
	if r.URL.Path == "/api/v1/edge/enroll" {
		s.enrolls++
	}
	s.mu.Unlock()

	switch {
	case s.reject.Load():
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"credential revoked"}`))
	case s.outage.Load():
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"maintenance"}`))
	default:
		// A successful heartbeat is exactly 200 with a JSON body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}
}

func (s *y10SaaS) state() (requests, enrolls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.enrolls
}

// y10Config is w5Config with the post-401 poll compressed, so a recovery cycle is
// observable in milliseconds instead of the production five minutes. The
// production default is untouched: the config field is zero there, which means
// "use the heartbeat module's own default".
func y10Config(t *testing.T, dataDir, saasURL string) *config.Config {
	t.Helper()
	cfg := w5Config(t, dataDir, saasURL)
	cfg.HeartbeatAuthFailureInterval = 50 * time.Millisecond
	return cfg
}

// readyzOfReporter renders the real /readyz decision for a reporter, so a gate
// unit test asserts the same thing an operator would see.
func readyzOfReporter(t *testing.T, r *health.Reporter) int {
	t.Helper()
	srv := httptest.NewServer(health.Handler(r))
	defer srv.Close()
	return getStatus(t, srv.URL+"/readyz")
}

func testReporter() *health.Reporter {
	return health.New("y10-test", nil, identity.Identity{}, platform.Info{})
}

// TestY10_CredentialRevocationRecoversWithoutRestart is the core Y10 property
// for the one runtime cause that can genuinely clear itself.
func TestY10_CredentialRevocationRecoversWithoutRestart(t *testing.T) {
	saas := newY10SaaS(t)
	saas.reject.Store(true)

	dataDir := t.TempDir()
	cfg := y10Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	identityPath := filepath.Join(dataDir, "identity.json")
	credentialsPath := filepath.Join(dataDir, "credentials.json")
	identityBefore := readFileOrFail(t, identityPath)
	credentialsBefore := readFileOrFail(t, credentialsPath)

	a := New(cfg)
	stop := startAgent(t, a)
	defer stop()

	// Phase 1: the SaaS rejects the credential.
	w5WaitFor(t, "the agent to report DEGRADED after a 401", 10*time.Second, func() bool {
		return a.Health().State() == health.StateDegraded
	})
	if _, readyz, _ := healthEndpoints(t, a); readyz != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz while revoked = %d, want 503", readyz)
	}

	// Phase 2: the operator re-enables the device. No restart, same process.
	saas.reject.Store(false)

	w5WaitFor(t, "the agent to return to READY after the credential is accepted again",
		15*time.Second, func() bool {
			return a.Health().State() == health.StateReady
		})

	healthz, readyz, statusBody := healthEndpoints(t, a)
	if healthz != http.StatusOK {
		t.Errorf("GET /healthz after recovery = %d, want 200", healthz)
	}
	if readyz != http.StatusOK {
		t.Errorf("GET /readyz after recovery = %d, want 200 (the Edge is doing its job again)", readyz)
	}
	if !strings.Contains(statusBody, `"status":"READY"`) {
		t.Errorf("/status does not report READY after recovery:\n%s", statusBody)
	}
	if strings.Contains(statusBody, w5Credential) {
		t.Errorf("/status leaked the credential after recovery:\n%s", statusBody)
	}
	if snap := a.Health().Snapshot(); snap.Heartbeat == nil || snap.Heartbeat.State != "running" {
		t.Errorf("heartbeat state after recovery = %+v, want running", snap.Heartbeat)
	}

	// Recovery must not have re-enrolled or rewritten the durable state.
	if _, enrolls := saas.state(); enrolls != 0 {
		t.Errorf("recovery triggered %d enrollment attempts, want 0", enrolls)
	}
	if got := readFileOrFail(t, identityPath); string(got) != string(identityBefore) {
		t.Error("identity.json changed across a revocation/recovery cycle")
	}
	if got := readFileOrFail(t, credentialsPath); string(got) != string(credentialsBefore) {
		t.Error("credentials.json changed across a revocation/recovery cycle")
	}
}

// TestY10_RevocationRecoveryRepeats drives the cycle repeatedly. One recovery
// could be an accident of ordering; ten in a row returning to READY every time
// is evidence the transition is not one-shot. Two further properties are
// asserted here: the goroutine count stays flat, which is how "recovery does not
// duplicate supervisors or workers, and does not restart-loop" shows up as
// observable evidence; and the process uptime keeps increasing, which proves the
// agent was never restarted to recover.
func TestY10_RevocationRecoveryRepeats(t *testing.T) {
	saas := newY10SaaS(t)
	dataDir := t.TempDir()
	cfg := y10Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	a := New(cfg)
	stop := startAgent(t, a)
	defer stop()

	w5WaitFor(t, "the agent to reach READY", 10*time.Second, func() bool {
		return a.Health().State() == health.StateReady
	})

	// Let the module and its supervisors settle before sampling the baseline.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()
	uptimeBefore := a.Health().Uptime()

	const cycles = 10
	for i := 1; i <= cycles; i++ {
		saas.reject.Store(true)
		w5WaitFor(t, "the agent to report DEGRADED", 15*time.Second, func() bool {
			return a.Health().State() == health.StateDegraded
		})
		saas.reject.Store(false)
		w5WaitFor(t, "the agent to return to READY", 15*time.Second, func() bool {
			return a.Health().State() == health.StateReady
		})
	}

	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	if after := runtime.NumGoroutine(); after > baseline+5 {
		t.Errorf("goroutines grew from %d to %d across %d failure/recovery cycles: recovery is duplicating workers or looping",
			baseline, after, cycles)
	}

	// The same process served all ten cycles: uptime grew, it did not reset.
	if got := a.Health().Uptime(); got <= uptimeBefore {
		t.Errorf("uptime went from %s to %s across %d cycles: the process was restarted instead of recovering", uptimeBefore, got, cycles)
	}

	if _, enrolls := saas.state(); enrolls != 0 {
		t.Errorf("%d enrollment attempts across %d cycles, want 0", enrolls, cycles)
	}
	if _, readyz, _ := healthEndpoints(t, a); readyz != http.StatusOK {
		t.Errorf("GET /readyz after %d recovered cycles = %d, want 200", cycles, readyz)
	}
}

// TestY10_StartupFaultIsNotClearedByAHeartbeatRecovery is the other half of "not
// incorrectly latched": clearing the credential cause must not clear a cause
// that only a restart fixes. A corrupt identity file degrades the agent at
// startup, and a perfectly healthy SaaS heartbeat afterwards must not paper
// over it.
func TestY10_StartupFaultIsNotClearedByAHeartbeatRecovery(t *testing.T) {
	saas := newY10SaaS(t)
	dataDir := t.TempDir()
	cfg := y10Config(t, dataDir, saas.url())

	// Enroll, then corrupt the identity file so identity.Load fails.
	w5Enroll(t, dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, "identity.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Load(dataDir, ""); err == nil {
		t.Fatal("the corrupted identity file was accepted; the test premise is wrong")
	}

	a := New(cfg)
	if a.identityErr == nil {
		t.Fatal("the agent did not record the identity error")
	}
	stop := startAgent(t, a)
	defer stop()

	w5WaitFor(t, "the agent to report DEGRADED for the startup fault", 10*time.Second, func() bool {
		return a.Health().State() == health.StateDegraded
	})

	// The SaaS is healthy, so the heartbeat module runs and succeeds. A
	// successful heartbeat (and any recovery hook it fires) must not lift a
	// startup degradation.
	w5WaitFor(t, "the heartbeat module to report running", 15*time.Second, func() bool {
		snap := a.Health().Snapshot()
		return snap.Heartbeat != nil && snap.Heartbeat.State == "running"
	})
	time.Sleep(300 * time.Millisecond) // several successful heartbeats

	if got := a.Health().State(); got != health.StateDegraded {
		t.Errorf("agent state = %s after a healthy heartbeat, want DEGRADED: a startup fault must not be masked", got)
	}
	if _, readyz, _ := healthEndpoints(t, a); readyz != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz with a corrupt identity file = %d, want 503", readyz)
	}
}

// TestAgentHealthGate_Contract covers the gate's own rules directly, including
// the lifecycle guard that keeps a late recovery from resurrecting READY during
// shutdown.
func TestAgentHealthGate_Contract(t *testing.T) {
	t.Run("credential cause is clearable and republished", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.activate()
		if got := r.State(); got != health.StateReady {
			t.Fatalf("activated state = %s, want READY", got)
		}
		g.MarkCredentialRevoked()
		if got := r.State(); got != health.StateDegraded {
			t.Fatalf("state = %s after revocation, want DEGRADED", got)
		}
		g.ClearCredentialRevoked()
		if got := r.State(); got != health.StateReady {
			t.Fatalf("state = %s after recovery, want READY", got)
		}
	})

	t.Run("startup cause is sticky", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.markStartupDegraded()
		g.activate()
		if got := r.State(); got != health.StateDegraded {
			t.Fatalf("state = %s, want DEGRADED", got)
		}
		g.ClearCredentialRevoked()
		if got := r.State(); got != health.StateDegraded {
			t.Errorf("state = %s after clearing an unrelated cause, want DEGRADED still", got)
		}
	})

	t.Run("both causes must clear before READY", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.markStartupDegraded()
		g.activate()
		g.MarkCredentialRevoked()
		g.ClearCredentialRevoked()
		if got := r.State(); got != health.StateDegraded {
			t.Errorf("state = %s with the startup cause still set, want DEGRADED", got)
		}
	})

	t.Run("shutdown is never overwritten", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.activate()
		g.MarkCredentialRevoked()
		r.Set(health.StateStopping)
		g.ClearCredentialRevoked()
		if got := r.State(); got != health.StateStopping {
			t.Errorf("state = %s after a recovery during shutdown, want STOPPING", got)
		}
		g.MarkCredentialRevoked()
		if got := r.State(); got != health.StateStopping {
			t.Errorf("state = %s after a revocation during shutdown, want STOPPING", got)
		}
	})

	t.Run("hooks before activate do not publish", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.MarkCredentialRevoked()
		if got := r.State(); got != health.StateStarting {
			t.Errorf("state = %s before activate, want STARTING untouched", got)
		}
		if got := readyzOfReporter(t, r); got == http.StatusOK {
			t.Error("/readyz must not be 200 before the gate is activated")
		}
	})

	t.Run("repeated revocations are idempotent", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.activate()
		for i := 0; i < 10; i++ {
			g.MarkCredentialRevoked()
			if got := r.State(); got != health.StateDegraded {
				t.Fatalf("cycle %d: state = %s, want DEGRADED", i, got)
			}
			if got := readyzOfReporter(t, r); got != http.StatusServiceUnavailable {
				t.Fatalf("cycle %d: /readyz = %d, want 503", i, got)
			}
			g.ClearCredentialRevoked()
			if got := r.State(); got != health.StateReady {
				t.Fatalf("cycle %d: state = %s, want READY", i, got)
			}
			if got := readyzOfReporter(t, r); got != http.StatusOK {
				t.Fatalf("cycle %d: /readyz = %d, want 200", i, got)
			}
		}
	})

	t.Run("clearing with nothing revoked is a no-op", func(t *testing.T) {
		r := testReporter()
		g := newAgentHealthGate(r)
		g.markStartupDegraded()
		g.activate()
		if got := r.State(); got != health.StateDegraded {
			t.Fatalf("state = %s, want DEGRADED", got)
		}
		// A run of successful heartbeats fires this repeatedly; it must not
		// promote the agent over a startup fault.
		for i := 0; i < 5; i++ {
			g.ClearCredentialRevoked()
		}
		if got := r.State(); got != health.StateDegraded {
			t.Errorf("state = %s after no-op clears, want DEGRADED", got)
		}
	})
}
