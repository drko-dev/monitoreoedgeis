package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// --- test doubles -----------------------------------------------------------

// fakeSender records every heartbeat and returns scripted results.
type fakeSender struct {
	mu            sync.Mutex
	calls         []transport.HeartbeatRequest
	devIDs        []string
	creds         []string
	results       []error
	fired         chan struct{}
	statusUpdates int
}

func newFakeSender(results ...error) *fakeSender {
	return &fakeSender{results: results, fired: make(chan struct{}, 256)}
}

func (f *fakeSender) Heartbeat(_ context.Context, deviceID, credential string, req transport.HeartbeatRequest) error {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, req)
	f.devIDs = append(f.devIDs, deviceID)
	f.creds = append(f.creds, credential)
	var err error
	if n < len(f.results) {
		err = f.results[n]
	} else if len(f.results) > 0 {
		err = f.results[len(f.results)-1]
	}
	f.mu.Unlock()

	select {
	case f.fired <- struct{}{}:
	default:
	}
	return err
}

func (f *fakeSender) snapshot() []transport.HeartbeatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]transport.HeartbeatRequest(nil), f.calls...)
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSender) recordStatus() {
	f.mu.Lock()
	f.statusUpdates++
	f.mu.Unlock()
}

func (f *fakeSender) statusCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusUpdates
}

// fakeClock records every delay the scheduler asks for and fires timers
// immediately, so a test exercises the real scheduling decisions without
// spending real time.
type fakeClock struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (c *fakeClock) newTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	c.mu.Unlock()

	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch, func() bool { return true }
}

func (c *fakeClock) snapshot() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testOptions returns a Module configuration with every non-determinism
// pinned: a fixed RNG value (0.5, the midpoint, so jitter is a no-op) and a
// fake timer.
func testOptions(sender Sender, clock *fakeClock) Options {
	opts := Options{
		Sender:     sender,
		DeviceID:   "dev-1",
		Credential: "secret-credential",
		Build:      func() transport.HeartbeatRequest { return transport.HeartbeatRequest{EdgeID: "edge-1"} },
		Interval:   30 * time.Second,
		Log:        quietLogger(),
		Now:        func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Rand:       func() float64 { return 0.5 },
		NewTimer:   clock.newTimer,
	}
	if f, ok := sender.(*fakeSender); ok {
		opts.OnStatus = func(Status) { f.recordStatus() }
	}
	return opts
}

// runUntil starts m, blocks until sender has made n calls, then stops the
// module. It returns the status as it stood *before* the stop: Stop
// deliberately overwrites State with "stopped", which would erase the
// scheduling state under test.
func runUntil(t *testing.T, m *Module, sender *fakeSender, n int) Status {
	t.Helper()
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for sender.count() < n {
		select {
		case <-sender.fired:
		case <-deadline:
			t.Fatalf("timed out waiting for %d heartbeats, got %d", n, sender.count())
		}
	}
	// Heartbeat returns to the fake sender before the module records the
	// outcome. Wait for the exact status-update count instead of sampling a
	// partially updated Status and racing the scheduler. Start emits one
	// status update, and each completed send emits exactly one more.
	bookkeepingDeadline := time.After(5 * time.Second)
	for sender.statusCount() < n+1 {
		select {
		case <-bookkeepingDeadline:
			t.Fatalf("timed out waiting for heartbeat bookkeeping: sends=%d status_updates=%d", n, sender.statusCount())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	st := m.Status()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	return st
}


// --- construction -----------------------------------------------------------

func TestNewRejectsIncompleteOptions(t *testing.T) {
	valid := testOptions(newFakeSender(nil), &fakeClock{})

	tests := map[string]func(*Options){
		"no sender":      func(o *Options) { o.Sender = nil },
		"no build":       func(o *Options) { o.Build = nil },
		"no device id":   func(o *Options) { o.DeviceID = "" },
		"no credential":  func(o *Options) { o.Credential = "" },
		"zero interval":  func(o *Options) { o.Interval = 0 },
		"negative intvl": func(o *Options) { o.Interval = -time.Second },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if _, err := New(opts); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

// --- D2: authentication -----------------------------------------------------

func TestHeartbeatSendsDeviceIDAndCredential(t *testing.T) {
	sender := newFakeSender(nil)
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 1)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.devIDs[0] != "dev-1" {
		t.Errorf("device id = %q, want dev-1", sender.devIDs[0])
	}
	if sender.creds[0] != "secret-credential" {
		t.Errorf("credential = %q, want secret-credential", sender.creds[0])
	}
}

// --- D1/D6/D7: payload ------------------------------------------------------

func TestHeartbeatPayloadIsRebuiltPerSend(t *testing.T) {
	sender := newFakeSender(nil)
	var n int64
	opts := testOptions(sender, &fakeClock{})
	opts.Build = func() transport.HeartbeatRequest {
		n++
		return transport.HeartbeatRequest{UptimeSeconds: n}
	}
	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 3)

	calls := sender.snapshot()
	// A payload sampled once and reused would report a frozen uptime.
	for i, c := range calls[:3] {
		if c.UptimeSeconds != int64(i+1) {
			t.Errorf("call %d uptime = %d, want %d", i, c.UptimeSeconds, i+1)
		}
	}
}

// --- D19: jitter ------------------------------------------------------------

func TestJitterStaysWithinBoundsAndVaries(t *testing.T) {
	sender := newFakeSender(nil)
	opts := testOptions(sender, &fakeClock{})

	// Sweep the RNG across its whole range rather than pinning it, so the
	// bounds are checked at both extremes.
	var seq int
	opts.Rand = func() float64 {
		v := float64(seq%11) / 10 // 0.0 .. 1.0
		seq++
		return v
	}
	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	interval := 30 * time.Second
	lo := time.Duration(float64(interval) * (1 - JitterFraction))
	hi := time.Duration(float64(interval) * (1 + JitterFraction))

	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		d := m.jittered(interval)
		if d < lo || d > hi {
			t.Fatalf("jittered delay %s outside [%s, %s]", d, lo, hi)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Errorf("jitter produced only %d distinct delay(s); it is not spreading load", len(seen))
	}
}

func TestJitteredIsANoOpAtTheMidpoint(t *testing.T) {
	m, err := New(testOptions(newFakeSender(nil), &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Rand() == 0.5 is the centre of the window: no shift either way.
	if got := m.jittered(time.Minute); got != time.Minute {
		t.Errorf("jittered(1m) at midpoint = %s, want 1m", got)
	}
}

// --- D16: backoff on transient failure, reset on success ---------------------

func TestTransientFailuresBackOffThenResetOnSuccess(t *testing.T) {
	clock := &fakeClock{}
	sender := newFakeSender(
		fmt.Errorf("%w: boom", transport.ErrSaaSUnavailable),
		fmt.Errorf("%w: boom", transport.ErrSaaSUnavailable),
		fmt.Errorf("%w: boom", transport.ErrSaaSUnavailable),
		nil, // recovery
		nil,
	)
	m, err := New(testOptions(sender, clock))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 5)

	delays := clock.snapshot()
	// delays[0] is the initial startup delay; the delays chosen *after* each
	// send are the ones under test.
	if len(delays) < 5 {
		t.Fatalf("expected at least 5 scheduled delays, got %d", len(delays))
	}
	want := []time.Duration{
		1 * time.Second,  // after failure 1
		2 * time.Second,  // after failure 2
		4 * time.Second,  // after failure 3
		30 * time.Second, // after success: back to the nominal interval
	}
	for i, w := range want {
		if delays[i+1] != w {
			t.Errorf("delay after send %d = %s, want %s", i+1, delays[i+1], w)
		}
	}
}

func TestTransientFailureKeepsModuleAliveAndDegraded(t *testing.T) {
	sender := newFakeSender(fmt.Errorf("%w: refused", transport.ErrSaaSUnavailable))
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 3)

	if st.ConsecutiveFailures < 3 {
		t.Errorf("consecutive failures = %d, want >= 3", st.ConsecutiveFailures)
	}
	if st.LastError != "unreachable" {
		t.Errorf("last error class = %q, want unreachable", st.LastError)
	}
	if !st.LastSuccessAt.IsZero() {
		t.Error("last success should stay zero when no heartbeat ever succeeded")
	}
	// The module keeps running: a SaaS outage is not a reason to give up.
	if sender.count() < 3 {
		t.Errorf("module stopped retrying after %d attempts", sender.count())
	}
}

func TestTimeoutIsClassifiedAsTransient(t *testing.T) {
	sender := newFakeSender(fmt.Errorf("%w: POST /hb", transport.ErrTimeout))
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 2)

	if st.LastError != "timeout" {
		t.Errorf("last error class = %q, want timeout", st.LastError)
	}
	if st.State != StateDegraded {
		t.Errorf("state = %q, want %q", st.State, StateDegraded)
	}
}

func TestServerErrorIsClassifiedAsTransient(t *testing.T) {
	sender := newFakeSender(fmt.Errorf("%w: status 503", transport.ErrUnexpectedStatus))
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 2)

	if got := st.LastError; got != "server_error" {
		t.Errorf("last error class = %q, want server_error", got)
	}
}

// --- D17: 429 ---------------------------------------------------------------

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	clock := &fakeClock{}
	sender := newFakeSender(&transport.RateLimitError{RetryAfter: 42 * time.Second})
	m, err := New(testOptions(sender, clock))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 2)

	delays := clock.snapshot()
	if delays[1] != 42*time.Second {
		t.Errorf("delay after 429 = %s, want the server-stated 42s", delays[1])
	}
	if st.LastError != "rate_limited" {
		t.Errorf("last error class = %q, want rate_limited", st.LastError)
	}
}

func TestRateLimitWithoutRetryAfterFallsBackToBackoff(t *testing.T) {
	clock := &fakeClock{}
	sender := newFakeSender(&transport.RateLimitError{}, nil)
	m, err := New(testOptions(sender, clock))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 2)

	if got := clock.snapshot()[1]; got != BaseBackoff {
		t.Errorf("delay after 429 with no Retry-After = %s, want the base backoff %s", got, BaseBackoff)
	}
}

// --- D17/D33: 401/403 -------------------------------------------------------

func TestUnauthorizedDoesNotRetryAggressively(t *testing.T) {
	clock := &fakeClock{}
	sender := newFakeSender(fmt.Errorf("%w (status 403)", transport.ErrUnauthorized))

	var unauthorizedCalls int
	opts := testOptions(sender, clock)
	opts.OnUnauthorized = func() { unauthorizedCalls++ }

	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 3)

	if st.State != StateUnauthorized {
		t.Errorf("state = %q, want %q", st.State, StateUnauthorized)
	}
	if st.LastError != "unauthorized" {
		t.Errorf("last error class = %q, want unauthorized", st.LastError)
	}

	// Every post-send delay must be the slow auth-failure poll, never the
	// fast transient backoff: a revoked credential is not going to un-revoke
	// itself in one second.
	for i, d := range clock.snapshot()[1:] {
		if d < AuthFailureInterval/2 {
			t.Errorf("delay %d after 401/403 = %s, want the slow %s poll", i, d, AuthFailureInterval)
		}
	}

	// The agent is told exactly once, not once per failed attempt.
	if unauthorizedCalls != 1 {
		t.Errorf("OnUnauthorized called %d times, want exactly 1", unauthorizedCalls)
	}
}

func TestRecoveryAfterUnauthorizedReArmsTheNotification(t *testing.T) {
	sender := newFakeSender(
		fmt.Errorf("%w (status 401)", transport.ErrUnauthorized),
		nil, // credential restored by an operator
		fmt.Errorf("%w (status 401)", transport.ErrUnauthorized),
	)
	var unauthorizedCalls int
	opts := testOptions(sender, &fakeClock{})
	opts.OnUnauthorized = func() { unauthorizedCalls++ }

	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 3)

	if unauthorizedCalls != 2 {
		t.Errorf("OnUnauthorized called %d times, want 2 (once per distinct revocation episode)", unauthorizedCalls)
	}
}

func TestInvalidPayloadDoesNotHammerTheSaaS(t *testing.T) {
	clock := &fakeClock{}
	sender := newFakeSender(fmt.Errorf("%w (status 422)", transport.ErrInvalidRequest))
	m, err := New(testOptions(sender, clock))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 2)

	// Resending an identical rejected body cannot succeed, so the retry waits
	// a full interval rather than escalating through the fast backoff.
	if got := clock.snapshot()[1]; got != 30*time.Second {
		t.Errorf("delay after 422 = %s, want the full 30s interval", got)
	}
	if got := st.LastError; got != "rejected_payload" {
		t.Errorf("last error class = %q, want rejected_payload", got)
	}
}

// --- D1: success bookkeeping ------------------------------------------------

func TestSuccessRecordsTimestampsAndClearsFailures(t *testing.T) {
	sender := newFakeSender(fmt.Errorf("%w: down", transport.ErrSaaSUnavailable), nil)
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := runUntil(t, m, sender, 2)

	if st.State != StateRunning {
		t.Errorf("state = %q, want %q", st.State, StateRunning)
	}
	if st.ConsecutiveFailures != 0 {
		t.Errorf("consecutive failures = %d, want 0 after recovery", st.ConsecutiveFailures)
	}
	if st.LastError != "" {
		t.Errorf("last error = %q, want cleared after recovery", st.LastError)
	}
	if st.LastSuccessAt.IsZero() || st.LastAttemptAt.IsZero() {
		t.Error("success and attempt timestamps must both be set after a successful heartbeat")
	}
}

// --- lifecycle --------------------------------------------------------------

func TestStopIsGracefulAndIdempotent(t *testing.T) {
	sender := newFakeSender(nil)
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 1) // already calls Stop once

	if got := m.Status().State; got != StateStopped {
		t.Errorf("state after Stop = %q, want %q", got, StateStopped)
	}

	// A second Stop must not panic or block.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStopOnANeverStartedModuleIsANoOp(t *testing.T) {
	m, err := New(testOptions(newFakeSender(nil), &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}

func TestStopHaltsSending(t *testing.T) {
	sender := newFakeSender(nil)
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 2)

	after := sender.count()
	time.Sleep(50 * time.Millisecond)
	if got := sender.count(); got != after {
		t.Errorf("sender called %d more times after Stop; the loop is still running", got-after)
	}
}

// A send that is in flight when the context is cancelled is a shutdown, not a
// failure, and must not be counted as one.
func TestCancellationDuringSendIsNotAFailure(t *testing.T) {
	blocked := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once

	sender := &blockingSender{
		onCall: func(ctx context.Context) error {
			once.Do(func() { close(blocked) })
			<-released
			return ctx.Err()
		},
	}
	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-blocked

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done <- m.Stop(ctx)
	}()
	close(released)

	if err := <-done; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := m.Status().ConsecutiveFailures; got != 0 {
		t.Errorf("consecutive failures = %d; a cancelled in-flight send must not count as a failure", got)
	}
}

func TestStopTimesOutRatherThanHanging(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	inFlight := make(chan struct{})
	var once sync.Once
	sender := &blockingSender{onCall: func(context.Context) error {
		once.Do(func() { close(inFlight) })
		<-released
		return nil
	}}

	m, err := New(testOptions(sender, &fakeClock{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Stop must be racing an in-flight send, not the scheduler's first wait.
	<-inFlight

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop error = %v, want a deadline-exceeded wrap", err)
	}
}

type blockingSender struct {
	onCall func(context.Context) error
}

func (b *blockingSender) Heartbeat(ctx context.Context, _, _ string, _ transport.HeartbeatRequest) error {
	return b.onCall(ctx)
}

// --- D27: the status surface must never leak a secret -----------------------

func TestStatusNeverCarriesTheCredential(t *testing.T) {
	sender := newFakeSender(fmt.Errorf("%w: dial tcp 10.0.0.1:443: refused", transport.ErrSaaSUnavailable))
	opts := testOptions(sender, &fakeClock{})
	opts.Credential = "super-secret-credential-value"

	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runUntil(t, m, sender, 2)

	encoded, err := json.Marshal(m.Status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	rendered := string(encoded)

	for _, forbidden := range []string{
		"super-secret-credential-value",
		"Bearer",
		"Authorization",
		"10.0.0.1", // raw transport errors leak endpoints; only a class is stored
		"refused",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("status JSON leaks %q: %s", forbidden, rendered)
		}
	}
}

// --- D19: fleets do not start in lockstep -----------------------------------

func TestInitialDelayIsSpreadAcrossTheStartupWindow(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 10; i++ {
		clock := &fakeClock{}
		sender := newFakeSender(nil)
		opts := testOptions(sender, clock)
		r := float64(i) / 10
		opts.Rand = func() float64 { return r }

		m, err := New(opts)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		runUntil(t, m, sender, 1)

		first := clock.snapshot()[0]
		if first < 0 || first > InitialDelay {
			t.Fatalf("initial delay %s outside [0, %s]", first, InitialDelay)
		}
		seen[first] = true
	}
	if len(seen) < 5 {
		t.Errorf("only %d distinct startup delays across 10 instances; a fleet would still arrive as one spike", len(seen))
	}
}
