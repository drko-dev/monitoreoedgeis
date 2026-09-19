//go:build linux

package systemd

import (
	"net"
	"testing"
	"time"
)

// TestAbstractSocketAddressForm exercises the address form systemd actually uses
// for the notification socket it creates: an abstract-namespace name written
// with a leading "@". The conversion to the kernel's NUL-prefixed form is Go's
// own documented behaviour for unix addresses on Linux, so this asserts it
// rather than reimplementing it.
//
// It is Linux-only on purpose. On darwin an "@"-prefixed name is an ordinary
// filename, so the same test there would pass while proving nothing about the
// abstract namespace (and would leave a stray socket file behind).
func TestAbstractSocketAddressForm(t *testing.T) {
	name := "@geocam-sdnotify-" + time.Now().Format("150405.000000000")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen on abstract address %s: %v", name, err)
	}
	defer conn.Close()

	t.Setenv("NOTIFY_SOCKET", name)
	if !Enabled() {
		t.Fatal("Enabled() = false with an abstract NOTIFY_SOCKET set")
	}
	if err := Watchdog(); err != nil {
		t.Fatalf("Watchdog() to an abstract socket: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read from abstract socket: %v", err)
	}
	if got := string(buf[:n]); got != "WATCHDOG=1" {
		t.Errorf("datagram = %q, want %q", got, "WATCHDOG=1")
	}
}
