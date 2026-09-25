package instance

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockRejectsDuplicateDataDirectoryAndAllowsRelease(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "edge-data")
	first, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire(first): %v", err)
	}
	defer first.Release()

	if _, err := Acquire(filepath.Join(root, ".", "edge-data")); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Acquire(duplicate) error = %v, want ErrAlreadyRunning", err)
	}

	other, err := Acquire(filepath.Join(root, "other-edge"))
	if err != nil {
		t.Fatalf("Acquire(other data dir): %v", err)
	}
	if err := other.Release(); err != nil {
		t.Fatalf("Release(other): %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release(first): %v", err)
	}
	third, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire(after release): %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("Release(third): %v", err)
	}
}
