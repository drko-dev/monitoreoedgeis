package heartbeat_test

// Hito W — W5 (internet loss / SaaS unreachable).
//
// These tests drive the real heartbeat scheduler together with the real
// transport client against a local fake SaaS. They exist because the existing
// scheduler tests all inject a fake Sender, and the existing transport tests
// each make a single request: nothing exercised the two together, which is
// where the error classification, the backoff and the "the Edge must survive a
// SaaS outage" contract actually meet.
//
// Deliberately no production behaviour is invented here. The assertions pin
// what internal/heartbeat and internal/transport already do:
//
//   - a refused/aborted connection is "unreachable", a deadline is "timeout",
//     a 5xx is "server_error", a 401/403 is "unauthorized";
//   - transient failures retry with jittered exponential backoff and the module
//     stays alive and DEGRADED;
//   - a revoked credential drops to a slow poll exactly once per episode and
//     never clears, rotates or re-enrolls anything;
//   - recovery clears the failure counters on the first success;
//   - the credential never reaches a log line, a status field or an error.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

const (
	testDeviceID   = "device-w5"
	testCredential = "edg_live_w5_credential"
	// testEchoMarker appears only in the body of the fake SaaS's 401
	// response, so it is a precise detector for "a response body leaked into
	// a log line".
	testEchoMarker = "W5-BODY-ECHO"
)

type saasMode int32

const (
	modeOK saasMode = iota
	modeUnavailable
	modeTimeout
	modeServerError
	modeUnauthorized
)

type recordedRequest struct {
	path          string
	authorization string
	deviceID      string
}

// fakeSaaS is a local, controllable SaaS endpoint. It records every request so
// the tests can prove what the Edge did (and did not) send.
type fakeSaaS struct {
	server *httptest.Server

	mode     atomic.Int32
	requests atomic.Int32

	mu       sync.Mutex
	recorded []recordedRequest
	enrolls  int
}

func newFakeSaaS(t *testing.T) *fakeSaaS {
	t.Helper()
	s := &fakeSaaS{}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *fakeSaaS) url() string { return s.server.URL }

func (s *fakeSaaS) setMode(m saasMode) { s.mode.Store(int32(m)) }

func (s *fakeSaaS) requestCount() int32 { return s.requests.Load() }

func (s *fakeSaaS) snapshot() ([]recordedRequest, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.recorded...), s.enrolls
}

func (s *fakeSaaS) handle(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	s.mu.Lock()
	s.recorded = append(s.recorded, recordedRequest{
		path:          r.URL.Path,
		authorization: r.Header.Get("Authorization"),
		deviceID:      r.Header.Get("X-Device-Id"),
	})
	if strings.Contains(r.URL.Path, "enroll") {
		s.enrolls++
	}
	s.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)

	switch saasMode(s.mode.Load()) {
	case modeUnavailable:
		// A connection that dies mid-flight, i.e. a network path that is gone
		// rather than a server that answered. The transport maps this to
		// ErrSaaSUnavailable.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)

	case modeTimeout:
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
		return

	case modeServerError:
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"maintenance"}`))

	case modeUnauthorized:
		w.WriteHeader(http.StatusUnauthorized)
		// A server that echoes request material back is exactly the case where
		// a naive implementation would put it in a log line.
		fmt.Fprintf(w, `{"detail":"%s credential %s rejected"}`, testEchoMarker, testCredential)

	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// recordingClock is the exported scheduling seam: it captures the delay the
// module asks for and fires the timer immediately, so a test can observe the
// backoff sequence without waiting for it.
type recordingClock struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (c *recordingClock) newTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch, func() bool { return true }
}

func (c *recordingClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

// syncBuffer collects log output without racing the module goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	saas      *fakeSaaS
	client    *transport.Client
	module    *heartbeat.Module
	clock     *recordingClock
	logs      *syncBuffer
	unauth    atomic.Int32
	successes atomic.Int32
}

func newHarness(t *testing.T, clientTimeout time.Duration) *harness {
	t.Helper()

	saas := newFakeSaaS(t)
	client, err := transport.New(saas.url(), true, clientTimeout, "w5-test")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}

	logs := &syncBuffer{}
	h := &harness{
		saas:   saas,
		client: client,
		clock:  &recordingClock{},
		logs:   logs,
	}

	module, err := heartbeat.New(heartbeat.Options{
		Sender:     client,
		DeviceID:   testDeviceID,
		Credential: testCredential,
		Build: func() transport.HeartbeatRequest {
			return transport.HeartbeatRequest{EdgeID: "edge-w5"}
		},
		Interval: 30 * time.Second,
		Log:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		// Rand fixed at the midpoint makes jitter a no-op, so the recorded
		// delays are the nominal ones.
		Rand:     func() float64 { return 0.5 },
		NewTimer: h.clock.newTimer,
		OnUnauthorized: func() {
			h.unauth.Add(1)
		},
		OnSuccess: func() {
			h.successes.Add(1)
		},
	})
	if err != nil {
		t.Fatalf("heartbeat.New: %v", err)
	}
	h.module = module

	if err := module.Start(context.Background()); err != nil {
		t.Fatalf("module.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := module.Stop(ctx); err != nil {
			t.Errorf("module.Stop: %v", err)
		}
	})
	return h
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func (h *harness) waitForRequests(t *testing.T, n int32) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d requests to reach the fake SaaS", n), 5*time.Second, func() bool {
		return h.saas.requestCount() >= n
	})
}

func (h *harness) waitForStatus(t *testing.T, what string, cond func(heartbeat.Status) bool) {
	t.Helper()
	waitFor(t, what, 5*time.Second, func() bool { return cond(h.module.Status()) })
}

// drainUntilStable lets the scheduler settle on a terminal status before the
// assertions read it.
func (h *harness) status() heartbeat.Status { return h.module.Status() }

// A SaaS that is unreachable and then degraded, and then healthy again: the
// module must classify each phase, keep running throughout, and clear every
// failure counter on the first success.
func TestW5_OutageIsClassifiedAndTheModuleRecovers(t *testing.T) {
	h := newHarness(t, 500*time.Millisecond)
	h.saas.setMode(modeUnavailable)

	h.waitForRequests(t, 3)
	h.waitForStatus(t, "the unreachable class", func(s heartbeat.Status) bool {
		return s.LastError == "unreachable" && s.ConsecutiveFailures >= 3
	})

	down := h.status()
	if down.State != heartbeat.StateDegraded {
		t.Errorf("state during an outage = %q, want %q", down.State, heartbeat.StateDegraded)
	}
	if down.ConsecutiveFailures < 3 {
		t.Errorf("consecutive_failures = %d after 3 failed sends, want >= 3", down.ConsecutiveFailures)
	}
	if !down.LastSuccessAt.IsZero() {
		t.Errorf("last_success_at = %v during an outage with no prior success, want zero", down.LastSuccessAt)
	}

	// A 5xx is a different class from an unreachable network, and must not be
	// mistaken for success.
	h.saas.setMode(modeServerError)
	h.waitForStatus(t, "the server_error class", func(s heartbeat.Status) bool {
		return s.LastError == "server_error"
	})
	if got := h.status().State; got != heartbeat.StateDegraded {
		t.Errorf("state on a 5xx = %q, want %q", got, heartbeat.StateDegraded)
	}

	// Recovery.
	h.saas.setMode(modeOK)
	h.waitForStatus(t, "recovery", func(s heartbeat.Status) bool {
		return s.State == heartbeat.StateRunning
	})

	up := h.status()
	if up.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d after recovery, want 0", up.ConsecutiveFailures)
	}
	if up.LastError != "" {
		t.Errorf("last_error = %q after recovery, want it cleared", up.LastError)
	}
	if up.LastSuccessAt.IsZero() {
		t.Error("last_success_at is zero after a successful heartbeat")
	}
	if h.successes.Load() == 0 {
		t.Error("OnSuccess was never called after recovery")
	}

	// The credential is reused verbatim on every attempt: nothing regenerated
	// or cleared it, and the Edge never tried to enroll again.
	recorded, enrolls := h.saas.snapshot()
	if enrolls != 0 {
		t.Errorf("the fake SaaS received %d enrollment attempts during an outage, want 0", enrolls)
	}
	if len(recorded) == 0 {
		t.Fatal("no requests recorded")
	}
	for i, req := range recorded {
		if want := "Bearer " + testCredential; req.authorization != want {
			t.Errorf("request %d carried Authorization %q, want %q", i, req.authorization, want)
		}
		if req.deviceID != testDeviceID {
			t.Errorf("request %d carried X-Device-Id %q, want %q", i, req.deviceID, testDeviceID)
		}
	}
}

// A deadline is its own class, distinct from an unreachable network.
func TestW5_TimeoutIsClassifiedAsTimeoutAndRecovers(t *testing.T) {
	// The client deadline is far shorter than the handler's delay, so every
	// request times out for real.
	h := newHarness(t, 40*time.Millisecond)
	h.saas.setMode(modeTimeout)

	h.waitForRequests(t, 2)
	h.waitForStatus(t, "the timeout class", func(s heartbeat.Status) bool {
		return s.LastError == "timeout"
	})

	if got := h.status().State; got != heartbeat.StateDegraded {
		t.Errorf("state on a timeout = %q, want %q", got, heartbeat.StateDegraded)
	}

	h.saas.setMode(modeOK)
	h.waitForStatus(t, "recovery after timeouts", func(s heartbeat.Status) bool {
		return s.State == heartbeat.StateRunning && s.LastError == ""
	})
}

// Transient failures back off exponentially (jittered), never in a tight loop.
func TestW5_TransientFailuresBackOffInsteadOfHammering(t *testing.T) {
	h := newHarness(t, 500*time.Millisecond)
	h.saas.setMode(modeUnavailable)

	h.waitForRequests(t, 5)
	h.waitForStatus(t, "five recorded failures", func(s heartbeat.Status) bool {
		return s.ConsecutiveFailures >= 5
	})

	delays := h.clock.recorded()
	// delays[0] is the randomised initial delay; every later entry is the
	// post-failure backoff.
	backoff := delays[1:]
	if len(backoff) < 4 {
		t.Fatalf("recorded %d backoff delays, want at least 4: %v", len(backoff), delays)
	}

	// Nominal sequence is 1s, 2s, 4s, 8s ... capped at MaxBackoff, with +/-10%
	// jitter (a no-op at Rand()==0.5).
	for i := 1; i < len(backoff) && i < 4; i++ {
		if backoff[i] <= backoff[i-1] {
			t.Errorf("backoff delay %d (%s) did not grow past %s: %v",
				i, backoff[i], backoff[i-1], backoff)
		}
	}
	for i, d := range backoff {
		if d > heartbeat.MaxBackoff {
			t.Errorf("backoff delay %d = %s, above MaxBackoff %s", i, d, heartbeat.MaxBackoff)
		}
		if d < heartbeat.BaseBackoff/2 {
			t.Errorf("backoff delay %d = %s, below the minimum the contract allows", i, d)
		}
	}
}

// A revoked credential is an administrative fact, not a blip: the module drops
// to a slow poll, reports it once, and never clears, rotates or re-enrolls
// anything.
func TestW5_RevokedCredentialIsASlowPollThatReEnrollsNothing(t *testing.T) {
	h := newHarness(t, 500*time.Millisecond)
	h.saas.setMode(modeUnauthorized)

	h.waitForRequests(t, 3)
	h.waitForStatus(t, "the unauthorized state", func(s heartbeat.Status) bool {
		return s.State == heartbeat.StateUnauthorized
	})

	unauthorized := h.status()
	if unauthorized.LastError != "unauthorized" {
		t.Errorf("last_error = %q on a 401, want %q", unauthorized.LastError, "unauthorized")
	}
	if got := h.unauth.Load(); got != 1 {
		t.Errorf("OnUnauthorized fired %d times for one contiguous 401 episode, want 1", got)
	}
	if h.successes.Load() != 0 {
		t.Errorf("OnSuccess fired %d times while every heartbeat was rejected, want 0", h.successes.Load())
	}

	// Every delay after the initial one is the slow poll, not the exponential
	// sequence.
	delays := h.clock.recorded()
	for i, d := range delays {
		if i == 0 {
			continue
		}
		if d < heartbeat.AuthFailureInterval/2 {
			t.Errorf("retry delay %d = %s after a 401, want the slow poll (~%s), not an aggressive retry",
				i, d, heartbeat.AuthFailureInterval)
		}
	}

	recorded, enrolls := h.saas.snapshot()
	if enrolls != 0 {
		t.Errorf("a rejected credential triggered %d enrollment attempts, want 0", enrolls)
	}
	for i, req := range recorded {
		if want := "Bearer " + testCredential; req.authorization != want {
			t.Errorf("request %d sent Authorization %q after a 401, want the stored credential %q unchanged",
				i, req.authorization, want)
		}
	}

	// Recovery re-arms the notification without re-enrolling.
	h.saas.setMode(modeOK)
	h.waitForStatus(t, "recovery from revocation", func(s heartbeat.Status) bool {
		return s.State == heartbeat.StateRunning
	})
	h.saas.setMode(modeUnauthorized)
	h.waitForStatus(t, "the second 401 episode", func(s heartbeat.Status) bool {
		return s.State == heartbeat.StateUnauthorized && h.unauth.Load() == 2
	})
}

// Nothing credential-shaped may reach a log line or the status surface, in any
// failure mode — including when the server echoes the credential back in the
// response body.
func TestW5_NoSecretsLeakThroughLogsOrStatusDuringAnOutage(t *testing.T) {
	h := newHarness(t, 40*time.Millisecond)

	for _, mode := range []saasMode{modeUnavailable, modeTimeout, modeServerError, modeUnauthorized} {
		h.saas.setMode(mode)
		before := h.saas.requestCount()
		h.waitForRequests(t, before+2)
	}

	// Give the last log lines a moment to be written by the module goroutine.
	waitFor(t, "the unauthorized state to be reported", 5*time.Second, func() bool {
		return h.status().State == heartbeat.StateUnauthorized
	})

	logs := h.logs.String()
	if logs == "" {
		t.Fatal("no log output captured; the test cannot judge leakage")
	}
	for _, forbidden := range []string{
		testCredential,
		"Bearer " + testCredential,
		testEchoMarker,
		"Authorization:",
	} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("log output contains %q:\n%s", forbidden, logs)
		}
	}

	encoded, err := json.Marshal(h.status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	for _, forbidden := range []string{testCredential, testEchoMarker, "Authorization"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("status JSON contains %q: %s", forbidden, encoded)
		}
	}

	// The transport keeps the same promise at the error level.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = h.client.Me(ctx, testDeviceID, testCredential)
	if err == nil {
		t.Fatal("Me unexpectedly succeeded against a 401 endpoint")
	}
	if strings.Contains(err.Error(), testCredential) || strings.Contains(err.Error(), testEchoMarker) {
		t.Errorf("transport error leaked request/response material: %v", err)
	}
}
