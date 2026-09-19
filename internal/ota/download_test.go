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
// without disabling the download path's own security checks.
func newDownloaderForTest(t *testing.T, srv *httptest.Server, dataDir string, pub ed25519.PublicKey, currentVersion, arch string) *FileDownloader {
	t.Helper()
	d := NewFileDownloader(dataDir, pub, currentVersion, arch, nil)
	d.httpClient = srv.Client()
	d.httpClient.CheckRedirect = rejectUnsafeRedirect
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
