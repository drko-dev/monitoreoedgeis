package appliance_test

// Hito W — W9 (upgrade).
//
// Hito T already has focused tests for each verification step (internal/ota's
// unit tests) and for each shell step (install/update/rollback with fake
// verifier binaries). What was never exercised is the join between them: a
// candidate release signed with a real Ed25519 key, staged in the layout the
// daemon produces, verified by the *current* release's real `geocam-edge ota
// verify`, and then activated by the privileged updater — plus the failure path
// where verification does not pass and the previous release must stay exactly
// where it was.
//
// This is the "current version -> candidate -> signature/checksum verification
// -> activation" path from the Hito W brief, run entirely against a throwaway
// install root with no root privileges and no systemd. The readiness gate and
// its automatic rollback are covered by Hito T's
// TestUpdateReadinessFailureRollsBackAndPreservesData and are deliberately not
// duplicated here.
//
// No OTA mechanism is introduced: the test reuses install.sh, ota-updater.sh
// and update.sh exactly as they ship.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	applianceBinaryOnce sync.Once
	applianceBinaryPath string
	applianceBinaryErr  error
)

// buildApplianceBinary compiles the real agent once per test binary. The
// privileged updater executes the current release's own `geocam-edge ota
// verify`, so a shell stub cannot stand in for it when the test wants to
// exercise real signature verification.
func buildApplianceBinary(t *testing.T) string {
	t.Helper()
	applianceBinaryOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			applianceBinaryErr = fmt.Errorf("go toolchain not available: %w", err)
			return
		}
		dir, err := os.MkdirTemp("", "geocam-edge-appliance-bin-*")
		if err != nil {
			applianceBinaryErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		bin := filepath.Join(dir, "geocam-edge")
		out, err := exec.Command("go", "build", "-o", bin, "../../cmd/geocam-edge").CombinedOutput()
		if err != nil {
			applianceBinaryErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		applianceBinaryPath = bin
		_ = os.Chmod(bin, 0o755)
	})
	if applianceBinaryErr != nil {
		t.Skipf("cannot build the appliance binary: %v", applianceBinaryErr)
	}
	return applianceBinaryPath
}

// runApplianceScript runs one appliance script with every inherited GEOCAM_*
// removed, so a developer's dev shell cannot decide what this test exercises.
func runApplianceScript(t *testing.T, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(t), name)}, args...)...)
	base := make([]string, 0, len(os.Environ())+len(env))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GEOCAM_") {
			continue
		}
		base = append(base, kv)
	}
	cmd.Env = append(base, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stageSignedRelease writes a complete staged release directory in the exact
// shape `geocam-edge ota verify --artifact-dir` consumes, signing SHA256SUMS
// with priv.
func stageSignedRelease(t *testing.T, stagedDir, artifactPath, artifactName, version, arch string, priv ed25519.PrivateKey) {
	t.Helper()
	if err := os.MkdirAll(stagedDir, 0o700); err != nil {
		t.Fatal(err)
	}

	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedDir, artifactName), artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(artifactBytes)
	manifest := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), artifactName)
	manifestBytes := []byte(manifest)
	if err := os.WriteFile(filepath.Join(stagedDir, "SHA256SUMS"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedDir, "SHA256SUMS.sig"), ed25519.Sign(priv, manifestBytes), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata := fmt.Sprintf(
		`{"release_id":%q,"version":%q,"architecture":%q,"artifact_name":%q,"staged_at":%q}`,
		version, version, arch, artifactName, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(stagedDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildRealArtifact packs a candidate release that carries the *real* agent
// binary. That matters beyond realism: the privileged updater verifies every
// later candidate by running the currently active release's own
// `geocam-edge ota verify`, so a release holding a stub binary would turn the
// next verification round into a no-op.
func buildRealArtifact(t *testing.T, dir, version, arch, binaryPath string) string {
	t.Helper()

	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read %s: %v", binaryPath, err)
	}
	if err := os.WriteFile(filepath.Join(stage, "geocam-edge"), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	if arch != "" {
		if err := os.WriteFile(filepath.Join(stage, "ARCH"), []byte(arch), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(scriptsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(scriptsDir(t), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "scripts", e.Name()), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	artifact := filepath.Join(dir, "geocam-edge-"+version+".tar.gz")
	if out, err := exec.Command("tar", "czf", artifact, "-C", stage, ".").CombinedOutput(); err != nil {
		t.Fatalf("tar czf failed: %v\n%s", err, out)
	}
	return artifact
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	return target
}

// TestW9_PrivilegedUpdaterActivatesOnlyAProperlySignedRealRelease drives the
// whole upgrade path with real cryptography and then proves that a release
// which fails verification is not activated and cannot damage the running one.
func TestW9_PrivilegedUpdaterActivatesOnlyAProperlySignedRealRelease(t *testing.T) {
	requireBash(t)

	root := t.TempDir()
	prefix := applianceRooted(root, logicalPrefix)
	configDir := applianceRooted(root, logicalConfigDir)
	dataDir := applianceRooted(root, logicalDataDir)
	systemdDir := applianceRooted(root, logicalSystemdDir)

	baseEnv := applianceEnv(root)

	// --- A real release-signing keypair, supplied out of band ------------
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubKeyPath := filepath.Join(configDir, "ota-signing-key.pub")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubKeyPath, []byte(hex.EncodeToString(pub)), 0o644); err != nil {
		t.Fatal(err)
	}

	// The public key reaches both the daemon and the privileged updater
	// through the appliance env file, never through the artifact channel.
	envFile := filepath.Join(configDir, "geocam-edge.env")
	if err := os.WriteFile(envFile, []byte("GEOCAM_OTA_PUBLIC_KEY_FILE="+pubKeyPath+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	// --- Install the current release with the real binary ---------------
	realBinary := buildApplianceBinary(t)
	if out, err := runApplianceScript(t, append(baseEnv, "GEOCAM_VERSION=1.0.0"), "install.sh", realBinary); err != nil {
		t.Fatalf("install v1.0.0 failed: %v\n%s", err, out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.0.0" {
		t.Fatalf("after install, current -> %q, want releases/1.0.0", got)
	}
	if got := readTextFile(t, envFile); !strings.Contains(got, "GEOCAM_OTA_PUBLIC_KEY_FILE=") {
		t.Errorf("install.sh overwrote the pre-existing env file: %q", got)
	}

	// The rendered privileged unit must hand that env file to the verifier.
	// Without it GEOCAM_OTA_PUBLIC_KEY_FILE is unset in the root service and
	// every real upgrade fails closed, so this is the wiring the whole path
	// depends on.
	unit := readTextFile(t, filepath.Join(systemdDir, "geocam-edge-ota-updater.service"))
	if !strings.Contains(unit, "EnvironmentFile=") {
		t.Error("the privileged OTA updater unit has no EnvironmentFile, so the " +
			"root verifier can never resolve GEOCAM_OTA_PUBLIC_KEY_FILE")
	}
	if !strings.Contains(unit, "geocam-edge.env") {
		t.Errorf("the privileged OTA updater unit does not reference the appliance env file:\n%s", unit)
	}
	if strings.Contains(unit, "@GEOCAM_ENV_FILE@") {
		t.Errorf("install.sh left an unsubstituted @GEOCAM_ENV_FILE@ placeholder:\n%s", unit)
	}

	// --- Candidate v1.1.0, properly signed ------------------------------
	candidateArtifact := buildRealArtifact(t, t.TempDir(), "1.1.0", runtime.GOARCH, realBinary)
	candidateName := fmt.Sprintf("geocam-edge-1.1.0-linux-%s.tar.gz", runtime.GOARCH)
	staged := filepath.Join(dataDir, "ota", "pending", "1.1.0")
	stageSignedRelease(t, staged, candidateArtifact, candidateName, "1.1.0", runtime.GOARCH, priv)

	request := filepath.Join(dataDir, "ota", "apply.request")
	if err := os.WriteFile(request, []byte("1.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// --- Activation ------------------------------------------------------
	// systemd supplies this through the unit's EnvironmentFile; here the test
	// supplies it directly, because no systemd runs in a staged root.
	updaterEnv := append(append([]string(nil), baseEnv...),
		"GEOCAM_OTA_PUBLIC_KEY_FILE="+pubKeyPath)

	out, err := runApplianceScript(t, updaterEnv, "ota-updater.sh")
	if err != nil {
		t.Fatalf("the privileged updater failed to activate a correctly signed release: %v\n%s\nstate: %s",
			err, out, readStateFile(dataDir))
	}

	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.1.0" {
		t.Errorf("current -> %q after activation, want releases/1.1.0", got)
	}
	if _, err := os.Stat(filepath.Join(prefix, "releases", "1.1.0", "geocam-edge")); err != nil {
		t.Errorf("the activated release is missing its binary: %v", err)
	}
	state := strings.TrimSpace(readTextFile(t, filepath.Join(dataDir, "ota", "state")))
	if state != "succeeded:1.1.0" {
		t.Errorf("OTA state = %q, want succeeded:1.1.0", state)
	}
	if _, err := os.Stat(request); !os.IsNotExist(err) {
		t.Errorf("apply.request was not consumed: %v", err)
	}
	if _, err := os.Stat(request + ".processed"); err != nil {
		t.Errorf("processed request marker missing: %v", err)
	}
	// The release we upgraded from is still on disk, which is what makes a
	// rollback possible at all.
	if _, err := os.Stat(filepath.Join(prefix, "releases", "1.0.0", "geocam-edge")); err != nil {
		t.Errorf("the previous release is no longer present after activation: %v", err)
	}

	// --- Candidate v1.2.0 signed by the WRONG key must not activate ------
	otherPubUnused, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate second keypair: %v", err)
	}
	_ = otherPubUnused

	badArtifact := buildRealArtifact(t, t.TempDir(), "1.2.0", runtime.GOARCH, realBinary)
	badName := fmt.Sprintf("geocam-edge-1.2.0-linux-%s.tar.gz", runtime.GOARCH)
	badStaged := filepath.Join(dataDir, "ota", "pending", "1.2.0")
	stageSignedRelease(t, badStaged, badArtifact, badName, "1.2.0", runtime.GOARCH, otherPriv)

	if err := os.WriteFile(request, []byte("1.2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err = runApplianceScript(t, updaterEnv, "ota-updater.sh")
	if err == nil {
		t.Fatalf("a release signed by an unknown key was accepted:\n%s", out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.1.0" {
		t.Errorf("current -> %q after a rejected candidate, want the previous release releases/1.1.0", got)
	}
	state = strings.TrimSpace(readTextFile(t, filepath.Join(dataDir, "ota", "state")))
	if state != "failed:verification" {
		t.Errorf("OTA state = %q after a bad signature, want failed:verification", state)
	}
	// The release that is actually running must still be usable.
	info, err := os.Stat(filepath.Join(prefix, "releases", "1.1.0", "geocam-edge"))
	if err != nil {
		t.Fatalf("the running release disappeared after a rejected candidate: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the running release is no longer executable: mode %v", info.Mode())
	}

	// --- Candidate v1.3.0 with a broken layout must not activate ---------
	brokenArtifact := buildRealArtifact(t, t.TempDir(), "1.3.0", "", realBinary) // no ARCH marker
	brokenName := fmt.Sprintf("geocam-edge-1.3.0-linux-%s.tar.gz", runtime.GOARCH)
	brokenStaged := filepath.Join(dataDir, "ota", "pending", "1.3.0")
	stageSignedRelease(t, brokenStaged, brokenArtifact, brokenName, "1.3.0", runtime.GOARCH, priv)

	if err := os.WriteFile(request, []byte("1.3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runApplianceScript(t, updaterEnv, "ota-updater.sh")
	if err == nil {
		t.Fatalf("a candidate whose artifact does not declare its architecture was accepted:\n%s", out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.1.0" {
		t.Errorf("current -> %q after a structurally invalid candidate, want releases/1.1.0", got)
	}
}

// The public key is the whole trust anchor, so the appliance must fail closed
// when it is absent: a perfectly valid, validly signed candidate still must not
// be activated.
func TestW9_MissingPublicKeyFailsClosedEvenForAValidRelease(t *testing.T) {
	requireBash(t)

	root := t.TempDir()
	prefix := applianceRooted(root, logicalPrefix)
	dataDir := applianceRooted(root, logicalDataDir)

	baseEnv := applianceEnv(root)

	realBinary := buildApplianceBinary(t)
	if out, err := runApplianceScript(t, append(baseEnv, "GEOCAM_VERSION=1.0.0"), "install.sh", realBinary); err != nil {
		t.Fatalf("install v1.0.0 failed: %v\n%s", err, out)
	}

	// Note: no GEOCAM_OTA_PUBLIC_KEY_FILE anywhere — the trust anchor is absent.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	_ = pub

	artifact := buildRealArtifact(t, t.TempDir(), "1.1.0", runtime.GOARCH, realBinary)
	name := fmt.Sprintf("geocam-edge-1.1.0-linux-%s.tar.gz", runtime.GOARCH)
	stageSignedRelease(t, filepath.Join(dataDir, "ota", "pending", "1.1.0"),
		artifact, name, "1.1.0", runtime.GOARCH, priv)

	request := filepath.Join(dataDir, "ota", "apply.request")
	if err := os.WriteFile(request, []byte("1.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runApplianceScript(t, baseEnv, "ota-updater.sh")
	if err == nil {
		t.Fatalf("a validly signed release was activated with no public key configured:\n%s", out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.0.0" {
		t.Errorf("current -> %q without a public key, want releases/1.0.0", got)
	}
	state := strings.TrimSpace(readTextFile(t, filepath.Join(dataDir, "ota", "state")))
	if state != "failed:verification" {
		t.Errorf("OTA state = %q with no public key, want failed:verification", state)
	}
}

// No signing key material may ship with the appliance: verification is
// public-key only, and the private key lives in the release pipeline.
func TestW9_NoSigningKeyMaterialShipsWithTheAppliance(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	forbidden := []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN ED25519 PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
	}

	var scanned int
	err = filepath.WalkDir(filepath.Join(repoRoot, "deploy"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Only shipped artifacts count. Test sources may name PEM markers
		// without shipping key material, and this file is one of them.
		if strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, marker := range forbidden {
			if strings.Contains(string(data), marker) {
				t.Errorf("%s contains private key material (%q); signing keys never ship to an appliance",
					path, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking deploy/: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no files scanned under deploy/; the check is not actually running")
	}

	// The private key is only ever loaded by the release pipeline's signing
	// command, never by the agent runtime.
	agentDir := filepath.Join(repoRoot, "internal", "agent")
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatalf("read %s: %v", agentDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body := readTextFile(t, filepath.Join(agentDir, entry.Name()))
		if strings.Contains(body, "LoadPrivateKey") || strings.Contains(body, "SignManifest") {
			t.Errorf("internal/agent/%s references release signing code; the agent runtime must never hold a private key",
				entry.Name())
		}
	}
}

// The appliance scripts keep the *logical* destination paths and prefix them
// with GEOCAM_INSTALL_ROOT themselves (lib.sh:root_path), so a staged test must
// pass logical paths and resolve the physical ones for its own assertions.
const (
	logicalPrefix     = "/opt/geocam-edge"
	logicalConfigDir  = "/etc/geocam-edge"
	logicalDataDir    = "/var/lib/geocam-edge"
	logicalSystemdDir = "/etc/systemd/system"
)

func applianceRooted(root, logical string) string {
	return filepath.Join(root, strings.TrimPrefix(logical, "/"))
}

// applianceEnv points every appliance path at an install root while keeping the
// logical values the scripts expect.
func applianceEnv(root string) []string {
	return []string{
		"GEOCAM_INSTALL_ROOT=" + root,
		"GEOCAM_PREFIX=" + logicalPrefix,
		"GEOCAM_CONFIG_DIR=" + logicalConfigDir,
		"GEOCAM_DATA_DIR=" + logicalDataDir,
		"GEOCAM_SYSTEMD_DIR=" + logicalSystemdDir,
		"GEOCAM_LIBEXEC_DIR=/usr/libexec/geocam-edge",
		"GEOCAM_OTA_STAGING_DIR=/run/geocam-edge/ota-staging",
		"GEOCAM_TEST_BINARY_ARCH=" + runtime.GOARCH,
	}
}

// readStateFile returns the privileged updater's state file, or a diagnostic
// string when it was never written — the state is the updater's only
// machine-readable verdict.
func readStateFile(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(dataDir, "ota", "state"))
	if err != nil {
		return "<no state file: " + err.Error() + ">"
	}
	return strings.TrimSpace(string(data))
}

// TestW9_RollbackRefusesSafelyWhenThereIsNoUsableReleaseToReturnTo pins the
// rollback safety properties the appliance actually has: it refuses at the
// first sign that the recorded previous release is not usable, and a refusal
// never disturbs the release that is currently running.
//
// NOT asserted here, because it is a real gap rather than current behaviour:
// rollback.sh re-checks neither the signature nor the checksum of the release
// it returns to — it only requires the directory to exist. Its safety rests on
// the release tree being root-owned (install.sh chowns it), which is weaker
// than docs/security/update-trust.md requirement 7 ("rollback must not become
// a bypass") states. Closing that gap needs a rollback-verifiable release
// layout, so it is out of scope for Hito W and recorded as a Hito Y item in
// docs/testing/failure-lifecycle-w.md.
func TestW9_RollbackRefusesSafelyWhenThereIsNoUsableReleaseToReturnTo(t *testing.T) {
	requireBash(t)

	root := t.TempDir()
	prefix := applianceRooted(root, logicalPrefix)

	realBinary := buildApplianceBinary(t)
	if out, err := runApplianceScript(t, append(applianceEnv(root), "GEOCAM_VERSION=1.0.0"), "install.sh", realBinary); err != nil {
		t.Fatalf("install v1.0.0 failed: %v\n%s", err, out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.0.0" {
		t.Fatalf("after a first install, current -> %q, want releases/1.0.0", got)
	}

	// A first install has nothing to roll back to, so no previous release is
	// recorded and rollback must refuse rather than repoint `current`.
	if _, err := os.Stat(filepath.Join(prefix, ".previous")); err == nil {
		t.Error(".previous exists after a first install, but there was nothing to record")
	}
	if out, err := runApplianceScript(t, applianceEnv(root), "rollback.sh"); err == nil {
		t.Fatalf("rollback succeeded with no recorded previous release:\n%s", out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.0.0" {
		t.Errorf("current -> %q after a refused rollback, want releases/1.0.0", got)
	}

	// A recorded previous release that has since been removed must also be
	// refused: silently pointing `current` at a missing directory would leave
	// the appliance unable to start.
	if err := os.WriteFile(filepath.Join(prefix, ".previous"), []byte("releases/0.9.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runApplianceScript(t, applianceEnv(root), "rollback.sh"); err == nil {
		t.Fatalf("rollback succeeded toward a release directory that does not exist:\n%s", out)
	}
	if got := readLink(t, filepath.Join(prefix, "current")); got != "releases/1.0.0" {
		t.Errorf("current -> %q after a refused rollback, want releases/1.0.0", got)
	}

	// The running release is still intact and executable after both refusals.
	info, err := os.Stat(filepath.Join(prefix, "releases", "1.0.0", "geocam-edge"))
	if err != nil {
		t.Fatalf("the running release was damaged by a refused rollback: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the running release is no longer executable: mode %v", info.Mode())
	}
}
