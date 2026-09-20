// Package systemd speaks the subset of the sd_notify(3) protocol this agent
// needs to be supervised by the deployment's real init system.
//
// The appliance already runs under systemd (deploy/appliance/systemd), and that
// unit already declares Restart=on-failure with a start-rate limit. That covers
// a process that *exits*. It cannot cover a process that is still running but no
// longer serving: systemd sees a live main process and does nothing. WatchdogSec
// is systemd's answer to exactly that, and it requires the service to say so —
// first READY=1 so Type=notify startup can complete, then a WATCHDOG=1 ping on
// every interval it keeps meeting its own liveness check.
//
// Everything here is a no-op when NOTIFY_SOCKET is unset, which is the case for
// a local `go run`, the Kubernetes deployment (which uses HTTP probes instead)
// and every test. That keeps the mechanism opt-in by construction: nothing
// changes outside a systemd unit that asks for it.
package systemd

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// notifySocketEnv is the variable systemd sets in the service environment.
const notifySocketEnv = "NOTIFY_SOCKET"

// watchdogUsecEnv carries WatchdogSec in microseconds; systemd sets it only when
// the unit declares WatchdogSec=.
const watchdogUsecEnv = "WATCHDOG_USEC"

// notifyTimeout bounds one datagram send. sd_notify is a local, non-blocking
// hand-off to systemd's own socket: if it cannot complete almost immediately
// something is wrong with the socket itself, and the agent must not stall its
// lifecycle over a notification.
const notifyTimeout = 2 * time.Second

// Enabled reports whether the process was started by systemd with a notification
// socket, i.e. whether sd_notify has anywhere to go.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(notifySocketEnv)) != ""
}

// WatchdogInterval reports the interval systemd expects a WATCHDOG=1 ping
// within, and whether a watchdog is configured at all. It is derived from
// WATCHDOG_USEC, which systemd sets only for a unit that declares WatchdogSec=.
func WatchdogInterval() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(watchdogUsecEnv))
	if raw == "" {
		return 0, false
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0, false
	}
	return time.Duration(usec) * time.Microsecond, true
}

// Ready sends READY=1, completing Type=notify startup.
func Ready() error { return Notify("READY=1") }

// Stopping sends STOPPING=1, telling systemd that a clean shutdown has begun.
// systemd cancels the watchdog timer on this, so a slow but deliberate
// shutdown is never mistaken for a hang.
func Stopping() error { return Notify("STOPPING=1") }

// Watchdog sends WATCHDOG=1, resetting systemd's watchdog timer.
func Watchdog() error { return Notify("WATCHDOG=1") }

// Status sends STATUS=<msg>, which systemd shows in `systemctl status`. The
// message must be short, single-line and free of anything sensitive; callers
// pass an agent state name, never a credential or a payload.
func Status(msg string) error {
	if strings.ContainsAny(msg, "\n\r") {
		return fmt.Errorf("systemd: status message must be single-line")
	}
	return Notify("STATUS=" + msg)
}

// Notify sends one sd_notify(3) datagram to the socket systemd exported in
// NOTIFY_SOCKET. It is a no-op returning nil when that variable is unset, so
// callers can invoke it unconditionally.
//
// NOTIFY_SOCKET is either an absolute filesystem path or, on Linux, an
// abstract-namespace name written with a leading "@" — the form systemd
// actually uses for services whose sockets it creates. Go's own net package
// documents a leading "@" as an abstract socket on Linux, so the value is passed
// through unchanged rather than translated here.
func Notify(state string) error {
	addr := strings.TrimSpace(os.Getenv(notifySocketEnv))
	if addr == "" {
		return nil
	}
	if state == "" {
		return fmt.Errorf("systemd: empty notification state")
	}

	conn, err := net.DialTimeout("unixgram", addr, notifyTimeout)
	if err != nil {
		return fmt.Errorf("systemd: dial notify socket %s: %w", addr, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(notifyTimeout)); err != nil {
		return fmt.Errorf("systemd: set notify deadline: %w", err)
	}
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("systemd: write notify state: %w", err)
	}
	return nil
}
