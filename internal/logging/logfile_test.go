package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceLogFileIsCreatedPrivateAndClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "edge.log")
	t.Setenv("GEOCAM_LOG_FILE", path)
	logger := New("info", "test-version", "edge-test", "cloud")
	logger.Info("service started", "status", "READY")
	if err := Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(log): %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("log file permissions = %04o, want 0600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	if !strings.Contains(string(body), "service started") || !strings.Contains(string(body), "READY") {
		t.Errorf("log output missing expected record: %s", body)
	}
}
