package ota

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// artifactHTTPTimeout bounds one artifact download request -- generous
// relative to the small JSON calls elsewhere in this codebase because
// appliance tarballs are megabytes, not bytes.
const artifactHTTPTimeout = 5 * time.Minute

// Downloader fetches and verifies one release's artifacts, staging them
// atomically under DataDir/ota/pending only once verification succeeds.
type Downloader interface {
	FetchAndStage(ctx context.Context, release *transport.OTARelease) error
}

// FileDownloader is the production Downloader: GitHub Releases as the
// artifact host, Ed25519 + SHA-256 verification, atomic filesystem
// staging.
//
// # Security
//
// httpClient here is entirely separate from the Edge<->SaaS
// transport.Client: it never sets Authorization or X-Device-Id. The
// artifact host is an unrelated third party and must never see Edge
// credentials. CheckRedirect rejects any redirect target that is not
// https:// or that resolves to a private/loopback/link-local address,
// closing the SSRF path a follow-anything downloader would otherwise open.
// This is not a general arbitrary-URL downloader -- it fetches exactly the
// three URLs a release descriptor names.
type FileDownloader struct {
	dataDir      string
	architecture string
	version      string
	pubKey       ed25519.PublicKey
	logger       *slog.Logger
	httpClient   *http.Client
}

// NewFileDownloader builds a FileDownloader. pubKey is the Ed25519 public
// key used to verify SHA256SUMS.sig (see GEOCAM_OTA_PUBLIC_KEY_FILE).
// architecture is this Edge's own arch (amd64|arm64); currentVersion is
// internal/agent.Version -- the single source of truth T1 reuses.
func NewFileDownloader(dataDir string, pubKey ed25519.PublicKey, currentVersion, architecture string, logger *slog.Logger) *FileDownloader {
	if logger == nil {
		logger = slog.Default()
	}
	return &FileDownloader{
		dataDir:      dataDir,
		architecture: architecture,
		version:      currentVersion,
		pubKey:       pubKey,
		logger:       logger,
		httpClient: &http.Client{
			Timeout:       artifactHTTPTimeout,
			CheckRedirect: rejectUnsafeRedirect,
		},
	}
}

// rejectUnsafeRedirect is http.Client.CheckRedirect: redirect targets are
// controlled by whatever server answered the initial (trusted, SaaS-issued)
// URL, so -- unlike that initial request -- they get the full SSRF guard:
// HTTPS only, and never a private/loopback/link-local address.
func rejectUnsafeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("ota: too many redirects")
	}
	return validatePublicHTTPSURL(req.URL.String())
}

// requireHTTPS enforces HTTPS on the initial, SaaS-issued artifact/
// SHA256SUMS/signature URLs. It does not resolve or restrict the host --
// see validatePublicHTTPSURL for that, applied only to redirect targets.
func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("ota: invalid artifact URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("ota: artifact URL must use https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("ota: artifact URL has no host")
	}
	return nil
}

// validatePublicHTTPSURL enforces HTTPS and rejects any URL whose host
// resolves to a private, loopback, link-local or unspecified address.
func validatePublicHTTPSURL(raw string) error {
	if err := requireHTTPS(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("ota: resolve artifact host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateOrLocal(ip) {
			return fmt.Errorf("ota: artifact host %q resolves to a private/local address, rejected", host)
		}
	}
	return nil
}

func isPrivateOrLocal(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func (d *FileDownloader) otaDir() string { return filepath.Join(d.dataDir, "ota") }
func (d *FileDownloader) pendingDir(id string) string {
	return filepath.Join(d.otaDir(), "pending", id)
}
func (d *FileDownloader) applyRequestPath() string {
	return filepath.Join(d.otaDir(), "apply.request")
}

type releaseMetadata struct {
	ReleaseID    string    `json:"release_id"`
	Version      string    `json:"version"`
	Architecture string    `json:"architecture"`
	StagedAt     time.Time `json:"staged_at"`
}

// FetchAndStage downloads, verifies (T4, fail-closed, in order:
// signature -> checksum -> version -> layout) and atomically stages one
// release under DataDir/ota/pending/<release-id>/, then writes
// DataDir/ota/apply.request LAST. It never executes install/update/
// rollback scripts and never touches systemd -- writing apply.request is
// its only side effect visible outside DataDir, per the contract with
// IA2's privileged updater.
//
// Idempotent across restarts: if this release was already downloaded and
// verified (its pending dir carries a "verified.ok" marker written only
// after every check above passed), it is not re-downloaded -- apply.request
// is simply re-affirmed.
func (d *FileDownloader) FetchAndStage(ctx context.Context, release *transport.OTARelease) error {
	if release.Architecture != d.architecture {
		return fmt.Errorf("ota: release architecture %q does not match this edge's %q", release.Architecture, d.architecture)
	}

	dest := d.pendingDir(release.ReleaseID)
	if _, err := os.Stat(filepath.Join(dest, "verified.ok")); err == nil {
		d.logger.Info("ota: release already downloaded and verified, re-affirming apply.request",
			slog.String("release_id", release.ReleaseID))
		return d.writeApplyRequest(release.ReleaseID)
	}

	if err := os.MkdirAll(d.otaDir(), 0o755); err != nil {
		return fmt.Errorf("ota: create ota data dir: %w", err)
	}
	scratch, err := os.MkdirTemp(d.otaDir(), "staging-*")
	if err != nil {
		return fmt.Errorf("ota: create staging dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	artifactName := filepath.Base(release.ArtifactURL)
	artifactPath := filepath.Join(scratch, "artifact.tar.gz")
	sumsPath := filepath.Join(scratch, "SHA256SUMS")
	sigPath := filepath.Join(scratch, "SHA256SUMS.sig")

	if err := d.download(ctx, release.SHA256SUMSURL, sumsPath); err != nil {
		return fmt.Errorf("ota: download SHA256SUMS: %w", err)
	}
	if err := d.download(ctx, release.SignatureURL, sigPath); err != nil {
		return fmt.Errorf("ota: download SHA256SUMS.sig: %w", err)
	}
	if err := d.download(ctx, release.ArtifactURL, artifactPath); err != nil {
		return fmt.Errorf("ota: download artifact: %w", err)
	}

	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("ota: read SHA256SUMS: %w", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("ota: read SHA256SUMS.sig: %w", err)
	}

	// Fail-closed verification order (T4): signature over the manifest
	// first, then the artifact's hash against that now-trusted manifest,
	// then the forward-update rule, then architecture/layout. No
	// checksum-only path exists at any point in this sequence.
	if err := VerifyManifestSignature(sums, sig, d.pubKey); err != nil {
		return fmt.Errorf("ota: manifest signature: %w", err)
	}
	if err := VerifyArtifactChecksum(artifactPath, sums, artifactName); err != nil {
		return fmt.Errorf("ota: artifact checksum: %w", err)
	}
	if err := VerifyEligible(d.version, release.Version); err != nil {
		return fmt.Errorf("ota: version check: %w", err)
	}
	if err := VerifyArchiveLayout(artifactPath, release.Architecture); err != nil {
		return fmt.Errorf("ota: artifact layout: %w", err)
	}

	metadata := releaseMetadata{
		ReleaseID:    release.ReleaseID,
		Version:      release.Version,
		Architecture: release.Architecture,
		StagedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(filepath.Join(scratch, "metadata.json"), metadata); err != nil {
		return fmt.Errorf("ota: write metadata: %w", err)
	}
	// verified.ok is written last, inside scratch, before the atomic
	// rename below -- its presence in the FINAL pending dir is what the
	// idempotency check above trusts, so it must never exist before every
	// verification step above has already passed.
	if err := os.WriteFile(filepath.Join(scratch, "verified.ok"), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("ota: write verified marker: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("ota: create pending dir: %w", err)
	}
	_ = os.RemoveAll(dest) // stale partial dir from a crashed prior attempt, if any
	if err := os.Rename(scratch, dest); err != nil {
		return fmt.Errorf("ota: stage release dir: %w", err)
	}

	return d.writeApplyRequest(release.ReleaseID)
}

// writeApplyRequest atomically creates DataDir/ota/apply.request (temp
// file + rename, same pattern as internal/credentials.Save), and only
// ever as the LAST step after a release is fully downloaded and verified.
func (d *FileDownloader) writeApplyRequest(releaseID string) error {
	path := d.applyRequestPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(releaseID+"\n"), 0o644); err != nil {
		return fmt.Errorf("ota: write apply.request: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("ota: rename apply.request: %w", err)
	}
	return nil
}

// download GETs rawURL with NO Edge credentials and writes the body to
// dest.
func (d *FileDownloader) download(ctx context.Context, rawURL, dest string) error {
	if err := requireHTTPS(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("ota: build request: %w", err)
	}
	// Deliberately no Authorization, no X-Device-Id: this client and this
	// request never carry Edge credentials to a third-party artifact host.
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ota: request %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ota: unexpected status %d fetching %s", resp.StatusCode, rawURL)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("ota: create %s: %w", dest, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("ota: write %s: %w", dest, err)
	}
	return nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
