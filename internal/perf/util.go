package perf

import (
	"fmt"
	"os"
	"time"
)

// tempSocketPath returns a short, unique Unix socket path for the vision
// worker. It is deliberately rooted at the OS temp directory rather than a
// t.TempDir(): macOS caps sun_path at 104 bytes and the Go test temp tree is
// usually far too long.
func tempSocketPath() string {
	return fmt.Sprintf("%s/geocam-perf-%d-%d.sock", os.TempDir(), os.Getpid(), time.Now().UnixNano()%1_000_000)
}

// TempWorkDir creates a scratch directory for benchmark artifacts (the
// generated clip, the JSON report). Callers own its removal.
func TempWorkDir(prefix string) (string, error) {
	return os.MkdirTemp("", prefix)
}
