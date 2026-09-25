// Package instance provides an OS-owned advisory lock scoped to one Edge data
// directory. The lock file itself remains on disk; the kernel lock is released
// when the process exits or crashes, so stale PID text never blocks recovery.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrAlreadyRunning = errors.New("an Edge process already owns this data directory")

type Lock struct {
	file *os.File
	path string
}

// Acquire obtains an exclusive non-blocking lock for dataDir. Different data
// directories may run concurrently; path aliases resolving to the same
// existing directory share the same lock file.
func Acquire(dataDir string) (*Lock, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("instance lock: data directory is required")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("instance lock: resolve data directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("instance lock: create data directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("instance lock: canonicalize data directory: %w", err)
	}
	path := filepath.Join(canonical, ".geocam-edge.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("instance lock: open lock file: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLockHeld) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("instance lock: acquire lock: %w", err)
	}
	if err := file.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(file, "pid=%d\n", os.Getpid())
		_, _ = file.Seek(0, 0)
	}
	return &Lock{file: file, path: path}, nil
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release drops the OS lock and closes the descriptor. It deliberately does
// not remove the file: unlinking a locked inode can let a second process lock
// a replacement file on some platforms.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("instance lock: release lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("instance lock: close lock file: %w", closeErr)
	}
	return nil
}
