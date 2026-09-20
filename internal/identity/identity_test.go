package identity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEnvOverride(t *testing.T) {
	dir := t.TempDir() // must never be touched by the env-override path

	id, err := Load(dir, "edge-dev-001")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if id.EdgeID != "edge-dev-001" {
		t.Errorf("EdgeID = %q, want %q", id.EdgeID, "edge-dev-001")
	}
	if id.Source != SourceEnvOverride {
		t.Errorf("Source = %q, want %q", id.Source, SourceEnvOverride)
	}
	if !id.IsEnrolled() {
		t.Error("IsEnrolled() = false, want true")
	}
	if _, err := os.Stat(identityPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("identity.json was created by the env-override path: %v", err)
	}
}

func TestLoadCreatesAndPersists(t *testing.T) {
	dir := t.TempDir()

	first, err := Load(dir, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !validUUID(first.EdgeID) {
		t.Errorf("EdgeID = %q, not a valid UUID", first.EdgeID)
	}
	if first.Source != SourcePersisted {
		t.Errorf("Source = %q, want %q", first.Source, SourcePersisted)
	}

	info, err := os.Stat(identityPath(dir))
	if err != nil {
		t.Fatalf("identity.json was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity.json perm = %v, want 0600", perm)
	}

	// Reuse: a second Load with the same dataDir must return the identical edge_id.
	second, err := Load(dir, "")
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if second.EdgeID != first.EdgeID {
		t.Errorf("second EdgeID = %q, want same as first %q", second.EdgeID, first.EdgeID)
	}
}

func TestLoadCreatesDataDirRestrictive(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "nested", "data")

	if _, err := Load(dir, ""); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("data dir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir perm = %v, want 0700", perm)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"bad json", `{not json`},
		{"invalid uuid", `{"edge_id":"not-a-uuid","created_at":"2026-01-01T00:00:00Z","schema_version":1}`},
		{"unsupported schema version", `{"edge_id":"11111111-1111-4111-8111-111111111111","created_at":"2026-01-01T00:00:00Z","schema_version":2}`},
		{"bad created_at", `{"edge_id":"11111111-1111-4111-8111-111111111111","created_at":"not-a-time","schema_version":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(identityPath(dir), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("setup: write identity.json: %v", err)
			}

			_, err := Load(dir, "")
			if err == nil {
				t.Fatal("Load() succeeded on a corrupt file, want error")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("Load() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestLoadUnwritableDataDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("setup: chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	dir := filepath.Join(parent, "data")
	if _, err := Load(dir, ""); err == nil {
		t.Fatal("Load() succeeded under an unwritable parent, want error")
	}
}

func TestLoadValidFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := fileRecord{
		EdgeID:        "11111111-1111-4111-8111-111111111111",
		CreatedAt:     "2026-01-01T00:00:00Z",
		SchemaVersion: 1,
	}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(identityPath(dir), data, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	id, err := Load(dir, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if id.EdgeID != rec.EdgeID {
		t.Errorf("EdgeID = %q, want %q", id.EdgeID, rec.EdgeID)
	}
	wantTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !id.CreatedAt.Equal(wantTime) {
		t.Errorf("CreatedAt = %v, want %v", id.CreatedAt, wantTime)
	}
	if id.Status != StatusEnrolled {
		t.Errorf("Status = %q, want %q", id.Status, StatusEnrolled)
	}
}

// TestLoad_SurvivesLeftoverTmpFromAbruptKill (Y2): a process killed between
// os.CreateTemp and the final os.Rename in save() leaves a randomly-named
// ".identity-*.json.tmp" file behind -- the deterministic proxy this task
// asks for in place of a real power cut. Load must never pick it up, and
// the last durably-committed identity.json must load exactly as before.
func TestLoad_SurvivesLeftoverTmpFromAbruptKill(t *testing.T) {
	dir := t.TempDir()

	first, err := Load(dir, "")
	if err != nil {
		t.Fatalf("initial Load() error = %v", err)
	}
	before, err := os.ReadFile(identityPath(dir))
	if err != nil {
		t.Fatalf("reading identity.json after first Load: %v", err)
	}

	// Simulate the abrupt kill: a partial temp file survives, the rename
	// that would have replaced identity.json never happened.
	leftover, err := os.CreateTemp(dir, ".identity-*.json.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := leftover.WriteString(`{"edge_id":"garbage`); err != nil {
		t.Fatal(err)
	}
	leftover.Close()

	second, err := Load(dir, "")
	if err != nil {
		t.Fatalf("Load() after leftover tmp file: %v", err)
	}
	if second.EdgeID != first.EdgeID {
		t.Errorf("EdgeID changed after a leftover tmp file: %q -> %q", first.EdgeID, second.EdgeID)
	}
	after, err := os.ReadFile(identityPath(dir))
	if err != nil {
		t.Fatalf("reading identity.json after second Load: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("identity.json content changed:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestUUIDGenerationIsUnique(t *testing.T) {
	a, err := newUUIDv4()
	if err != nil {
		t.Fatalf("newUUIDv4() error = %v", err)
	}
	b, err := newUUIDv4()
	if err != nil {
		t.Fatalf("newUUIDv4() error = %v", err)
	}
	if a == b {
		t.Error("newUUIDv4() produced the same value twice")
	}
	if !validUUID(a) || !validUUID(b) {
		t.Errorf("generated UUIDs are not valid: %q, %q", a, b)
	}
}
