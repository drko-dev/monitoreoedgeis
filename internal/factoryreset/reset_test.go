package factoryreset

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResetRequiresExplicitConfirmation(t *testing.T) {
	dir := t.TempDir()
	if err := Reset(dir, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("Reset without confirmation error = %v, want %v", err, ErrConfirmationRequired)
	}
}

func TestResetRemovesOnlyDeviceState(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range StatePaths {
		path := filepath.Join(dir, rel)
		if filepath.Ext(rel) == "" {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "state"), []byte("device"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("device"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	software := filepath.Join(dir, "releases", "geocam-edge")
	if err := os.MkdirAll(filepath.Dir(software), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(software, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Reset(dir, true); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	for _, rel := range StatePaths {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("state path %q still exists: %v", rel, err)
		}
	}
	for _, rel := range []string{"camera_credentials.json", "camera_master.key"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("camera state %q still exists: %v", rel, err)
		}
	}
	if got, err := os.ReadFile(software); err != nil || string(got) != "binary" {
		t.Fatalf("software artifact changed or removed: %q, %v", got, err)
	}
}

func TestResetRejectsUnsafeDataDir(t *testing.T) {
	if err := Reset(string(filepath.Separator), true); err == nil {
		t.Fatal("Reset('/') succeeded, want safety error")
	}
}
