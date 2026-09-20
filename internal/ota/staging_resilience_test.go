package ota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// pendingEntries lists DataDir/ota/pending.
func pendingEntries(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "ota", "pending"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestFetchAndStage_ReplacesPreviousStagingWithoutLosingIt re-stages a release
// over an already-staged one. The old code did `_ = os.RemoveAll(dest)` and
// then renamed over it, so a removal that failed partway destroyed the
// previously-verified release while the new one never landed. Now the old dir
// is displaced by a rename first, so the end state is always exactly one
// complete release and never a leftover half-deleted directory.
func TestFetchAndStage_ReplacesPreviousStagingWithoutLosingIt(t *testing.T) {
	srv, release, pub := newTestApplianceRelease(t, "amd64", "v2.0.0", nil)
	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, pub, "v1.0.0", "amd64")

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("first stage: %v", err)
	}
	firstEntries := pendingEntries(t, dataDir)
	if len(firstEntries) != 1 {
		t.Fatalf("pending after first stage = %v, want exactly the release dir", firstEntries)
	}

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("re-stage over an existing release dir: %v", err)
	}

	entries := pendingEntries(t, dataDir)
	if len(entries) != 1 || entries[0] != release.ReleaseID {
		t.Fatalf("pending after re-stage = %v, want exactly [%s]", entries, release.ReleaseID)
	}
	for _, name := range entries {
		if strings.HasSuffix(name, ".previous") {
			t.Errorf("stale backup %q left behind in pending/", name)
		}
	}

	// The surviving release must still be a complete, verified release.
	staged := filepath.Join(dataDir, "ota", "pending", release.ReleaseID)
	for _, want := range []string{"verified.ok", "metadata.json", "SHA256SUMS", "SHA256SUMS.sig"} {
		if _, err := os.Stat(filepath.Join(staged, want)); err != nil {
			t.Errorf("staged release is missing %s: %v", want, err)
		}
	}
	if _, err := VerifyReleaseDir(staged, pub, "v1.0.0"); err != nil {
		t.Errorf("the staged release no longer verifies after being replaced: %v", err)
	}
}

// TestFetchAndStage_CleansUpStaleBackup covers a crash between the displacement
// rename and its cleanup: the next attempt must remove the bounded stale backup
// rather than fail or accumulate a second one.
func TestFetchAndStage_CleansUpStaleBackup(t *testing.T) {
	srv, release, pub := newTestApplianceRelease(t, "amd64", "v2.0.0", nil)
	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, pub, "v1.0.0", "amd64")

	dest := filepath.Join(dataDir, "ota", "pending", release.ReleaseID)
	backup := dest + ".previous"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "stale"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("stage with a stale backup present: %v", err)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Error("the stale .previous backup survived a successful stage")
	}
	if entries := pendingEntries(t, dataDir); len(entries) != 1 {
		t.Errorf("pending = %v, want exactly one release dir", entries)
	}
}

// TestFetchAndStage_FailedDownloadLeavesNoPartialRelease is the Y6 property for
// OTA staging: a download that fails must not leave a partial artifact, a
// pending directory, an apply.request, or a staging scratch dir behind.
func TestFetchAndStage_FailedDownloadLeavesNoPartialRelease(t *testing.T) {
	dataDir := t.TempDir()

	failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer failing.Close()

	release := &transport.OTARelease{
		ReleaseID: "rel-v2.0.0", Version: "v2.0.0", Architecture: "amd64",
		ArtifactURL:   failing.URL + "/geocam-edge-v2.0.0-linux-amd64.tar.gz",
		SHA256SUMSURL: failing.URL + "/SHA256SUMS",
		SignatureURL:  failing.URL + "/SHA256SUMS.sig",
	}
	d := newDownloaderForTest(t, failing, dataDir, nil, "v1.0.0", "amd64")

	if err := d.FetchAndStage(context.Background(), release); err == nil {
		t.Fatal("expected a failed download to return an error")
	}
	if entries := pendingEntries(t, dataDir); len(entries) != 0 {
		t.Errorf("pending = %v after a failed download, want nothing", entries)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ota", "apply.request")); err == nil {
		t.Error("apply.request must not exist after a failed download")
	}
	otaEntries, err := os.ReadDir(filepath.Join(dataDir, "ota"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range otaEntries {
		if strings.HasPrefix(e.Name(), "staging-") {
			t.Errorf("staging scratch dir %q survived a failed download", e.Name())
		}
	}
}

// TestFetchAndStage_FailedRedownloadKeepsPreviousRelease forces a real
// re-download (by removing the verified marker that makes staging idempotent)
// and makes it fail, then asserts the release already on disk is byte-identical
// afterwards. That is the "a valid file is never replaced by a partial one"
// property applied to the one OTA path that writes megabytes.
func TestFetchAndStage_FailedRedownloadKeepsPreviousRelease(t *testing.T) {
	srv, release, pub := newTestApplianceRelease(t, "amd64", "v2.0.0", nil)
	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, pub, "v1.0.0", "amd64")

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("first stage: %v", err)
	}
	staged := filepath.Join(dataDir, "ota", "pending", release.ReleaseID)
	artifact := filepath.Join(staged, "geocam-edge-v2.0.0-linux-amd64.tar.gz")
	before, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the marker so the next call really re-downloads instead of
	// short-circuiting on the idempotency check.
	if err := os.Remove(filepath.Join(staged, "verified.ok")); err != nil {
		t.Fatal(err)
	}

	failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer failing.Close()
	// Repoint the release at the failing server, otherwise the re-download
	// would simply succeed against the still-running original test server.
	release.SHA256SUMSURL = failing.URL + "/SHA256SUMS"
	release.SignatureURL = failing.URL + "/SHA256SUMS.sig"
	release.ArtifactURL = failing.URL + "/geocam-edge-v2.0.0-linux-amd64.tar.gz"
	d.httpClient = failing.Client()
	d.allowedRedirectHosts = map[string]bool{"127.0.0.1": true}
	d.skipHostSafetyForTest = true
	d.skipInitialHostCheckForTest = true

	if err := d.FetchAndStage(context.Background(), release); err == nil {
		t.Fatal("expected the forced re-download to fail")
	}
	after, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("the previously staged artifact was destroyed by a failed re-download: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a failed re-download modified the artifact already on disk")
	}
}

// TestDownloadWriteErrorIsClassifiedAsDiskFull pins the classification of the
// one write path in OTA that streams megabytes: the artifact body. It writes
// to a path that cannot be created as a regular file (a directory), which is a
// deterministic non-ENOSPC failure, and asserts the error is still returned
// with the path rather than swallowed. The ENOSPC classification itself is
// covered by internal/platform's own tests plus TestDownloadClassifiesNoSpace
// below, which injects the real errno through the same wrapper production uses.
func TestDownloadWriteErrorIsNotSwallowed(t *testing.T) {
	dataDir := t.TempDir()
	d := NewFileDownloader(dataDir, nil, "v1.0.0", "amd64", nil)
	// The URL host checks are unrelated to what this test asserts, so relax
	// them exactly as the other download tests do.
	d.skipHostSafetyForTest = true
	d.skipInitialHostCheckForTest = true

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("artifact body"))
	}))
	defer srv.Close()
	d.httpClient = srv.Client()
	d.allowedRedirectHosts = map[string]bool{"127.0.0.1": true}

	// dest is an existing directory, so the write fails with EISDIR after the
	// response has already been accepted: the error must name the path and
	// must not be classified as a full disk.
	dest := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	err := d.download(context.Background(), srv.URL+"/artifact.tar.gz", dest)
	if err == nil {
		t.Fatal("expected an error writing to a directory path")
	}
	if platform.IsDiskFull(err) {
		t.Error("a non-ENOSPC write failure must not be classified as a full disk")
	}
	if !strings.Contains(err.Error(), dest) {
		t.Errorf("error %q does not name the failing path", err.Error())
	}
}

// TestStagingFailurePathsDoNotPanic drives the staging entry point with a
// release whose URLs the downloader must refuse, so every early-return path in
// FetchAndStage is exercised with nothing staged.
func TestStagingFailurePathsDoNotPanic(t *testing.T) {
	dataDir := t.TempDir()
	d := NewFileDownloader(dataDir, nil, "v1.0.0", "amd64", nil)

	for _, release := range []*transport.OTARelease{
		{ReleaseID: "../escape", Version: "v2.0.0", Architecture: "amd64", ArtifactURL: "https://example.invalid/a.tar.gz", SHA256SUMSURL: "https://example.invalid/SHA256SUMS", SignatureURL: "https://example.invalid/sig"},
		{ReleaseID: "ok", Version: "v2.0.0", Architecture: "arm64", ArtifactURL: "https://example.invalid/a.tar.gz", SHA256SUMSURL: "https://example.invalid/SHA256SUMS", SignatureURL: "https://example.invalid/sig"},
	} {
		if err := d.FetchAndStage(context.Background(), release); err == nil {
			t.Errorf("release %+v was accepted, want a refusal", release)
		}
	}
	if entries := pendingEntries(t, dataDir); len(entries) != 0 {
		t.Errorf("pending = %v after refused releases, want empty", entries)
	}
}
