package cameracreds

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateMasterKey_CreatesThenLoadsSame(t *testing.T) {
	dir := t.TempDir()

	key1, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(key1) != MasterKeySize {
		t.Fatalf("key length = %d, want %d", len(key1), MasterKeySize)
	}

	key2, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call returned a different key; must load the persisted one")
	}
}

func TestLoadOrCreateMasterKey_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits only")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	if _, err := LoadOrCreateMasterKey(dir); err != nil {
		t.Fatalf("create: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir perm = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(filepath.Join(dir, masterKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("master key file perm = %o, want 0600", perm)
	}
}

func TestLoadOrCreateMasterKey_CorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterKeyFileName)
	if err := os.WriteFile(path, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrCreateMasterKey(dir)
	if err == nil {
		t.Fatal("expected error for corrupt master key, got nil")
	}

	// Must never have been silently regenerated: file content is untouched.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "too-short" {
		t.Fatal("corrupt master key file was rewritten instead of failing closed")
	}
}

func TestLoadOrCreateMasterKey_NoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateMasterKey(dir); err != nil {
		t.Fatalf("create: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != masterKeyFileName {
		t.Fatalf("unexpected dir contents: %v", entries)
	}
}
