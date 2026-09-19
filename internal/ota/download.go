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
	"regexp"
	"strings"
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

// defaultAllowedArtifactHosts are the only hosts FileDownloader will ever
// contact for artifact bytes: github.com itself (the SaaS-issued initial
// URL) and GitHub's release-asset redirect targets. This is deliberately
// NOT a general arbitrary-HTTPS downloader.
var defaultAllowedArtifactHosts = map[string]bool{
	"github.com":                            true,
	"objects.githubusercontent.com":         true,
	"github-releases.githubusercontent.com": true,
	"release-assets.githubusercontent.com":  true,
}

// FileDownloader is the production Downloader: GitHub Releases as the
// artifact host, Ed25519 + SHA-256 verification, atomic filesystem
// staging.
//
// # Security
//
// httpClient here is entirely separate from the Edge<->SaaS
// transport.Client: it never sets Authorization or X-Device-Id. Every
// request -- the initial, SaaS-issued URL AND every redirect target --
// goes through validateArtifactURL: HTTPS only, host must be on the
// GitHub-Releases allowlist, and the resolved address must not be
// private/loopback/link-local/unspecified. A redirect off the allowlist,
// or to a private network, is refused before it is ever followed.
type FileDownloader struct {
	dataDir      string
	architecture string
	version      string
	pubKey       ed25519.PublicKey
	logger       *slog.Logger
	httpClient   *http.Client

	allowedHosts map[string]bool
	// skipHostSafetyForTest disables the private/loopback-address
	// rejection in validateArtifactURL. Set ONLY by this package's own
	// tests (which must talk to an httptest server bound to 127.0.0.1);
	// production wiring (internal/agent) never touches this field, so it
	// is always false outside internal/ota's test binary.
	skipHostSafetyForTest bool
}

// NewFileDownloader builds a FileDownloader. pubKey is the Ed25519 public
// key used to verify SHA256SUMS.sig (see GEOCAM_OTA_PUBLIC_KEY_FILE).
// architecture is this Edge's own arch (amd64|arm64); currentVersion is
// internal/agent.Version -- the single source of truth T1 reuses.
func NewFileDownloader(dataDir string, pubKey ed25519.PublicKey, currentVersion, architecture string, logger *slog.Logger) *FileDownloader {
	if logger == nil {
		logger = slog.Default()
	}
	d := &FileDownloader{
		dataDir:      dataDir,
		architecture: architecture,
		version:      currentVersion,
		pubKey:       pubKey,
		logger:       logger,
		allowedHosts: defaultAllowedArtifactHosts,
	}
	d.httpClient = &http.Client{
		Timeout:       artifactHTTPTimeout,
		CheckRedirect: d.checkRedirect,
	}
	return d
}

// checkRedirect is http.Client.CheckRedirect: a redirect target is
// controlled by whatever server answered the previous request, so it gets
// exactly the same validation as the initial request -- see
// validateArtifactURL.
func (d *FileDownloader) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("ota: too many redirects")
	}
	return d.validateArtifactURL(req.URL.String())
}

// validateArtifactURL enforces HTTPS, restricts the host to the GitHub
// Releases allowlist, and (unless skipHostSafetyForTest) rejects any host
// that resolves to a private, loopback, link-local or unspecified
// address. Applied to BOTH the initial artifact/SHA256SUMS/signature URLs
// and every redirect target -- defense in depth, not redirect-only.
func (d *FileDownloader) validateArtifactURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("ota: invalid artifact URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("ota: artifact URL must use https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("ota: artifact URL has no host")
	}
	if !d.allowedHosts[strings.ToLower(host)] {
		return fmt.Errorf("ota: artifact host %q is not an allowed GitHub Releases host, rejected", host)
	}
	if d.skipHostSafetyForTest {
		return nil
	}
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

// releaseIDPattern is the closed rule for release_id: non-empty, ASCII
// alnum/dot/dash/underscore only, must start and end alphanumeric, at most
// 64 characters. No "/", no "\", no way to encode a path segment.
var releaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)

// validateReleaseID rejects anything that is not a bare, closed-alphabet
// identifier BEFORE it is ever used to build a filesystem path. release_id
// arrives from the SaaS-authenticated /ota/next response, which this
// package still treats as untrusted input for path-construction purposes.
func validateReleaseID(id string) error {
	if id == "" {
		return errors.New("ota: release_id is empty")
	}
	if len(id) > 64 {
		return fmt.Errorf("ota: release_id %q exceeds 64 characters", id)
	}
	if !releaseIDPattern.MatchString(id) {
		return fmt.Errorf("ota: release_id %q contains disallowed characters", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("ota: release_id %q must not contain '..'", id)
	}
	return nil
}

// artifactNamePattern is the closed rule for the artifact's own filename
// (taken from the SaaS-issued ArtifactURL's basename): a bare .tar.gz
// filename, no path separators.
var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.tar\.gz$`)

func validateArtifactName(name string) error {
	if name == "" {
		return errors.New("ota: artifact name is empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("ota: artifact name %q must not contain a path separator", name)
	}
	if !artifactNamePattern.MatchString(name) {
		return fmt.Errorf("ota: artifact name %q is not a valid .tar.gz filename", name)
	}
	return nil
}

// pendingDir validates release_id and returns
// DataDir/ota/pending/<release-id>, additionally confirming -- belt and
// suspenders on top of releaseIDPattern -- that the resolved path stays
// strictly inside DataDir/ota/pending. No MkdirAll/Stat/RemoveAll/Rename
// or write ever happens on a path derived from an unvalidated release_id:
// every caller in this file goes through this function first.
func (d *FileDownloader) pendingDir(id string) (string, error) {
	if err := validateReleaseID(id); err != nil {
		return "", err
	}
	root := filepath.Clean(filepath.Join(d.otaDir(), "pending"))
	dest := filepath.Clean(filepath.Join(root, id))
	if dest != root && !strings.HasPrefix(dest, root+string(filepath.Separator)) {
		return "", fmt.Errorf("ota: resolved release path %q escapes %q", dest, root)
	}
	return dest, nil
}

func (d *FileDownloader) applyRequestPath() string {
	return filepath.Join(d.otaDir(), "apply.request")
}

// ReleaseMetadata is the record staged alongside a downloaded release
// (DataDir/ota/pending/<release-id>/metadata.json). ArtifactName is the
// authoritative link between the locally staged file and the entry it
// must match inside SHA256SUMS -- see VerifyReleaseDir, the single
// reusable verifier both this package and `geocam-edge ota verify
// --artifact-dir` (and IA2's privileged updater) run against it.
type ReleaseMetadata struct {
	ReleaseID    string    `json:"release_id"`
	Version      string    `json:"version"`
	Architecture string    `json:"architecture"`
	ArtifactName string    `json:"artifact_name"`
	StagedAt     time.Time `json:"staged_at"`
}

// LoadReleaseMetadata reads and validates dir/metadata.json.
func LoadReleaseMetadata(dir string) (ReleaseMetadata, error) {
	var m ReleaseMetadata
	data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return m, fmt.Errorf("ota: read metadata.json: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("ota: decode metadata.json: %w", err)
	}
	if m.ReleaseID == "" || m.Version == "" || m.Architecture == "" || m.ArtifactName == "" {
		return m, errors.New("ota: metadata.json is missing required fields")
	}
	return m, nil
}

// FetchAndStage downloads, verifies (T4, via VerifyReleaseDir -- the same
// function `geocam-edge ota verify --artifact-dir` and IA2's privileged
// updater reuse) and atomically stages one release under
// DataDir/ota/pending/<release-id>/, then writes DataDir/ota/apply.request
// LAST. It never executes install/update/rollback scripts and never
// touches systemd -- writing apply.request is its only side effect
// visible outside DataDir.
//
// Idempotent across restarts: if this release was already downloaded and
// verified (its pending dir carries a "verified.ok" marker written only
// after VerifyReleaseDir passed), it is not re-downloaded -- apply.request
// is simply re-affirmed.
func (d *FileDownloader) FetchAndStage(ctx context.Context, release *transport.OTARelease) error {
	if release.Architecture != d.architecture {
		return fmt.Errorf("ota: release architecture %q does not match this edge's %q", release.Architecture, d.architecture)
	}

	dest, err := d.pendingDir(release.ReleaseID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dest, "verified.ok")); err == nil {
		d.logger.Info("ota: release already downloaded and verified, re-affirming apply.request",
			slog.String("release_id", release.ReleaseID))
		return d.writeApplyRequest(release.ReleaseID)
	}

	artifactName := filepath.Base(release.ArtifactURL)
	if err := validateArtifactName(artifactName); err != nil {
		return fmt.Errorf("ota: %w", err)
	}

	if err := os.MkdirAll(d.otaDir(), 0o755); err != nil {
		return fmt.Errorf("ota: create ota data dir: %w", err)
	}
	scratch, err := os.MkdirTemp(d.otaDir(), "staging-*")
	if err != nil {
		return fmt.Errorf("ota: create staging dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	artifactPath := filepath.Join(scratch, artifactName)
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

	metadata := ReleaseMetadata{
		ReleaseID:    release.ReleaseID,
		Version:      release.Version,
		Architecture: release.Architecture,
		ArtifactName: artifactName,
		StagedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(filepath.Join(scratch, "metadata.json"), metadata); err != nil {
		return fmt.Errorf("ota: write metadata: %w", err)
	}

	// The single reusable T4 pipeline -- fail-closed, no checksum-only
	// fallback -- run here exactly as `geocam-edge ota verify
	// --artifact-dir` and IA2's privileged updater run it against a
	// staged/snapshotted copy of this same directory.
	if err := VerifyReleaseDir(scratch, d.pubKey, d.version); err != nil {
		return err
	}

	// verified.ok is written last, inside scratch, before the atomic
	// rename below -- its presence in the FINAL pending dir is what the
	// idempotency check above trusts, so it must never exist before
	// VerifyReleaseDir has already passed.
	if err := os.WriteFile(filepath.Join(scratch, "verified.ok"), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("ota: write verified marker: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("ota: create pending dir: %w", err)
	}
	_ = os.RemoveAll(dest) // stale partial dir from a crashed prior attempt, if any -- dest is always validateReleaseID-derived, never raw input
	if err := os.Rename(scratch, dest); err != nil {
		return fmt.Errorf("ota: stage release dir: %w", err)
	}

	return d.writeApplyRequest(release.ReleaseID)
}

// writeApplyRequest atomically creates DataDir/ota/apply.request (temp
// file + rename, same pattern as internal/credentials.Save), and only
// ever as the LAST step after a release is fully downloaded and verified.
// releaseID is written as file CONTENT only, never as part of a path, so
// it needs no separate validation here (see validateReleaseID for the
// path-construction boundary).
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
	if err := d.validateArtifactURL(rawURL); err != nil {
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
