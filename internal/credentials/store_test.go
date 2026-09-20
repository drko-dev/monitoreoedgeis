package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingReturnsUnenrolled(t *testing.T) {
	dir := t.TempDir()
	creds, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if creds.Status != StatusUnenrolled {
		t.Errorf("Status = %q, want %q", creds.Status, StatusUnenrolled)
	}
	if creds.IsEnrolled() {
		t.Error("IsEnrolled() = true, want false")
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := Credentials{
		EdgeID: "edge-1", DeviceID: "device-1", Credential: "edg_live_secret",
		CredentialVersion: 1, TenantID: "tenant-1", SiteID: "site-1",
		EnrolledAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Status != StatusEnrolled {
		t.Errorf("Status = %q, want %q", got.Status, StatusEnrolled)
	}
	if got.EdgeID != want.EdgeID || got.Credential != want.Credential || got.CredentialVersion != want.CredentialVersion {
		t.Errorf("got = %+v, want %+v", got, want)
	}
	if !got.EnrolledAt.Equal(want.EnrolledAt) {
		t.Errorf("EnrolledAt = %v, want %v", got.EnrolledAt, want.EnrolledAt)
	}
}

// TestPersistenceSurvivesRestart mirrors identity's persistence test: a
// fresh Load() (simulating process restart) must see exactly what Save()
// wrote.
func TestPersistenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	want := Credentials{
		EdgeID: "edge-1", DeviceID: "device-1", Credential: "edg_live_secret",
		CredentialVersion: 3, EnrolledAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// "Restart": nothing but a fresh Load() against the same dir.
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() after restart error = %v", err)
	}
	if got.Credential != want.Credential || got.CredentialVersion != want.CredentialVersion {
		t.Errorf("got = %+v, want %+v", got, want)
	}
}

func TestSavePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if err := Save(dir, Credentials{EdgeID: "edge-1", Credential: "secret", EnrolledAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(credentialsPath(dir))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(credentialsPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestLoadRejectsUnsupportedSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema_version":99,"edge_id":"edge-1","credential":"secret","enrolled_at":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(credentialsPath(dir), []byte(body), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("Exists() = true before Save, want false")
	}
	if err := Save(dir, Credentials{EdgeID: "edge-1", Credential: "secret", EnrolledAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !Exists(dir) {
		t.Error("Exists() = false after Save, want true")
	}
}

// TestSaveFailureLeavesOldCredentialIntact simulates the "persistence fails
// after rotation" scenario: Save fails part-way (permission denied on
// MkdirAll target being a file, not a dir) and the previously saved
// credential file must remain byte-for-byte untouched.
func TestSaveFailureLeavesOldCredentialIntact(t *testing.T) {
	dir := t.TempDir()
	original := Credentials{EdgeID: "edge-1", Credential: "edg_live_original", CredentialVersion: 1, EnrolledAt: time.Now().UTC().Truncate(time.Second)}
	if err := Save(dir, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before, err := os.ReadFile(credentialsPath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Make the data dir read-only so the temp-file creation for the next
	// Save() fails before any rename happens.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = Save(dir, Credentials{EdgeID: "edge-1", Credential: "edg_live_new", CredentialVersion: 2, EnrolledAt: original.EnrolledAt})
	if err == nil {
		t.Fatal("Save() into read-only dir succeeded, want error")
	}

	_ = os.Chmod(dir, 0o700)
	after, err := os.ReadFile(credentialsPath(dir))
	if err != nil {
		t.Fatalf("read after failed save: %v", err)
	}
	if string(before) != string(after) {
		t.Error("credentials.json changed despite failed Save()")
	}
}

// TestLoad_SurvivesLeftoverTmpFromAbruptKill (Y2): mirrors
// internal/identity's test of the same name. A process killed between
// os.CreateTemp and the final os.Rename in save() leaves a randomly-named
// ".credentials-*.json.tmp" file behind -- the deterministic proxy this
// task asks for in place of a real power cut. Load must never pick it up.
func TestLoad_SurvivesLeftoverTmpFromAbruptKill(t *testing.T) {
	dir := t.TempDir()
	want := Credentials{EdgeID: "edge-1", DeviceID: "device-1", Credential: "edg_live_secret", CredentialVersion: 1, EnrolledAt: time.Now().UTC().Truncate(time.Second)}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before, err := os.ReadFile(credentialsPath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	leftover, err := os.CreateTemp(dir, ".credentials-*.json.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := leftover.WriteString(`{"edge_id":"garbage`); err != nil {
		t.Fatal(err)
	}
	leftover.Close()

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() after leftover tmp file: %v", err)
	}
	if got.Credential != want.Credential || got.EdgeID != want.EdgeID {
		t.Errorf("got = %+v, want %+v", got, want)
	}
	after, err := os.ReadFile(credentialsPath(dir))
	if err != nil {
		t.Fatalf("read after leftover tmp file: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("credentials.json content changed:\nbefore: %s\nafter:  %s", before, after)
	}
}
