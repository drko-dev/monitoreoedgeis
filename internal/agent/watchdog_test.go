package agent

// Hito Y — Y9 (watchdog) at the agent level.
//
// The unit file declares WatchdogSec=60, so the real systemd behaviour is
// NOT_VALIDATED here: no test in this repository runs on a host where PID 1 is
// systemd. What *is* validated is everything the unit depends on and that this
// process controls: the sd_notify datagrams really are emitted, on the wire, to
// a real unix socket; a ping is withheld exactly when the liveness check fails;
// and the check fails for a wedged process rather than for a merely idle one.
// See docs/resilience/RESILIENCE_MATRIX.md.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/health"
)

// sdListener is a real unixgram socket standing in for systemd's NOTIFY_SOCKET.
type sdListener struct {
	conn *net.UnixConn
	path string
}

func newSDListener(t *testing.T) *sdListener {
	t.Helper()
	// Short path: sun_path is capped at 104 bytes on darwin and t.TempDir() is
	// already long there.
	path := filepath.Join(os.TempDir(), "geocam-wd-"+time.Now().Format("150405.000000000"))
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	l := &sdListener{conn: conn, path: path}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = os.Remove(path)
	})
	return l
}

// recv returns the next datagram, or "" if none arrives within d.
func (l *sdListener) recv(t *testing.T, d time.Duration) string {
	t.Helper()
	if err := l.conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _, err := l.conn.ReadFromUnix(buf)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return ""
		}
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

// scriptedProbe fails while failing is set.
type scriptedProbe struct {
	failing atomic.Bool
	calls   atomic.Int64
}

func (p *scriptedProbe) Probe(context.Context) error {
	p.calls.Add(1)
	if p.failing.Load() {
		return errors.New("simulated wedge")
	}
	return nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestWatchdogLoopPingsOnlyWhileLivenessHolds is the core Y9 property: a ping is
// sent while the liveness check passes, withheld the moment it stops passing,
// and resumed when it passes again. Withholding is what lets systemd's timer
// expire and restart a process that is running but wedged — the case
// Restart=on-failure cannot see.
func TestWatchdogLoopPingsOnlyWhileLivenessHolds(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)

	probe := &scriptedProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A 60ms watchdog interval gives a 20ms ping period, so the test is fast
	// while exercising the same thirds arithmetic production uses.
	done := make(chan struct{})
	go func() {
		runWatchdogLoop(ctx, 60*time.Millisecond, probe, testLogger())
		close(done)
	}()

	// Phase 1: healthy — pings must arrive.
	if got := l.recv(t, 2*time.Second); got != "WATCHDOG=1" {
		t.Fatalf("no watchdog ping while the probe was healthy (got %q)", got)
	}

	// Phase 2: wedged — pings must stop.
	probe.failing.Store(true)
	// Drain anything already queued before the probe flipped.
	for l.recv(t, 10*time.Millisecond) != "" {
	}
	if got := l.recv(t, 200*time.Millisecond); got != "" {
		t.Errorf("a ping (%q) was sent while the liveness probe was failing; systemd's timer would never expire", got)
	}

	// Phase 3: recovered — pings resume and are logged as a recovery.
	probe.failing.Store(false)
	if got := l.recv(t, 2*time.Second); got != "WATCHDOG=1" {
		t.Fatalf("no watchdog ping after the probe recovered (got %q)", got)
	}

	// Phase 4: shutdown — the loop must exit rather than leak.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatchdogLoop did not return after its context was cancelled")
	}
	if probe.calls.Load() == 0 {
		t.Error("the probe was never called")
	}
}

// TestWatchdogLoopStopsWithoutPingingWhenTheProbeNeverPasses covers a process
// that is wedged from the moment the watchdog arms.
func TestWatchdogLoopStopsWithoutPingingWhenTheProbeNeverPasses(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)

	probe := &scriptedProbe{}
	probe.failing.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runWatchdogLoop(ctx, 30*time.Millisecond, probe, testLogger())

	if got := l.recv(t, 300*time.Millisecond); got != "" {
		t.Errorf("a ping (%q) was sent for a probe that never passed", got)
	}
	if probe.calls.Load() < 2 {
		t.Errorf("the probe was called %d times, want it retried every period", probe.calls.Load())
	}
}

// TestWatchdogPeriodToleratesTwoMissedChecks pins the arithmetic: the ping period
// must be short enough that two consecutive failures still fit inside systemd's
// interval, so a momentary stall under heavy decode/inference load cannot kill a
// working Edge.
func TestWatchdogPeriodToleratesTwoMissedChecks(t *testing.T) {
	for _, interval := range []time.Duration{time.Second, 30 * time.Second, 60 * time.Second, 5 * time.Minute} {
		period := watchdogPeriod(interval)
		if period <= 0 {
			t.Fatalf("watchdogPeriod(%s) = %s, want positive", interval, period)
		}
		if 3*period > interval {
			t.Errorf("watchdogPeriod(%s) = %s: three periods (%s) exceed the interval, so two missed checks would already be fatal",
				interval, period, 3*period)
		}
	}
	// The value the appliance unit actually declares.
	if got := watchdogPeriod(60 * time.Second); got != 20*time.Second {
		t.Errorf("watchdogPeriod(WatchdogSec=60) = %s, want 20s", got)
	}
}

// TestStartWatchdogIsNotArmedWithoutWatchdogUsec guards the opt-in boundary: the
// agent must behave exactly as before outside a unit that asks for a watchdog.
func TestStartWatchdogIsNotArmedWithoutWatchdogUsec(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	t.Setenv("WATCHDOG_USEC", "")

	before := runtime.NumGoroutine()
	a := &Agent{log: testLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.startWatchdog(ctx, &scriptedProbe{})

	time.Sleep(100 * time.Millisecond)
	if got := l.recv(t, 100*time.Millisecond); got != "" {
		t.Errorf("a ping (%q) was sent with no WATCHDOG_USEC set", got)
	}
	runtime.GC()
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines grew from %d to %d: the watchdog loop started without a configured interval", before, after)
	}
}

// TestStartWatchdogIsArmedWithWatchdogUsec is the positive half.
func TestStartWatchdogIsArmedWithWatchdogUsec(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	// 60ms so the loop's 20ms period is observable quickly.
	t.Setenv("WATCHDOG_USEC", "60000")

	a := &Agent{log: testLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.startWatchdog(ctx, &scriptedProbe{})

	if got := l.recv(t, 2*time.Second); got != "WATCHDOG=1" {
		t.Errorf("no ping with WATCHDOG_USEC=60000 set (got %q)", got)
	}
}

// TestHealthServerProbeSucceedsWhileServingAndFailsOnceStopped ties the liveness
// signal to the real health surface rather than a flag.
func TestHealthServerProbeSucceedsWhileServingAndFailsOnceStopped(t *testing.T) {
	m := newHealthServerModule("127.0.0.1:0", nil, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Before Start there is no listener, so the probe must fail rather than
	// claim the process is alive.
	if err := m.Probe(ctx); err == nil {
		t.Error("Probe before Start = nil, want an error")
	}

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.probeAddr == "" {
		t.Fatalf("the health server did not resolve a probe address from %q", m.addr)
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
	defer probeCancel()
	if err := m.Probe(probeCtx); err != nil {
		t.Fatalf("Probe while serving: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	failCtx, failCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer failCancel()
	if err := m.Probe(failCtx); err == nil {
		t.Error("Probe after Stop = nil, want an error")
	}
}

// TestHealthServerProbeReportsAHungSurface is the case a watchdog exists for: the
// socket accepts the connection and then never answers. A process-level flag
// would happily report "alive" here; a real round trip must not.
func TestHealthServerProbeReportsAHungSurface(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Accept and hold: never write a byte, never close.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // deliberately leaked until the listener closes
		}
	}()

	m := &healthServerModule{
		probeAddr:   ln.Addr().String(),
		probeClient: &http.Client{Timeout: 300 * time.Millisecond},
		log:         testLogger(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	started := time.Now()
	err = m.Probe(ctx)
	if err == nil {
		t.Fatal("Probe against a surface that accepts but never answers = nil, want an error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Probe took %s to report a hung surface; it must respect its deadline", elapsed)
	}
}

// TestProbeAddressNormalisesUnspecifiedHosts covers the address the probe dials.
func TestProbeAddressNormalisesUnspecifiedHosts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:8091", "127.0.0.1:8091"},
		{"0.0.0.0:8091", "127.0.0.1:8091"},
		{":8091", "127.0.0.1:8091"},
		{"[::]:8091", "127.0.0.1:8091"},
		{"127.0.0.1:0", ""},
		{"not-an-address", "not-an-address"},
	}
	for _, tc := range cases {
		if got := probeAddress(tc.in); got != tc.want {
			t.Errorf("probeAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAgentNotifiesReadyWhenTheHealthSurfaceComesUpAndStoppingOnShutdown proves
// the two notifications the unit's Type=notify and STOPPING handling depend on
// actually leave the process, in order, on a real socket.
//
// READY=1 is asserted to arrive before the agent has necessarily reached READY
// as a health verdict: a DEGRADED agent must still complete systemd startup, or
// systemd would kill and restart it over a condition a restart cannot fix.
func TestAgentNotifiesReadyWhenTheHealthSurfaceComesUpAndStoppingOnShutdown(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	t.Setenv("WATCHDOG_USEC", "") // isolate: this test is about READY/STOPPING

	saas := newY10SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())
	enrolled := w5Enroll(t, dataDir)
	cfg.EdgeID = enrolled.EdgeID

	a := New(cfg)
	stop := startAgent(t, a)

	if got := l.recv(t, 10*time.Second); got != "READY=1" {
		t.Fatalf("first notification = %q, want READY=1", got)
	}

	stop()

	if got := l.recv(t, 10*time.Second); got != "STOPPING=1" {
		t.Fatalf("notification after shutdown = %q, want STOPPING=1", got)
	}
}

// TestAgentDegradedStillNotifiesReady is the restart-loop guard at the agent
// level: an agent that cannot become READY must still complete systemd startup.
// Not doing so would have systemd kill and restart it every TimeoutStartSec
// forever, over a fault (a corrupt credential file) that only an operator fixes.
func TestAgentDegradedStillNotifiesReady(t *testing.T) {
	l := newSDListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	t.Setenv("WATCHDOG_USEC", "")

	saas := newY10SaaS(t)
	dataDir := t.TempDir()
	cfg := w5Config(t, dataDir, saas.url())

	// Enroll, then corrupt the credentials file: a startup fault.
	w5Enroll(t, dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := New(cfg)
	if a.credentialsErr == nil {
		t.Fatal("the agent did not record the credentials error")
	}
	stop := startAgent(t, a)
	defer stop()

	w5WaitFor(t, "the agent to report DEGRADED", 10*time.Second, func() bool {
		return a.Health().State() == health.StateDegraded
	})
	if got := l.recv(t, 10*time.Second); got != "READY=1" {
		t.Fatalf("a DEGRADED agent sent %q as its first notification, want READY=1 so systemd does not restart-loop it", got)
	}
}

func TestServiceProcessWatchdogReturnsFailureAfterConsecutiveHealthProbeMisses(t *testing.T) {
	probe := &scriptedProbe{}
	probe.failing.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fatal := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runServiceProcessWatchdog(ctx, time.Millisecond, time.Millisecond, 3, probe, fatal, testLogger())
		close(done)
	}()

	select {
	case err := <-fatal:
		if err == nil || !strings.Contains(err.Error(), "3 consecutive times") {
			t.Fatalf("fatal error=%v, want three consecutive probe failures", err)
		}
	case <-time.After(time.Second):
		t.Fatal("service watchdog did not report a persistently hung health endpoint")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("service watchdog did not exit after reporting its failure")
	}
}
