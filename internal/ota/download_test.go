package ota

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newTestApplianceRelease starts a TLS test server that serves a minimal
// valid appliance artifact + SHA256SUMS + signature under it, and returns
// the *transport.OTARelease pointing at it plus the Ed25519 public key
// that validates the signature.
func newTestApplianceRelease(t *testing.T, arch, version string, capturedHeaders *http.Header) (*httptest.Server, *transport.OTARelease, ed25519.PublicKey) {
	t.Helper()

	dir := t.TempDir()
	artifactName := "geocam-edge-" + version + "-linux-" + arch + ".tar.gz"
	artifactPath := filepath.Join(dir, artifactName)
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "fake binary bytes",
		"VERSION":     version,
		"ARCH":        arch,
	})
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read built artifact: %v", err)
	}
	sum := sha256.Sum256(artifactBytes)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  " + artifactName + "\n")

	pub, priv, _ := ed25519.GenerateKey(nil)
	sig := SignManifest(priv, manifest)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+artifactName, func(w http.ResponseWriter, r *http.Request) {
		if capturedHeaders != nil {
			*capturedHeaders = r.Header.Clone()
		}
		w.Write(artifactBytes)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		if capturedHeaders != nil {
			*capturedHeaders = r.Header.Clone()
		}
		w.Write(manifest)
	})
	mux.HandleFunc("/SHA256SUMS.sig", func(w http.ResponseWriter, r *http.Request) {
		if capturedHeaders != nil {
			*capturedHeaders = r.Header.Clone()
		}
		w.Write(sig)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	release := &transport.OTARelease{
		ReleaseID:     "rel-" + version,
		Version:       version,
		Architecture:  arch,
		ArtifactURL:   srv.URL + "/" + artifactName,
		SHA256SUMSURL: srv.URL + "/SHA256SUMS",
		SignatureURL:  srv.URL + "/SHA256SUMS.sig",
	}
	return srv, release, pub
}

// newDownloaderForTest builds a FileDownloader whose httpClient trusts the
// given test server's TLS certificate, so it can talk to httptest.NewTLSServer
// without disabling the download path's own security-of-CONTENT checks
// (signature/checksum/binding). It DOES relax both host-safety checks
// (official-path restriction on the initial URL, private-IP rejection) to
// accept the test server's own 127.0.0.1 address -- production wiring
// (internal/agent) never does this; see TestValidateInitialArtifactURL_*
// and TestValidateRedirectURL_* for those checks exercised against the
// real, unrelaxed defaults.
func newDownloaderForTest(t *testing.T, srv *httptest.Server, dataDir string, pub ed25519.PublicKey, currentVersion, arch string) *FileDownloader {
	t.Helper()
	d := NewFileDownloader(dataDir, pub, currentVersion, arch, nil)
	d.allowedRedirectHosts = map[string]bool{"127.0.0.1": true}
	d.skipHostSafetyForTest = true
	d.skipInitialHostCheckForTest = true
	d.httpClient = srv.Client()
	d.httpClient.CheckRedirect = d.checkRedirect
	return d
}

func TestFetchAndStage_Success(t *testing.T) {
	var headers http.Header
	srv, release, pub := newTestApplianceRelease(t, "amd64", "v2.0.0", &headers)
	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, pub, "v1.0.0", "amd64")

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("FetchAndStage: %v", err)
	}

	applyReq := filepath.Join(dataDir, "ota", "apply.request")
	if _, err := os.Stat(applyReq); err != nil {
		t.Fatalf("expected apply.request to exist after successful staging: %v", err)
	}
	verifiedMarker := filepath.Join(dataDir, "ota", "pending", release.ReleaseID, "verified.ok")
	if _, err := os.Stat(verifiedMarker); err != nil {
		t.Fatalf("expected verified.ok marker in staged pending dir: %v", err)
	}
}

// TestFetchAndStage_NoEdgeCredentialsSent is the mandated "downloader no
// envía Edge credential a artifact host" check: the artifact HTTP client
// must never set Authorization or X-Device-Id, even though a real Edge
// always has both for its SaaS transport.
func TestFetchAndStage_NoEdgeCredentialsSent(t *testing.T) {
	var headers http.Header
	srv, release, pub := newTestApplianceRelease(t, "arm64", "v3.0.0", &headers)
	d := newDownloaderForTest(t, srv, t.TempDir(), pub, "v1.0.0", "arm64")

	if err := d.FetchAndStage(context.Background(), release); err != nil {
		t.Fatalf("FetchAndStage: %v", err)
	}

	if headers.Get("Authorization") != "" {
		t.Errorf("artifact host received an Authorization header, must never see Edge credentials: %q", headers.Get("Authorization"))
	}
	if headers.Get("X-Device-Id") != "" {
		t.Errorf("artifact host received an X-Device-Id header, must never see Edge identity: %q", headers.Get("X-Device-Id"))
	}
}

// TestFetchAndStage_WrongArchitecture verifies a release descriptor whose
// architecture does not match this Edge's own is rejected before any
// download happens.
func TestFetchAndStage_WrongArchitecture(t *testing.T) {
	_, release, pub := newTestApplianceRelease(t, "arm64", "v2.0.0", nil)
	d := NewFileDownloader(t.TempDir(), pub, "v1.0.0", "amd64", nil) // this edge is amd64

	if err := d.FetchAndStage(context.Background(), release); err == nil {
		t.Fatal("expected architecture mismatch to be rejected")
	}
}

// TestFetchAndStage_MissingSignatureRejects verifies fail-closed behavior:
// a nil/empty public key rejects every release, with no checksum-only
// fallback.
func TestFetchAndStage_MissingSignatureRejects(t *testing.T) {
	srv, release, _ := newTestApplianceRelease(t, "amd64", "v2.0.0", nil)
	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, nil /* no public key configured */, "v1.0.0", "amd64")

	err := d.FetchAndStage(context.Background(), release)
	if err == nil {
		t.Fatal("expected missing public key to reject the release (fail closed)")
	}

	// Atomic staging: apply.request must not exist when verification never
	// completed.
	if _, statErr := os.Stat(filepath.Join(dataDir, "ota", "apply.request")); statErr == nil {
		t.Fatal("apply.request must not exist before verification completes")
	}
}

// TestFetchAndStage_AtomicStaging checks that a tampered SHA256SUMS
// (signature no longer matches) never results in apply.request or a
// verified.ok marker -- nothing partially verified is ever exposed to the
// privileged updater's contract.
func TestFetchAndStage_AtomicStaging(t *testing.T) {
	dir := t.TempDir()
	artifactName := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	artifactPath := filepath.Join(dir, artifactName)
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary", "VERSION": "v2.0.0", "ARCH": "amd64",
	})
	artifactBytes, _ := os.ReadFile(artifactPath)
	sum := sha256.Sum256(artifactBytes)
	correctManifest := []byte(hex.EncodeToString(sum[:]) + "  " + artifactName + "\n")
	tamperedManifest := []byte("0000000000000000000000000000000000000000000000000000000000000000  " + artifactName + "\n")

	pub, priv, _ := ed25519.GenerateKey(nil)
	// Sign the CORRECT manifest, but serve the TAMPERED one -- simulates a
	// manifest modified in transit/at rest after signing.
	sig := SignManifest(priv, correctManifest)

	mux := http.NewServeMux()
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, r *http.Request) { w.Write(artifactBytes) })
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write(tamperedManifest) })
	mux.HandleFunc("/SHA256SUMS.sig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig) })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	release := &transport.OTARelease{
		ReleaseID: "rel-tampered", Version: "v2.0.0", Architecture: "amd64",
		ArtifactURL:   srv.URL + "/artifact.tar.gz",
		SHA256SUMSURL: srv.URL + "/SHA256SUMS",
		SignatureURL:  srv.URL + "/SHA256SUMS.sig",
	}

	dataDir := t.TempDir()
	d := newDownloaderForTest(t, srv, dataDir, pub, "v1.0.0", "amd64")

	if err := d.FetchAndStage(context.Background(), release); err == nil {
		t.Fatal("expected tampered manifest to fail signature verification")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ota", "apply.request")); err == nil {
		t.Fatal("apply.request must not exist when the manifest signature check failed")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ota", "pending", release.ReleaseID)); err == nil {
		t.Fatal("no pending release dir should be staged when verification failed")
	}
}

// --- BLOCKER 1: release_id path traversal -------------------------------

func TestValidateReleaseID_RejectsTraversalAndInvalidForms(t *testing.T) {
	bad := []string{
		"",
		"../../credentials.json",
		"..",
		"a/b",
		`a\b`,
		"a/../../etc/passwd",
		"-leadingdash",
		"trailingdash-",
		".leadingdot",
		"trailingdot.",
		"a..b",
		strings.Repeat("a", 65),
	}
	for _, id := range bad {
		if err := validateReleaseID(id); err == nil {
			t.Errorf("validateReleaseID(%q) = nil, want error", id)
		}
	}

	good := []string{"rel-1", "a", strings.Repeat("a", 64), "rel_2024.01.02"}
	for _, id := range good {
		if err := validateReleaseID(id); err != nil {
			t.Errorf("validateReleaseID(%q) unexpected error: %v", id, err)
		}
	}
}

// TestFetchAndStage_RejectsPathTraversalReleaseID is BLOCKER 1's exact
// regression: a malicious release_id must be rejected before ANY
// filesystem operation, and must never be able to escape
// DataDir/ota/pending to reach a neighboring file.
func TestFetchAndStage_RejectsPathTraversalReleaseID(t *testing.T) {
	dataDir := t.TempDir()
	sentinel := filepath.Join(dataDir, "credentials.json")
	if err := os.WriteFile(sentinel, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	for _, maliciousID := range []string{"../../credentials.json", "..", "a/../../credentials.json"} {
		d := NewFileDownloader(dataDir, nil, "v1.0.0", "amd64", nil)
		release := &transport.OTARelease{
			ReleaseID: maliciousID, Version: "v2.0.0", Architecture: "amd64",
			ArtifactURL:   "https://github.com/drko-dev/monitoreoedgeis/releases/download/v2.0.0/x.tar.gz",
			SHA256SUMSURL: "https://github.com/drko-dev/monitoreoedgeis/releases/download/v2.0.0/SHA256SUMS",
			SignatureURL:  "https://github.com/drko-dev/monitoreoedgeis/releases/download/v2.0.0/SHA256SUMS.sig",
		}
		if err := d.FetchAndStage(context.Background(), release); err == nil {
			t.Errorf("release_id %q: expected rejection, got nil error", maliciousID)
		}
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel file was removed by a path-traversal release_id: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("sentinel file was modified by a path-traversal release_id: %q", got)
	}
	entries, _ := os.ReadDir(dataDir)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	if len(entries) != 1 || names[0] != "credentials.json" {
		t.Fatalf("unexpected files created directly under DataDir: %v", names)
	}
}

// --- BLOCKER 2: SSRF / arbitrary host, and arbitrary repo ----------------

func TestValidateInitialArtifactURL_RejectsNonHTTPS(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	u := "http://github.com/" + officialOwnerRepo + "/releases/download/v1.0.0/x.tar.gz"
	if err := d.validateInitialArtifactURL(u); err == nil {
		t.Fatal("expected a non-https initial URL to be rejected")
	}
}

func TestValidateInitialArtifactURL_RejectsLoopback(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	if err := d.validateInitialArtifactURL("https://127.0.0.1/x"); err == nil {
		t.Fatal("expected the INITIAL URL to reject a loopback host")
	}
}

func TestValidateInitialArtifactURL_RejectsPrivateRFC1918(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	if err := d.validateInitialArtifactURL("https://10.1.2.3/x"); err == nil {
		t.Fatal("expected the INITIAL URL to reject an RFC1918 private host")
	}
}

// TestValidateInitialArtifactURL_RejectsOtherOwnerRepo is BLOCKER 2's core
// regression: github.com alone is not enough -- a descriptor pointing at
// a DIFFERENT owner/repo's release must be rejected even though the host
// is genuinely github.com.
func TestValidateInitialArtifactURL_RejectsOtherOwnerRepo(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	d.skipHostSafetyForTest = true
	if err := d.validateInitialArtifactURL("https://github.com/some-other-owner/some-other-repo/releases/download/v1.0.0/x.tar.gz"); err == nil {
		t.Fatal("expected a different owner/repo's release URL to be rejected")
	}
}

func TestValidateInitialArtifactURL_RejectsNonReleasePath(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	d.skipHostSafetyForTest = true
	for _, u := range []string{
		"https://github.com/" + officialOwnerRepo, // repo root, not a release asset
		"https://github.com/" + officialOwnerRepo + "/archive/refs/heads/main.zip",
		"https://github.com/" + officialOwnerRepo + "/issues/1",
	} {
		if err := d.validateInitialArtifactURL(u); err == nil {
			t.Errorf("expected non-release-download path %q to be rejected", u)
		}
	}
}

// TestValidateInitialArtifactURL_AcceptsOfficialReleasePath isolates the
// host/path decision from a real DNS lookup (skipHostSafetyForTest) so
// this test stays hermetic/offline.
func TestValidateInitialArtifactURL_AcceptsOfficialReleasePath(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	d.skipHostSafetyForTest = true
	u := "https://github.com/" + officialOwnerRepo + "/releases/download/v1.0.0/geocam-edge-v1.0.0-linux-amd64.tar.gz"
	if err := d.validateInitialArtifactURL(u); err != nil {
		t.Fatalf("expected the official release-download path to be accepted, got: %v", err)
	}
}

// TestFetchAndStage_RejectsDisallowedInitialHost is BLOCKER 2's exact
// regression at the FetchAndStage level: a release descriptor pointing
// outside the official release path must be rejected on the INITIAL
// request, not only on a redirect.
func TestFetchAndStage_RejectsDisallowedInitialHost(t *testing.T) {
	for name, release := range map[string]*transport.OTARelease{
		"different host": {
			ReleaseID: "rel-1", Version: "v2.0.0", Architecture: "amd64",
			ArtifactURL:   "https://evil.example.com/artifact.tar.gz",
			SHA256SUMSURL: "https://evil.example.com/SHA256SUMS",
			SignatureURL:  "https://evil.example.com/SHA256SUMS.sig",
		},
		"different owner/repo on github.com": {
			ReleaseID: "rel-2", Version: "v2.0.0", Architecture: "amd64",
			ArtifactURL:   "https://github.com/some-other-owner/some-other-repo/releases/download/v2.0.0/artifact.tar.gz",
			SHA256SUMSURL: "https://github.com/some-other-owner/some-other-repo/releases/download/v2.0.0/SHA256SUMS",
			SignatureURL:  "https://github.com/some-other-owner/some-other-repo/releases/download/v2.0.0/SHA256SUMS.sig",
		},
	} {
		d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
		if err := d.FetchAndStage(context.Background(), release); err == nil {
			t.Errorf("%s: expected the initial artifact host/path to be rejected", name)
		}
	}
}

func TestValidateRedirectURL_AcceptsOfficialCDNHost(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	d.skipHostSafetyForTest = true
	if err := d.validateRedirectURL("https://objects.githubusercontent.com/x"); err != nil {
		t.Fatalf("expected the official release-asset CDN redirect host to be accepted, got: %v", err)
	}
}

func TestValidateRedirectURL_RejectsDisallowedHost(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	if err := d.validateRedirectURL("https://evil.example.com/x"); err == nil {
		t.Fatal("expected a redirect host outside the CDN allowlist to be rejected")
	}
}

func TestCheckRedirect_RejectsPrivateTarget(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	req, err := http.NewRequest(http.MethodGet, "https://10.0.0.5/evil", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := d.checkRedirect(req, nil); err == nil {
		t.Fatal("expected a redirect to a private address to be rejected")
	}
}

func TestCheckRedirect_RejectsTooManyHops(t *testing.T) {
	d := NewFileDownloader(t.TempDir(), nil, "v1.0.0", "amd64", nil)
	d.skipHostSafetyForTest = true
	req, err := http.NewRequest(http.MethodGet, "https://objects.githubusercontent.com/x", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	via := make([]*http.Request, 5)
	if err := d.checkRedirect(req, via); err == nil {
		t.Fatal("expected too many redirect hops to be rejected")
	}
}
