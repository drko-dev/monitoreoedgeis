package rtsp_test

// Hito W — W6 (camera loss) and W7 (camera/RTSP credentials).
//
// These tests drive the real rtsp.Supervisor / rtsp.Manager / rtsp.Dial
// against a fully local fake RTSP server. They deliberately exercise the
// existing contract rather than a new one:
//
//   - A camera that disappears on the wire always reports "degraded" while
//     the supervisor keeps retrying. "offline" is the supervisor's *stopped*
//     state, not the camera-unreachable state (see Supervisor.run's defer).
//   - A read timeout after a successful session counts as a timeout AND a
//     stall; a peer close or a refused dial counts as neither.
//   - reconnect_count increments on every dial failure and every stream
//     interruption, and is never reset by a successful reconnect.
//   - A wrong password is reported as auth_failed, retried with the same
//     exponential backoff, and the username/password never appear in status
//     or logs.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// ---------------------------------------------------------------------------
// Test-local RTSP fixture
// ---------------------------------------------------------------------------

type fakeBehavior string

const (
	// behaviorStream delivers interleaved RTP on channel 0.
	behaviorStream fakeBehavior = "stream"
	// behaviorSilent holds the TCP connection open and sends nothing, so the
	// client can only notice through its own read deadline.
	behaviorSilent fakeBehavior = "silent"
	// behaviorClose drops the TCP connection right after PLAY — what a camera
	// powering off or losing its link looks like on the wire.
	behaviorClose fakeBehavior = "close"
)

// fakeRTSP implements only what internal/rtsp actually negotiates: a Digest
// challenge, DESCRIBE/SETUP/PLAY, and interleaved RTP. Deliberately minimal:
// no real camera, no fixed port, no external network.
type fakeRTSP struct {
	username string
	password string
	realm    string
	nonce    string

	listener net.Listener
	addr     string

	mu       sync.Mutex
	behavior fakeBehavior
	conns    []net.Conn
	accepted int
}

func newFakeRTSP(t *testing.T, behavior fakeBehavior) *fakeRTSP {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake rtsp listen: %v", err)
	}
	s := &fakeRTSP{
		username: "admin",
		password: "correct-horse",
		realm:    "geocam-tests",
		nonce:    "nonce-1",
		listener: l,
		addr:     l.Addr().String(),
		behavior: behavior,
	}
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

func (s *fakeRTSP) setBehavior(b fakeBehavior) {
	s.mu.Lock()
	s.behavior = b
	conns := append([]net.Conn(nil), s.conns...)
	s.mu.Unlock()

	// Losing the link is an event, not something the peer can wait to notice:
	// when the camera "powers off", every live socket drops immediately.
	if b == behaviorClose {
		for _, c := range conns {
			_ = c.Close()
		}
	}
}

func (s *fakeRTSP) acceptedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// dropLink takes the camera off the network: the listening port disappears
// (so a dial is refused) and every established socket is torn down at once.
func (s *fakeRTSP) dropLink() {
	_ = s.listener.Close()
	s.mu.Lock()
	conns := append([]net.Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *fakeRTSP) Close() {
	_ = s.listener.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func (s *fakeRTSP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.accepted++
		behavior := s.behavior
		if behavior != behaviorClose {
			s.conns = append(s.conns, conn)
		}
		s.mu.Unlock()

		if behavior == behaviorClose {
			// The camera is gone: the port still answers, but nothing
			// speaks RTSP behind it.
			_ = conn.Close()
			continue
		}
		go s.handle(conn)
	}
}

func (s *fakeRTSP) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	var sessionID string

	// stopStream ends this connection's packet writer. It is closed exactly
	// once, on TEARDOWN or when the request loop unwinds for any reason, so a
	// stream writer can never outlive its connection.
	var stopOnce sync.Once
	stopStream := make(chan struct{})
	stop := func() { stopOnce.Do(func() { close(stopStream) }) }
	defer stop()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		method, uri := fields[0], fields[1]

		headers := map[string]string{}
		for {
			headerLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			headerLine = strings.TrimRight(headerLine, "\r\n")
			if headerLine == "" {
				break
			}
			if i := strings.Index(headerLine, ":"); i != -1 {
				key := strings.ToLower(strings.TrimSpace(headerLine[:i]))
				headers[key] = strings.TrimSpace(headerLine[i+1:])
			}
		}
		cseq := headers["cseq"]

		switch method {
		case "DESCRIBE":
			if headers["authorization"] == "" {
				fmt.Fprintf(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\n"+
					"WWW-Authenticate: Digest realm=%q, nonce=%q\r\n\r\n", cseq, s.realm, s.nonce)
				continue
			}
			if !s.digestAccepted(headers["authorization"], uri) {
				// Second 401 carries no fresh challenge, exactly like a camera
				// that simply refuses the credential. internal/rtsp surfaces
				// this as "describe status 401".
				fmt.Fprintf(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\n\r\n", cseq)
				return
			}
			sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=Session\r\nt=0 0\r\n" +
				"m=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:track1\r\n"
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Type: application/sdp\r\n"+
				"Content-Length: %d\r\n\r\n%s", cseq, len(sdp), sdp)

		case "SETUP":
			sessionID = "session-1"
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\n"+
				"Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\nSession: %s;timeout=60\r\n\r\n",
				cseq, sessionID)

		case "PLAY":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\nRange: npt=0.000-\r\n\r\n",
				cseq, sessionID)
			s.mu.Lock()
			behavior := s.behavior
			s.mu.Unlock()

			switch behavior {
			case behaviorClose:
				// The camera's link dies the instant the stream starts.
				return
			case behaviorSilent:
				// Keep the socket open and stay mute until the client tears
				// down (or its deadline expires and it closes).
				_, _ = reader.ReadString('\n')
				return
			default:
				go s.stream(conn, stopStream)
			}

		case "TEARDOWN":
			stop()
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\n\r\n", cseq, sessionID)
			return
		}
	}
}

func (s *fakeRTSP) stream(conn net.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	n := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Re-read the behaviour every tick so a test can make a live
			// camera go silent and then resume on the same connection.
			s.mu.Lock()
			behavior := s.behavior
			s.mu.Unlock()
			if behavior != behaviorStream {
				continue
			}
			payload := []byte(fmt.Sprintf("rtp-%d", n))
			n++
			header := []byte{'$', 0, 0, 0}
			binary.BigEndian.PutUint16(header[2:], uint16(len(payload)))
			if _, err := conn.Write(header); err != nil {
				return
			}
			if _, err := conn.Write(payload); err != nil {
				return
			}
		}
	}
}

func (s *fakeRTSP) digestAccepted(auth, uri string) bool {
	ha1 := md5hex(s.username + ":" + s.realm + ":" + s.password)
	ha2 := md5hex("DESCRIBE:" + uri)
	return strings.Contains(auth, md5hex(ha1+":"+s.nonce+":"+ha2))
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Local helpers
// ---------------------------------------------------------------------------

func testConfig() rtsp.Config {
	return rtsp.Config{
		StreamRole:     rtsp.StreamRoleSub,
		PacketTimeout:  100 * time.Millisecond,
		InitialBackoff: 30 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		DialTimeout:    500 * time.Millisecond,
		Enabled:        true,
	}
}

func cameraTarget(s *fakeRTSP) rtsp.CameraTarget {
	return rtsp.CameraTarget{
		CandidateKey: "cam-1",
		Addr:         s.addr,
		RTSPPath:     "/live",
		Username:     s.username,
		Password:     s.password,
		StreamRole:   rtsp.StreamRoleSub,
		Codec:        "H264",
		Width:        1920,
		Height:       1080,
		FPS:          15,
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
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

// stopSupervisor bounds Stop. Stop itself takes no context, so a supervisor
// whose goroutine is wedged would otherwise hang the whole test binary.
func stopSupervisor(t *testing.T, s *rtsp.Supervisor) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Supervisor.Stop blocked: a supervision goroutine is stuck")
	}
}

// syncBuffer collects log output written by the supervisor goroutine without
// racing the test goroutine that reads it.
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

// ---------------------------------------------------------------------------
// W6 — camera loss
// ---------------------------------------------------------------------------

// A camera that drops the socket leaves online, reports degraded, counts a
// reconnect without inventing a timeout, and comes back online on its own
// once the camera is reachable again.
func TestW6_PeerCloseIsNotATimeoutAndTheSupervisorRecovers(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sup := rtsp.NewSupervisor(cameraTarget(srv), testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})
	online := sup.Snapshot()
	if online.PacketsReceived == 0 {
		t.Fatal("supervisor reported online but received no packets")
	}

	// The camera disappears.
	srv.setBehavior(behaviorClose)

	waitFor(t, "a reconnect after the peer closed the socket", 5*time.Second, func() bool {
		return sup.Snapshot().ReconnectCount > online.ReconnectCount
	})

	lost := sup.Snapshot()
	if lost.Status == rtsp.StateOnline {
		t.Error("status is still online after the camera dropped the socket")
	}
	if lost.Status == rtsp.StateOffline {
		t.Error("camera loss must not report the supervisor's stopped state (offline); " +
			"the supervisor is still retrying")
	}
	if lost.TimeoutCount != online.TimeoutCount {
		t.Errorf("a peer close was counted as a timeout: timeout_count %d -> %d",
			online.TimeoutCount, lost.TimeoutCount)
	}
	if lost.StallCount != online.StallCount {
		t.Errorf("a peer close was counted as a stream stall: stall_count %d -> %d",
			online.StallCount, lost.StallCount)
	}
	if lost.LastErrorSafe == "" {
		t.Error("LastErrorSafe is empty after a peer close; the failure class was dropped")
	}

	// The camera comes back.
	srv.setBehavior(behaviorStream)
	waitFor(t, "recovery to online", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > lost.PacketsReceived
	})
	if got := sup.Snapshot().LastErrorSafe; got != "" {
		t.Errorf("LastErrorSafe = %q after recovery, want it cleared", got)
	}
}

// A camera that keeps the socket open but stops sending is a *timeout* and a
// *stall* of an established stream, and the supervisor recovers when the
// stream resumes.
func TestW6_SilenceIsATimeoutAndAStallAndTheSupervisorRecovers(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sup := rtsp.NewSupervisor(cameraTarget(srv), testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})
	before := sup.Snapshot()

	srv.setBehavior(behaviorSilent)

	waitFor(t, "silence to be detected", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.TimeoutCount > before.TimeoutCount && snap.StallCount > before.StallCount
	})

	stalled := sup.Snapshot()
	if stalled.ReconnectCount <= before.ReconnectCount {
		t.Errorf("reconnect_count did not advance on a silent stream: %d -> %d",
			before.ReconnectCount, stalled.ReconnectCount)
	}
	if stalled.Status != rtsp.StateDegraded {
		t.Errorf("status = %q on a silent stream, want %q", stalled.Status, rtsp.StateDegraded)
	}

	srv.setBehavior(behaviorStream)
	waitFor(t, "recovery after silence", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > stalled.PacketsReceived
	})
}

// A dial that never completes is neither a timeout nor a stall: the
// supervisor had no established stream to stall.
func TestW6_DialRefusedDegradesTheStreamWithoutATimeoutCount(t *testing.T) {
	// Reserve a port, then close it so nothing is listening: this is a real
	// ECONNREFUSED from the local kernel, not a simulated error.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	refusedAddr := l.Addr().String()
	_ = l.Close()

	sup := rtsp.NewSupervisor(rtsp.CameraTarget{
		CandidateKey: "cam-1",
		Addr:         refusedAddr,
		RTSPPath:     "/live",
		Username:     "admin",
		Password:     "correct-horse",
		StreamRole:   rtsp.StreamRoleSub,
	}, testConfig(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "a refused dial to be recorded", 5*time.Second, func() bool {
		return sup.Snapshot().ReconnectCount > 0
	})

	snap := sup.Snapshot()
	if snap.Status != rtsp.StateDegraded {
		t.Errorf("status = %q after a refused dial, want %q", snap.Status, rtsp.StateDegraded)
	}
	if snap.TimeoutCount != 0 {
		t.Errorf("timeout_count = %d after a refused dial, want 0 (nothing timed out)", snap.TimeoutCount)
	}
	if snap.StallCount != 0 {
		t.Errorf("stall_count = %d after a refused dial, want 0 (no stream ever played)", snap.StallCount)
	}
	if snap.LastErrorSafe == "" {
		t.Error("LastErrorSafe is empty after a refused dial")
	}
}

// Pins the contract the audit established: "offline" is what a supervisor
// reports once it has been stopped, and it is never how an unreachable camera
// is reported while the supervisor is still retrying.
func TestW6_OfflineIsTheStoppedStateNotTheCameraLossState(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sup := rtsp.NewSupervisor(cameraTarget(srv), testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})

	// Make the camera unreachable and let at least one retry happen.
	srv.dropLink()
	waitFor(t, "a retry against the closed listener", 5*time.Second, func() bool {
		return sup.Snapshot().ReconnectCount > 0
	})
	if got := sup.Snapshot().Status; got == rtsp.StateOffline {
		t.Fatal("a camera becoming unreachable reported offline; the supervisor is still retrying")
	}

	stopSupervisor(t, sup)
	if got := sup.Snapshot().Status; got != rtsp.StateOffline {
		t.Errorf("status after Stop = %q, want %q", got, rtsp.StateOffline)
	}
}

// Regression: Config documents InitialBackoff with a 1s default, and the
// supervisor normalises a zero value — but it used to reset to the raw
// config field after every successful session. With InitialBackoff unset,
// one successful connect therefore dropped the retry delay to 0 and turned a
// flapping camera into an unthrottled reconnect storm.
func TestW6_ReconnectBackoffIsNotZeroedAfterASuccessfulSession(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	// Only PacketTimeout is set: InitialBackoff and MaxBackoff stay zero on
	// purpose, which is exactly the shape that used to hot-loop.
	cfg := rtsp.Config{PacketTimeout: 60 * time.Millisecond, DialTimeout: time.Second}

	sup := rtsp.NewSupervisor(cameraTarget(srv), cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})
	online := sup.Snapshot()

	srv.setBehavior(behaviorSilent)

	// With the documented 1s default applying, at most a couple of reconnects
	// fit in this window. Without it the loop reconnects every ~60ms.
	time.Sleep(900 * time.Millisecond)
	reconnects := sup.Snapshot().ReconnectCount - online.ReconnectCount

	if reconnects == 0 {
		t.Fatal("no reconnect happened at all; the test window is too short to judge backoff")
	}
	if reconnects > 4 {
		t.Errorf("reconnect_count grew by %d in 900ms with InitialBackoff unset; "+
			"the documented 1s default is not being applied (reconnect storm)", reconnects)
	}
}

// Regression: Start used to be unguarded, so a second call spawned a second
// run goroutine and both closed doneChan on exit ("close of closed channel").
func TestW6_DoubleStartIsIdempotent(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sup := rtsp.NewSupervisor(cameraTarget(srv), testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.Start(ctx)
	sup.Start(ctx) // must be a no-op, not a second supervisor

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})

	if got := srv.acceptedCount(); got != 1 {
		t.Errorf("fake camera accepted %d connections for one started supervisor, want 1", got)
	}

	stopSupervisor(t, sup)
}

// Regression: Stop on a supervisor that was never started waited forever on a
// done channel no goroutine would ever close.
func TestW6_StopBeforeStartReturnsImmediately(t *testing.T) {
	sup := rtsp.NewSupervisor(rtsp.CameraTarget{CandidateKey: "cam-1", Addr: "127.0.0.1:1"}, testConfig(), nil)
	stopSupervisor(t, sup)
}

// Regression: the agent registers camera targets during construction and
// calls Start afterwards. SetTargets used to skip Start when the manager had
// no context yet, so those supervisors never ran and Manager.Stop blocked
// forever on their done channels.
func TestW6_ManagerStartAfterSetTargetsDoesNotDeadlock(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sink := &statusSink{}
	mgr := rtsp.NewManager(testConfig(), sink, nil)

	// Targets first, Start second — the production ordering.
	mgr.SetTargets([]rtsp.CameraTarget{cameraTarget(srv)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Manager.Start: %v", err)
	}

	waitFor(t, "the pre-registered supervisor to come online", 5*time.Second, func() bool {
		for _, cam := range mgr.Snapshot() {
			if cam.CandidateKey == "cam-1" && cam.Status == rtsp.StateOnline {
				return true
			}
		}
		return false
	})

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Manager.Stop after SetTargets-before-Start: %v", err)
	}
}

// The manager's target map is the single-flight guard: re-declaring the same
// camera must not spin up a second supervisor (a second RTSP session) for it.
func TestW6_ManagerDoesNotCreateASecondSupervisorForTheSameCamera(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	sink := &statusSink{}
	mgr := rtsp.NewManager(testConfig(), sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Manager.Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = mgr.Stop(stopCtx)
	}()

	mgr.SetTargets([]rtsp.CameraTarget{cameraTarget(srv)})
	waitFor(t, "the camera to come online", 5*time.Second, func() bool {
		for _, cam := range mgr.Snapshot() {
			if cam.Status == rtsp.StateOnline {
				return true
			}
		}
		return false
	})

	// Re-declare the identical target several times.
	for i := 0; i < 5; i++ {
		mgr.SetTargets([]rtsp.CameraTarget{cameraTarget(srv)})
	}
	time.Sleep(200 * time.Millisecond)

	if got := len(mgr.KnownCameras()); got != 1 {
		t.Errorf("manager tracks %d cameras for one target, want 1", got)
	}
	if got := srv.acceptedCount(); got != 1 {
		t.Errorf("fake camera accepted %d connections after re-declaring one target, want 1 "+
			"(a second supervisor opened a parallel session)", got)
	}
}

// Stopping a supervisor must release its goroutine, not leave it parked.
func TestW6_StopReleasesTheSupervisionGoroutine(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	baseline := runtime.NumGoroutine()

	sup := rtsp.NewSupervisor(cameraTarget(srv), testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	waitFor(t, "a playing stream", 5*time.Second, func() bool {
		snap := sup.Snapshot()
		return snap.Status == rtsp.StateOnline && snap.PacketsReceived > 0
	})

	stopSupervisor(t, sup)

	waitFor(t, "the supervision goroutine to exit", 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	})
}

// ---------------------------------------------------------------------------
// W7 — camera / RTSP credentials
// ---------------------------------------------------------------------------

// A wrong password is surfaced as an authentication denial, not as a stream
// timeout, and the error text carries neither the username nor the password.
func TestW7_WrongRTSPPasswordIsAnAuthDenialNotATimeout(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := rtsp.Dial(ctx, srv.addr, "/live", srv.username, "wrong-password", time.Second)
	if err == nil {
		t.Fatal("Dial with a wrong password succeeded")
	}

	if !strings.Contains(err.Error(), "401") {
		t.Errorf("auth failure is not identifiable in the error: %v", err)
	}
	if strings.Contains(err.Error(), rtsp.ErrTimeout.Error()) {
		t.Errorf("a wrong password was reported as a timeout: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Errorf("the password leaked into the Dial error: %v", err)
	}
	if strings.Contains(err.Error(), "admin:"+"wrong-password") {
		t.Errorf("the credential pair leaked into the Dial error: %v", err)
	}

	// The sanitizer used on every supervisor log line must leave nothing
	// credential-shaped behind either.
	sanitized := rtsp.SanitizeError(err)
	if strings.Contains(sanitized, "wrong-password") {
		t.Errorf("SanitizeError did not scrub the password: %q", sanitized)
	}
}

// Bad credentials must never reach the camera status served on /status, nor
// the supervisor's logs, and must not be retried aggressively.
func TestW7_WrongRTSPPasswordNeverReachesStatusOrLogs(t *testing.T) {
	const password = "wrong-password"
	srv := newFakeRTSP(t, behaviorStream)

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	target := cameraTarget(srv)
	target.Password = password

	sup := rtsp.NewSupervisor(target, testConfig(), logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "the credential rejection to be retried at least twice", 5*time.Second, func() bool {
		return sup.Snapshot().ReconnectCount >= 2
	})
	if got := srv.acceptedCount(); got == 0 {
		t.Fatal("fake camera never accepted a connection")
	}

	snap := sup.Snapshot()
	if !strings.Contains(snap.LastErrorSafe, "401") {
		t.Errorf("LastErrorSafe = %q, want it to name the authentication denial", snap.LastErrorSafe)
	}

	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	for _, forbidden := range []string{password, srv.username + ":" + password, "Authorization"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("camera status JSON contains %q: %s", forbidden, encoded)
		}
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("supervisor logs contain %q:\n%s", forbidden, logs.String())
		}
	}
}

// The existing contract retries a rejected credential, but only with the
// configured exponential backoff — never in a tight loop.
func TestW7_BadCredentialsAreRetriedWithBackoffNotInALoop(t *testing.T) {
	const password = "wrong-password"
	srv := newFakeRTSP(t, behaviorStream)

	cfg := testConfig()
	cfg.InitialBackoff = 60 * time.Millisecond
	cfg.MaxBackoff = 2 * time.Second

	target := cameraTarget(srv)
	target.Password = password

	sup := rtsp.NewSupervisor(target, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer stopSupervisor(t, sup)

	waitFor(t, "the first rejected dial", 5*time.Second, func() bool {
		return sup.Snapshot().ReconnectCount >= 1
	})

	time.Sleep(700 * time.Millisecond)
	attempts := sup.Snapshot().ReconnectCount

	// 60ms, 120ms, 240ms, 480ms -> 4 attempts fit in 700ms; a handful of
	// extra handshakes must not turn into dozens.
	if attempts > 6 {
		t.Errorf("%d dial attempts in 700ms against a rejected credential; "+
			"the exponential backoff is not being applied", attempts)
	}
}

func TestY5_AuthFailureIsDistinctAndRecoversAfterCredentialUpdate(t *testing.T) {
	srv := newFakeRTSP(t, behaviorStream)
	cfg := testConfig()
	cfg.InitialBackoff = 40 * time.Millisecond
	cfg.MaxBackoff = 200 * time.Millisecond

	target := cameraTarget(srv)
	target.Password = "wrong-password"

	mgr := rtsp.NewManager(cfg, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("manager start: %v", err)
	}
	mgr.SetTargets([]rtsp.CameraTarget{target})

	waitFor(t, "auth_failed camera state", 5*time.Second, func() bool {
		for _, camera := range mgr.Snapshot() {
			if camera.Status == rtsp.StateAuthFailed {
				return true
			}
		}
		return false
	})

	target.Password = srv.password
	mgr.SetTargets([]rtsp.CameraTarget{target})
	waitFor(t, "camera recovery after credential update", 5*time.Second, func() bool {
		for _, camera := range mgr.Snapshot() {
			if camera.Status == rtsp.StateOnline && camera.PacketsReceived > 0 {
				return true
			}
		}
		return false
	})

	if got := len(mgr.Snapshot()); got != 1 {
		t.Fatalf("manager tracks %d cameras after credential recovery, want 1", got)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("manager stop: %v", err)
	}
}

// statusSink records what the manager publishes (health.Reporter satisfies the
// same interface in production).
type statusSink struct {
	mu   sync.Mutex
	cams []rtsp.CameraStreamStatus
}

func (s *statusSink) SetCameras(cams []rtsp.CameraStreamStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cams = append([]rtsp.CameraStreamStatus(nil), cams...)
}
