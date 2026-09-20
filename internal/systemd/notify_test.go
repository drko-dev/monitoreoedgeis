package systemd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// notifyListener is a real unixgram socket standing in for systemd's
// NOTIFY_SOCKET, plus the datagrams it has received.
type notifyListener struct {
	conn *net.UnixConn
	path string
}

// newNotifyListener creates a listening unixgram socket. The path is kept short
// because sun_path is capped at 104 bytes on darwin (t.TempDir() paths are long
// enough to exceed it).
func newNotifyListener(t *testing.T) *notifyListener {
	t.Helper()
	path := filepath.Join(os.TempDir(), "geocam-sdnotify-"+time.Now().Format("150405.000000000"))
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	l := &notifyListener{conn: conn, path: path}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = os.Remove(path)
	})
	return l
}

// recv returns the next datagram, or "" if none arrives within d.
func (l *notifyListener) recv(t *testing.T, d time.Duration) string {
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

func TestNotifyIsNoOpWithoutNotifySocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if Enabled() {
		t.Error("Enabled() = true with no NOTIFY_SOCKET")
	}
	for _, state := range []string{"READY=1", "WATCHDOG=1", "STOPPING=1"} {
		if err := Notify(state); err != nil {
			t.Errorf("Notify(%q) with no NOTIFY_SOCKET = %v, want nil (a no-op)", state, err)
		}
	}
	if err := Ready(); err != nil {
		t.Errorf("Ready() = %v, want nil", err)
	}
	if err := Watchdog(); err != nil {
		t.Errorf("Watchdog() = %v, want nil", err)
	}
	if err := Stopping(); err != nil {
		t.Errorf("Stopping() = %v, want nil", err)
	}
}

func TestNotifyDeliversEachStateVerbatim(t *testing.T) {
	l := newNotifyListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	if !Enabled() {
		t.Fatal("Enabled() = false with NOTIFY_SOCKET set")
	}

	for _, tc := range []struct {
		send func() error
		want string
	}{
		{Ready, "READY=1"},
		{Watchdog, "WATCHDOG=1"},
		{Stopping, "STOPPING=1"},
		{func() error { return Status("DEGRADED") }, "STATUS=DEGRADED"},
	} {
		if err := tc.send(); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := l.recv(t, 2*time.Second); got != tc.want {
			t.Errorf("datagram = %q, want %q", got, tc.want)
		}
	}
}

func TestNotifyRejectsEmptyState(t *testing.T) {
	l := newNotifyListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	if err := Notify(""); err == nil {
		t.Error("Notify(\"\") = nil, want an error")
	}
}

func TestStatusRejectsMultilineMessages(t *testing.T) {
	l := newNotifyListener(t)
	t.Setenv("NOTIFY_SOCKET", l.path)
	for _, msg := range []string{"a\nb", "a\rb", "a\r\nb"} {
		if err := Status(msg); err == nil {
			t.Errorf("Status(%q) = nil, want an error: the protocol is line-based and a newline would inject a second field", msg)
		}
	}
	if got := l.recv(t, 100*time.Millisecond); got != "" {
		t.Errorf("a rejected status still emitted %q", got)
	}
}

func TestNotifyReportsAnUnreachableSocket(t *testing.T) {
	// Nothing is listening on this path: the notification must fail loudly
	// rather than being silently dropped, so the agent can log it.
	t.Setenv("NOTIFY_SOCKET", filepath.Join(os.TempDir(), "geocam-sdnotify-absent.sock"))
	if err := Watchdog(); err == nil {
		t.Error("Watchdog() to a socket nobody is listening on = nil, want an error")
	}
}

func TestWatchdogInterval(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantOK  bool
		comment string
	}{
		{"", 0, false, "unset: no watchdog configured"},
		{"   ", 0, false, "blank is unset"},
		{"60000000", 60 * time.Second, true, "WatchdogSec=60"},
		{"1000000", time.Second, true, "one second"},
		{"0", 0, false, "zero is not a configured watchdog"},
		{"-1", 0, false, "a negative value is nonsense"},
		{"abc", 0, false, "a non-numeric value is nonsense"},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			t.Setenv("WATCHDOG_USEC", tc.raw)
			got, ok := WatchdogInterval()
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("WatchdogInterval() with %q = (%s, %v), want (%s, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestNotifyTrimsWhitespaceFromTheSocketPath covers an environment file that
// carries a trailing newline or spaces around the value.
func TestNotifyTrimsWhitespaceFromTheSocketPath(t *testing.T) {
	l := newNotifyListener(t)
	t.Setenv("NOTIFY_SOCKET", "  "+l.path+"\n")
	if err := Watchdog(); err != nil {
		t.Fatalf("Watchdog() with a padded path: %v", err)
	}
	if got := l.recv(t, 2*time.Second); got != "WATCHDOG=1" {
		t.Errorf("datagram = %q, want %q", got, "WATCHDOG=1")
	}
}
