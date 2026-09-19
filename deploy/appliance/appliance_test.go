// Package appliance_test exercises the deploy/appliance/scripts/*.sh
// install/update/rollback/uninstall lifecycle against a throwaway staged
// root (GEOCAM_INSTALL_ROOT), never the real filesystem. It requires bash
// and standard POSIX utilities (cp, tar, ln, mv) but NOT systemd, root, or
// Linux — see scripts/lib.sh:is_real_linux_target for what that means the
// scripts skip. This is the automated coverage the appliance work can carry
// without a real Linux/systemd machine, which this sandbox does not have
// (see docs/deployment/appliance.md for what remains unverified).
package appliance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func scriptsDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("scripts")
	if err != nil {
		t.Fatalf("resolving scripts dir: %v", err)
	}
	return dir
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

// runScript runs one appliance script against a staged root, with the real
// GEOCAM_DATA_DIR/GEOCAM_SAAS_URL scrubbed from the test process's own
// environment first — this repo's dev shell sets both to a developer's real
// local defaults, and a script bug that silently fell back to the
// unscrubbed environment instead of GEOCAM_INSTALL_ROOT must show up as a
// test failure, not get masked by inheriting real values.
func runScript(t *testing.T, root, name string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(t), name)}, args...)...)
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "GEOCAM_DATA_DIR=") || strings.HasPrefix(kv, "GEOCAM_SAAS_URL=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	filtered = append(filtered, "GEOCAM_INSTALL_ROOT="+root)
	filtered = append(filtered, extraEnv...)
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fakeBinary writes a minimal executable standing in for geocam-edge: real
// cross-compiled linux/amd64+arm64 binaries from `make build-linux` cannot
// run on the dev/CI machine that executes this test, and install.sh must
// not need to execute the binary at all when GEOCAM_VERSION is supplied
// (see install.sh's version-resolution comment) — this fixture proves that.
func fakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("writing fake binary: %v", err)
	}
	return path
}

func otaVerifierBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nif [ \"$1\" = ota ] && [ \"$2\" = verify ]; then exit 0; fi\necho fake\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing OTA verifier binary: %v", err)
	}
	return path
}

func rejectingOTAVerifierBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nif [ \"$1\" = ota ] && [ \"$2\" = verify ]; then exit 1; fi\necho fake\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing rejecting OTA verifier binary: %v", err)
	}
	return path
}

func TestInstallCreatesExpectedLayout(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	for _, p := range []string{
		"opt/geocam-edge/releases/1.0.0/geocam-edge",
		"opt/geocam-edge/current", // symlink
		"opt/geocam-edge/current/scripts/bootstrap.sh",
		"etc/geocam-edge/geocam-edge.env",
		"etc/systemd/system/geocam-edge.service",
		"etc/systemd/system/geocam-edge-bootstrap.service",
		"var/lib/geocam-edge",
	} {
		full := filepath.Join(root, p)
		if _, err := os.Lstat(full); err != nil {
			t.Errorf("expected path missing: %s (%v)", p, err)
		}
	}

	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatalf("reading current symlink: %v", err)
	}
	if target != "releases/1.0.0" {
		t.Errorf("current -> %q, want releases/1.0.0", target)
	}

	unitBytes, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/geocam-edge.service"))
	if err != nil {
		t.Fatalf("reading unit: %v", err)
	}
	unitStr := string(unitBytes)
	for _, expected := range []string{
		"WorkingDirectory=/var/lib/geocam-edge",
		"ExecStart=/opt/geocam-edge/current/geocam-edge run",
		"EnvironmentFile=/etc/geocam-edge/geocam-edge.env",
		"User=geocam-edge",
		"Group=geocam-edge",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"After=network-online.target",
		"Wants=network-online.target",
	} {
		if !strings.Contains(unitStr, expected) {
			t.Errorf("unit missing directive %q", expected)
		}
	}

	dataInfo, err := os.Stat(filepath.Join(root, "var/lib/geocam-edge"))
	if err != nil {
		t.Fatalf("stating data dir: %v", err)
	}
	if perm := dataInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir perm = %o, want 0700", perm)
	}

	configInfo, err := os.Stat(filepath.Join(root, "etc/geocam-edge"))
	if err != nil {
		t.Fatalf("stating config dir: %v", err)
	}
	if perm := configInfo.Mode().Perm(); perm != 0o750 {
		t.Errorf("config dir perm = %o, want 0750", perm)
	}
}

func TestInstallIsIdempotentAndPreservesData(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")

	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("first install.sh failed: %v\n%s", err, out)
	}

	identityPath := filepath.Join(root, "var/lib/geocam-edge/identity.json")
	if err := os.WriteFile(identityPath, []byte(`{"edge_id":"marker"}`), 0o600); err != nil {
		t.Fatalf("seeding identity.json: %v", err)
	}
	configPath := filepath.Join(root, "etc/geocam-edge/geocam-edge.env")
	if err := os.WriteFile(configPath, []byte("GEOCAM_SAAS_URL=https://operator-edited.example\n"), 0o640); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("second install.sh failed: %v\n%s", err, out)
	}

	gotIdentity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("reading identity.json after second install: %v", err)
	}
	if string(gotIdentity) != `{"edge_id":"marker"}` {
		t.Errorf("identity.json was overwritten by a repeat install: %q", gotIdentity)
	}

	gotConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config after second install: %v", err)
	}
	if !strings.Contains(string(gotConfig), "operator-edited.example") {
		t.Errorf("config was overwritten by a repeat install: %q", gotConfig)
	}
}

func TestInstallNewVersionPreservesData(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin1 := fakeBinary(t, t.TempDir(), "geocam-edge-v1")
	bin2 := fakeBinary(t, t.TempDir(), "geocam-edge-v2")

	// 1. Initial install of 1.0.0
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin1); err != nil {
		t.Fatalf("install 1.0.0 failed: %v\n%s", err, out)
	}

	// 2. Seed data: identity.json, credentials.json, offline buffer file, and edited config
	dataDir := filepath.Join(root, "var/lib/geocam-edge")
	identityPath := filepath.Join(dataDir, "identity.json")
	if err := os.WriteFile(identityPath, []byte(`{"device_id":"dev-123","token":"ident-tok"}`), 0o600); err != nil {
		t.Fatalf("seeding identity.json: %v", err)
	}

	credsPath := filepath.Join(dataDir, "credentials.json")
	if err := os.WriteFile(credsPath, []byte(`{"credential":"secret-credential-value"}`), 0o600); err != nil {
		t.Fatalf("seeding credentials.json: %v", err)
	}

	bufferDir := filepath.Join(dataDir, "cloud_buffer")
	if err := os.MkdirAll(bufferDir, 0o700); err != nil {
		t.Fatalf("creating buffer dir: %v", err)
	}
	bufferFilePath := filepath.Join(bufferDir, "frame_0001.bin")
	if err := os.WriteFile(bufferFilePath, []byte("buffered-frame-payload"), 0o600); err != nil {
		t.Fatalf("seeding buffer file: %v", err)
	}

	configPath := filepath.Join(root, "etc/geocam-edge/geocam-edge.env")
	customConfig := "GEOCAM_SAAS_URL=https://custom-saas.example.com\nGEOCAM_LOG_LEVEL=debug\n"
	if err := os.WriteFile(configPath, []byte(customConfig), 0o640); err != nil {
		t.Fatalf("seeding custom config: %v", err)
	}

	// 3. Install new version 2.0.0 via install.sh
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=2.0.0"}, bin2); err != nil {
		t.Fatalf("install 2.0.0 failed: %v\n%s", err, out)
	}

	// 4. Verify release structure & symlinks
	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatalf("reading current symlink: %v", err)
	}
	if target != "releases/2.0.0" {
		t.Errorf("current -> %q, want releases/2.0.0", target)
	}

	previous, err := os.ReadFile(filepath.Join(root, "opt/geocam-edge/.previous"))
	if err != nil {
		t.Fatalf("reading .previous: %v", err)
	}
	if strings.TrimSpace(string(previous)) != "releases/1.0.0" {
		t.Errorf(".previous -> %q, want releases/1.0.0", string(previous))
	}

	if _, err := os.Stat(filepath.Join(root, "opt/geocam-edge/releases/1.0.0/geocam-edge")); err != nil {
		t.Errorf("previous release 1.0.0 binary missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "opt/geocam-edge/releases/2.0.0/geocam-edge")); err != nil {
		t.Errorf("new release 2.0.0 binary missing: %v", err)
	}

	// 5. Verify P9 persistence: identity, credentials, offline buffer, config
	gotIdentity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("reading identity.json after new install: %v", err)
	}
	if string(gotIdentity) != `{"device_id":"dev-123","token":"ident-tok"}` {
		t.Errorf("identity.json modified: %q", gotIdentity)
	}

	gotCreds, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("reading credentials.json after new install: %v", err)
	}
	if string(gotCreds) != `{"credential":"secret-credential-value"}` {
		t.Errorf("credentials.json modified: %q", gotCreds)
	}

	gotBuffer, err := os.ReadFile(bufferFilePath)
	if err != nil {
		t.Fatalf("reading offline buffer file after new install: %v", err)
	}
	if string(gotBuffer) != "buffered-frame-payload" {
		t.Errorf("offline buffer file modified: %q", gotBuffer)
	}

	gotConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config after new install: %v", err)
	}
	if string(gotConfig) != customConfig {
		t.Errorf("config modified after new install: %q", gotConfig)
	}
}

// buildArtifact assembles a tar.gz matching what package.sh produces, using
// a fake binary since we cannot execute cross-arch real binaries here (see
// fakeBinary). arch controls the ARCH marker file; pass "" to omit it.
func buildArtifact(t *testing.T, dir, version, arch string) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBinary(t, stage, "geocam-edge")
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	if arch != "" {
		if err := os.WriteFile(filepath.Join(stage, "ARCH"), []byte(arch), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scriptsSrc := scriptsDir(t)
	entries, err := os.ReadDir(scriptsSrc)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(scriptsSrc, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "scripts", e.Name()), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	artifact := filepath.Join(dir, "geocam-edge-"+version+".tar.gz")
	cmd := exec.Command("tar", "czf", artifact, "-C", stage, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar czf failed: %v\n%s", err, out)
	}

	// update.sh now rejects an artifact with no checksum (fail-closed), so
	// every artifact this helper builds needs a valid sibling .sha256, same
	// format package.sh/sha256sum produce, unless a test deliberately wants
	// to exercise the missing/bad checksum path.
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	checksum := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(artifact))
	if err := os.WriteFile(artifact+".sha256", []byte(checksum), 0o644); err != nil {
		t.Fatal(err)
	}

	return artifact
}

func TestUpdateThenRollbackRoundTrips(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	hostArch := "amd64"
	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		m := strings.TrimSpace(string(out))
		if m == "arm64" || m == "aarch64" {
			hostArch = "arm64"
		}
	}

	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	artifactDir := t.TempDir()
	artifact := buildArtifact(t, artifactDir, "2.0.0", hostArch)

	out, err := runScript(t, root, "update.sh", nil, artifact)
	if err != nil {
		t.Fatalf("update.sh failed: %v\n%s", err, out)
	}
	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/2.0.0" {
		t.Fatalf("after update, current -> %q, want releases/2.0.0", target)
	}
	if _, err := os.Stat(filepath.Join(root, "opt/geocam-edge/releases/1.0.0/geocam-edge")); err != nil {
		t.Errorf("previous release 1.0.0 was deleted, want it kept for rollback: %v", err)
	}

	out, err = runScript(t, root, "rollback.sh", nil)
	if err != nil {
		t.Fatalf("rollback.sh failed: %v\n%s", err, out)
	}
	target, err = os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/1.0.0" {
		t.Fatalf("after rollback, current -> %q, want releases/1.0.0", target)
	}
}

func TestUpdateRejectsWrongArchitecture(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	artifactDir := t.TempDir()
	artifact := buildArtifact(t, artifactDir, "2.0.0", "definitely-not-a-real-arch")

	out, err := runScript(t, root, "update.sh", nil, artifact)
	if err == nil {
		t.Fatalf("update.sh with wrong architecture should have failed; output:\n%s", out)
	}
	target, rerr := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if target != "releases/1.0.0" {
		t.Errorf("current changed despite rejected artifact: %q", target)
	}
}

func TestUpdateRejectsBadChecksum(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	artifactDir := t.TempDir()
	artifact := buildArtifact(t, artifactDir, "2.0.0", "")
	if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("0", 64)+"  "+filepath.Base(artifact)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "update.sh", nil, artifact)
	if err == nil {
		t.Fatalf("update.sh with a bad checksum should have failed; output:\n%s", out)
	}
	target, rerr := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if target != "releases/1.0.0" {
		t.Errorf("current changed despite rejected artifact: %q", target)
	}
}

func TestUpdateRejectsMissingChecksum(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	artifactDir := t.TempDir()
	artifact := buildArtifact(t, artifactDir, "2.0.0", "")
	if err := os.Remove(artifact + ".sha256"); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "update.sh", nil, artifact)
	if err == nil {
		t.Fatalf("update.sh with no checksum file should have failed; output:\n%s", out)
	}
	target, rerr := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if target != "releases/1.0.0" {
		t.Errorf("current changed despite rejected artifact: %q", target)
	}
}

func TestInstallRejectsWrongArchitecture(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	bin := fakeBinary(t, binDir, "geocam-edge")
	if err := os.WriteFile(filepath.Join(binDir, "ARCH"), []byte("definitely-not-a-real-arch"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin)
	if err == nil {
		t.Fatalf("install.sh with wrong architecture should have failed; output:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "opt/geocam-edge/current")); !os.IsNotExist(statErr) {
		t.Errorf("current symlink created despite rejected architecture: err=%v", statErr)
	}
}

// hostArch mirrors lib.sh's host_arch() from the Go side, for tests that
// need to know what the sandbox's own uname -m normalizes to.
func hostArch(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		t.Fatalf("uname -m: %v", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "arm64", "aarch64":
		return "arm64"
	default:
		return "amd64"
	}
}

// otherArch returns the appliance architecture that is NOT a.
func otherArch(a string) string {
	if a == "amd64" {
		return "arm64"
	}
	return "amd64"
}

// TestInstallRejectsArchMarkerBinaryMismatch covers the hardening gap: an
// ARCH sidecar file that matches the host is no longer enough on its own —
// install.sh must also check it against the binary's real, inspected
// architecture (simulated here via the test-only GEOCAM_TEST_BINARY_ARCH,
// since the fake fixture binary is a plain shell script) and reject if
// they disagree, before touching releases/current.
func TestInstallRejectsArchMarkerBinaryMismatch(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	bin := fakeBinary(t, binDir, "geocam-edge")
	host := hostArch(t)
	// ARCH marker claims the host's own architecture (the check the old
	// code relied on alone), but the real binary is reported as the other
	// one — this must still be rejected.
	if err := os.WriteFile(filepath.Join(binDir, "ARCH"), []byte(host), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh",
		[]string{"GEOCAM_VERSION=1.0.0", "GEOCAM_TEST_BINARY_ARCH=" + otherArch(host)}, bin)
	if err == nil {
		t.Fatalf("install.sh with ARCH/binary mismatch should have failed; output:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "opt/geocam-edge/current")); !os.IsNotExist(statErr) {
		t.Errorf("current symlink created despite rejected architecture: err=%v", statErr)
	}
}

// TestInstallFailsClosedWhenArchUnverifiable covers the fail-closed
// requirement: on a real Linux install target, if the binary's real
// architecture can't be positively determined (no ARCH marker, and
// file/readelf are inconclusive against the fake shell-script fixture),
// install.sh must reject rather than silently skip the check.
// GEOCAM_TEST_ARCH_CHECK_STRICT simulates "real Linux install target" from
// this staged sandbox, where is_real_linux_target is otherwise always
// false.
func TestInstallFailsClosedWhenArchUnverifiable(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")

	out, err := runScript(t, root, "install.sh",
		[]string{"GEOCAM_VERSION=1.0.0", "GEOCAM_TEST_ARCH_CHECK_STRICT=1"}, bin)
	if err == nil {
		t.Fatalf("install.sh should fail closed when arch can't be verified on a real target; output:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "opt/geocam-edge/current")); !os.IsNotExist(statErr) {
		t.Errorf("current symlink created despite unverifiable architecture: err=%v", statErr)
	}
}

// TestInstallAcceptsMatchingArchMarker is the valid path: the ARCH marker
// matches the host, and matches the (simulated) binary's real architecture
// too — install proceeds normally.
func TestInstallAcceptsMatchingArchMarker(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	bin := fakeBinary(t, binDir, "geocam-edge")
	host := hostArch(t)
	if err := os.WriteFile(filepath.Join(binDir, "ARCH"), []byte(host), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh",
		[]string{"GEOCAM_VERSION=1.0.0", "GEOCAM_TEST_BINARY_ARCH=" + host}, bin)
	if err != nil {
		t.Fatalf("install.sh with matching architecture should have succeeded: %v\n%s", err, out)
	}
	target, rerr := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if target != "releases/1.0.0" {
		t.Errorf("current -> %q, want releases/1.0.0", target)
	}
}

func TestInstallWithFfmpegPinsSystemdToPackagedFfmpeg(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	dir := t.TempDir()
	bin := fakeBinary(t, dir, "geocam-edge")
	ffmpeg := fakeBinary(t, dir, "ffmpeg")

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin, ffmpeg)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	unit, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/geocam-edge.service"))
	if err != nil {
		t.Fatalf("reading generated systemd unit: %v", err)
	}
	want := "Environment=GEOCAM_VIDEO_FFMPEG_PATH=/opt/geocam-edge/current/ffmpeg"
	if !strings.Contains(string(unit), want) {
		t.Errorf("packaged install with ffmpeg should pin the service to it; unit missing %q:\n%s", want, unit)
	}
}

func TestUninstallPreservesDataByDefault(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	marker := filepath.Join(root, "var/lib/geocam-edge/credentials.json")
	if err := os.WriteFile(marker, []byte(`{"credential":"should-never-be-deleted"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := runScript(t, root, "uninstall.sh", nil); err != nil {
		t.Fatalf("uninstall.sh failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(root, "opt/geocam-edge")); !os.IsNotExist(err) {
		t.Errorf("expected /opt/geocam-edge removed, got err=%v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("GEOCAM_DATA_DIR content was deleted by a non-purge uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/geocam-edge/geocam-edge.env")); err != nil {
		t.Errorf("config was deleted by a non-purge uninstall: %v", err)
	}
}

func TestUninstallPurgeRequiresConfirmation(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	dataDir := filepath.Join(root, "var/lib/geocam-edge")

	// --purge without confirmation env var reads from stdin; feed it "no".
	cmd := exec.Command("bash", filepath.Join(scriptsDir(t), "uninstall.sh"), "--purge")
	cmd.Env = append(os.Environ(), "GEOCAM_INSTALL_ROOT="+root)
	cmd.Stdin = strings.NewReader("no\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall.sh --purge (declined) failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data dir removed despite declined confirmation: %v", err)
	}

	// Explicit confirmation does purge it.
	out, err := runScript(t, root, "uninstall.sh", []string{"GEOCAM_UNINSTALL_PURGE_CONFIRM=yes"}, "--purge")
	if err != nil {
		t.Fatalf("uninstall.sh --purge (confirmed) failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("data dir still present after confirmed purge: err=%v", err)
	}
}

// TestNoSecretsInScriptOutput guards against a future change reintroducing
// something that prints the enrollment token or stored credential. It
// checks the actual combined output of a full install+update+rollback
// cycle, not just a grep of the scripts' source.
func TestNoSecretsInScriptOutput(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")

	const fakeToken = "super-secret-enrollment-token-12345"
	out, err := runScript(t, root, "install.sh", []string{
		"GEOCAM_VERSION=1.0.0",
		"GEOCAM_ENROLLMENT_TOKEN=" + fakeToken,
	}, bin)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if strings.Contains(out, fakeToken) {
		t.Errorf("install.sh output leaked GEOCAM_ENROLLMENT_TOKEN:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(root, "var/lib/geocam-edge/credentials.json"),
		[]byte(`{"credential":"edg_live_should_not_leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin)
	if err != nil {
		t.Fatalf("second install.sh failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "edg_live_should_not_leak") {
		t.Errorf("install.sh output leaked stored credential:\n%s", out)
	}
}

// TestSystemdUnitTemplateStructure statically checks the unit template for
// the directives docs/deployment/appliance.md and the task both call out as
// required. `systemd-analyze verify` would be a stronger check, but it
// requires a real systemd install this sandbox does not have — see the
// package doc comment.
func TestSystemdUnitTemplateStructure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(scriptsDir(t), "..", "systemd", "geocam-edge.service.in"))
	if err != nil {
		t.Fatalf("reading unit template: %v", err)
	}
	content := string(data)

	required := []string{
		"[Unit]", "[Service]", "[Install]",
		"After=network-online.target",
		"WorkingDirectory=@GEOCAM_DATA_DIR_PLACEHOLDER@",
		"ExecStart=@GEOCAM_EXEC_PATH@ run",
		"EnvironmentFile=@GEOCAM_ENV_FILE@",
		"User=@GEOCAM_SERVICE_USER@",
		"Group=@GEOCAM_SERVICE_GROUP@",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"WantedBy=multi-user.target",
		// Hito S / S10: least-privilege hardening confirmed safe -- no
		// Linux capability is needed by this agent/ffmpeg. PrivateDevices
		// is deliberately NOT required here: Full Edge's CUDA vision
		// worker profile (GEOCAM_EDGE_YOLO_DEVICE=cuda) needs host
		// accelerator device nodes, and this unit is shared across every
		// processing mode.
		"CapabilityBoundingSet=",
	}

	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("unit template missing required directive: %q", want)
		}
	}

	forbidden := []string{
		"sleep ", "GEOCAM_ENROLLMENT_TOKEN=", "Type=forking",
		// Hito S / S10 correction: PrivateDevices=true would mask the host
		// accelerator device nodes (/dev/nvidia*) Full Edge's CUDA vision
		// worker profile (GEOCAM_EDGE_YOLO_DEVICE=cuda, Hito K/K5) needs.
		// This unit is shared by every processing mode, so it must not
		// assume CPU-only.
		"PrivateDevices=true",
	}
	for _, bad := range forbidden {
		if strings.Contains(content, bad) {
			t.Errorf("unit template contains disallowed content: %q", bad)
		}
	}

	// Every section header must appear before any of its expected keys —
	// a cheap structural sanity check without a full INI parser.
	if strings.Index(content, "[Unit]") > strings.Index(content, "[Service]") {
		t.Error("[Unit] section must come before [Service]")
	}
	if strings.Index(content, "[Service]") > strings.Index(content, "[Install]") {
		t.Error("[Service] section must come before [Install]")
	}
}

func TestPrivilegedOTAUnitTemplateStructure(t *testing.T) {
	service, err := os.ReadFile(filepath.Join(scriptsDir(t), "..", "systemd", "geocam-edge-ota-updater.service.in"))
	if err != nil {
		t.Fatal(err)
	}
	pathUnit, err := os.ReadFile(filepath.Join(scriptsDir(t), "..", "systemd", "geocam-edge-ota-updater.path.in"))
	if err != nil {
		t.Fatal(err)
	}
	serviceText := string(service)
	pathText := string(pathUnit)
	for _, want := range []string{
		"User=root",
		"Group=root",
		"ExecStart=@GEOCAM_OTA_UPDATER_EXEC@",
		"ProtectSystem=strict",
		"ReadWritePaths=@GEOCAM_DATA_DIR_PLACEHOLDER@ @GEOCAM_PREFIX_PLACEHOLDER@",
	} {
		if !strings.Contains(serviceText, want) {
			t.Errorf("privileged updater unit missing %q", want)
		}
	}
	if strings.Contains(serviceText, "NoNewPrivileges=true") {
		t.Error("privileged updater must not drop privileges before applying an update")
	}
	for _, want := range []string{
		"PathChanged=@GEOCAM_DATA_DIR_PLACEHOLDER@/ota/apply.request",
		"Unit=geocam-edge-ota-updater.service",
	} {
		if !strings.Contains(pathText, want) {
			t.Errorf("OTA path unit missing %q", want)
		}
	}

	install, err := os.ReadFile(filepath.Join(scriptsDir(t), "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(install)
	if !strings.Contains(installText, `chown -R root:root "$PREFIX"`) {
		t.Error("install.sh does not root-own the release tree")
	}
	if !strings.Contains(installText, `chown -R "$GEOCAM_SERVICE_USER:$GEOCAM_SERVICE_GROUP" "$DATA_DIR"`) {
		t.Error("install.sh does not restrict service ownership to the data directory")
	}
}

func TestPrivilegedOTAUpdaterStagesAndConsumesRequest(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	verifier := otaVerifierBinary(t, t.TempDir(), "geocam-edge")
	baseEnv := []string{
		"GEOCAM_TEST_BINARY_ARCH=" + runtime.GOARCH,
		"GEOCAM_WAIT_READY_ATTEMPTS=1",
		"GEOCAM_WAIT_READY_INTERVAL=0",
	}
	if out, err := runScript(t, root, "install.sh", append(baseEnv, "GEOCAM_VERSION=1.0.0"), verifier); err != nil {
		t.Fatalf("initial install failed: %v\n%s", err, out)
	}

	artifact := buildArtifact(t, t.TempDir(), "2.0.0", runtime.GOARCH)
	pending := filepath.Join(root, "var/lib/geocam-edge/ota/pending/2.0.0")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactBytes, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(pending, "artifact.tar.gz")
	if err := os.WriteFile(artifactPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifactBytes)
	checksum := fmt.Sprintf("%s  artifact.tar.gz\n", hex.EncodeToString(sum[:]))
	for name, data := range map[string][]byte{
		"SHA256SUMS":     []byte(checksum),
		"SHA256SUMS.sig": []byte("signature-checked-by-current-binary"),
		"metadata.json":  []byte(fmt.Sprintf(`{"version":"2.0.0","architecture":%q}`, runtime.GOARCH)),
	} {
		if err := os.WriteFile(filepath.Join(pending, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := filepath.Join(root, "var/lib/geocam-edge/ota/apply.request")
	if err := os.WriteFile(request, []byte("2.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "ota-updater.sh", baseEnv)
	if err != nil {
		t.Fatalf("privileged updater failed: %v\n%s", err, out)
	}
	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/2.0.0" {
		t.Fatalf("current -> %q, want releases/2.0.0", target)
	}
	state, err := os.ReadFile(filepath.Join(root, "var/lib/geocam-edge/ota/state"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(state)) != "succeeded:2.0.0" {
		t.Fatalf("unexpected OTA state: %q", state)
	}
	if _, err := os.Stat(request); !os.IsNotExist(err) {
		t.Fatalf("apply.request was not consumed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/geocam-edge/ota/apply.request.processed")); err != nil {
		t.Fatalf("processed request missing: %v", err)
	}

	// A second invocation has no request to replay and must not activate again.
	if out, err := runScript(t, root, "ota-updater.sh", baseEnv); err != nil {
		t.Fatalf("second updater invocation failed: %v\n%s", err, out)
	}
	target, err = os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil || target != "releases/2.0.0" {
		t.Fatalf("second invocation changed current: target=%q err=%v", target, err)
	}
}

func TestPrivilegedOTAUpdaterRejectsPathTraversalRequest(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	verifier := otaVerifierBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, verifier); err != nil {
		t.Fatalf("initial install failed: %v\n%s", err, out)
	}
	request := filepath.Join(root, "var/lib/geocam-edge/ota/apply.request")
	if err := os.WriteFile(request, []byte("../escape\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runScript(t, root, "ota-updater.sh", nil); err == nil {
		t.Fatalf("path traversal request unexpectedly succeeded: %s", out)
	}
	state, err := os.ReadFile(filepath.Join(root, "var/lib/geocam-edge/ota/state"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(state)) != "failed:invalid-request" {
		t.Fatalf("unexpected rejection state: %q", state)
	}
	if _, err := os.Stat(filepath.Join(root, "opt/geocam-edge/current")); err != nil {
		t.Fatalf("current release disappeared after rejected request: %v", err)
	}
}

func TestPrivilegedOTAUpdaterRejectsInvalidSignature(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	verifier := rejectingOTAVerifierBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, verifier); err != nil {
		t.Fatalf("initial install failed: %v\n%s", err, out)
	}
	pending := filepath.Join(root, "var/lib/geocam-edge/ota/pending/2.0.0")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"artifact.tar.gz": []byte("not-a-valid-package"),
		"SHA256SUMS":      []byte("invalid  artifact.tar.gz\n"),
		"SHA256SUMS.sig":  []byte("invalid-signature"),
		"metadata.json":   []byte(`{"version":"2.0.0"}`),
	} {
		if err := os.WriteFile(filepath.Join(pending, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := filepath.Join(root, "var/lib/geocam-edge/ota/apply.request")
	if err := os.WriteFile(request, []byte("2.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runScript(t, root, "ota-updater.sh", nil); err == nil {
		t.Fatalf("invalid signature unexpectedly succeeded: %s", out)
	}
	state, err := os.ReadFile(filepath.Join(root, "var/lib/geocam-edge/ota/state"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(state)) != "failed:verification" {
		t.Fatalf("unexpected signature rejection state: %q", state)
	}
	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil || target != "releases/1.0.0" {
		t.Fatalf("invalid signature changed current: target=%q err=%v", target, err)
	}
}

// appliance path at a temporary directory and injects fake privileged/system
// commands through PATH. The first readiness probe fails, forcing update.sh to
// invoke rollback.sh; the rollback readiness probe then succeeds.
func TestUpdateReadinessFailureRollsBackAndPreservesData(t *testing.T) {
	requireBash(t)

	root := t.TempDir()
	prefix := filepath.Join(root, "opt", "geocam-edge")
	configDir := filepath.Join(root, "etc", "geocam-edge")
	dataDir := filepath.Join(root, "var", "lib", "geocam-edge")
	systemdDir := filepath.Join(root, "etc", "systemd", "system")
	fakeBinDir := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeFakeCommand := func(name, body string) {
		t.Helper()
		path := filepath.Join(fakeBinDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("writing fake %s: %v", name, err)
		}
	}

	writeFakeCommand("uname", `case "$1" in
  -s) echo Linux ;;
  -m) echo x86_64 ;;
  *) echo Linux ;;
esac`)
	writeFakeCommand("getent", "exit 0")
	writeFakeCommand("chown", "exit 0")
	writeFakeCommand("systemctl", "exit 0")

	curlState := filepath.Join(root, "curl-count")
	writeFakeCommand("curl", `state="${GEOCAM_TEST_CURL_STATE:?}"
n=0
[ -f "$state" ] && n="$(cat "$state")"
n=$((n + 1))
printf '%s\n' "$n" > "$state"
[ "$n" -ge 2 ]`)

	baseEnv := []string{
		"PATH=" + fakeBinDir + ":" + os.Getenv("PATH"),
		"GEOCAM_INSTALL_ROOT=",
		"GEOCAM_PREFIX=" + prefix,
		"GEOCAM_CONFIG_DIR=" + configDir,
		"GEOCAM_DATA_DIR=" + dataDir,
		"GEOCAM_SYSTEMD_DIR=" + systemdDir,
		"GEOCAM_LIBEXEC_DIR=" + filepath.Join(root, "usr", "libexec", "geocam-edge"),
		"GEOCAM_TEST_BINARY_ARCH=amd64",
		"GEOCAM_WAIT_READY_ATTEMPTS=1",
		"GEOCAM_WAIT_READY_INTERVAL=0",
		"GEOCAM_TEST_CURL_STATE=" + curlState,
	}

	runRealTargetScript := func(name string, extraEnv []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(t), name)}, args...)...)
		env := make([]string, 0, len(os.Environ())+len(baseEnv)+len(extraEnv))
		for _, kv := range os.Environ() {
			switch {
			case strings.HasPrefix(kv, "PATH="),
				strings.HasPrefix(kv, "GEOCAM_INSTALL_ROOT="),
				strings.HasPrefix(kv, "GEOCAM_PREFIX="),
				strings.HasPrefix(kv, "GEOCAM_CONFIG_DIR="),
				strings.HasPrefix(kv, "GEOCAM_DATA_DIR="),
				strings.HasPrefix(kv, "GEOCAM_SYSTEMD_DIR="),
				strings.HasPrefix(kv, "GEOCAM_TEST_BINARY_ARCH="),
				strings.HasPrefix(kv, "GEOCAM_WAIT_READY_ATTEMPTS="),
				strings.HasPrefix(kv, "GEOCAM_WAIT_READY_INTERVAL="),
				strings.HasPrefix(kv, "GEOCAM_TEST_CURL_STATE="):
				continue
			}
			env = append(env, kv)
		}
		env = append(env, baseEnv...)
		env = append(env, extraEnv...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Install v1 on the isolated fake real-Linux target.
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")
	if out, err := runRealTargetScript("install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin); err != nil {
		t.Fatalf("install v1 failed: %v\n%s", err, out)
	}

	identityPath := filepath.Join(dataDir, "identity.json")
	credentialsPath := filepath.Join(dataDir, "credentials.json")
	bufferDir := filepath.Join(dataDir, "cloud_buffer")
	bufferPath := filepath.Join(bufferDir, "frame.bin")
	if err := os.MkdirAll(bufferDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte(`{"edge_id":"persist-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte(`{"credential":"persist-me-too"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bufferPath, []byte("offline-frame"), 0o600); err != nil {
		t.Fatal(err)
	}

	artifact := buildArtifact(t, t.TempDir(), "2.0.0", "amd64")

	// v2 activation reaches readiness, fails there, and must auto-rollback.
	out, err := runRealTargetScript("update.sh", nil, artifact)
	if err == nil {
		t.Fatalf("update v2 should fail readiness and rollback; output:\n%s", out)
	}
	if !strings.Contains(out, "rolled back") {
		t.Fatalf("update output does not show automatic rollback:\n%s", out)
	}

	target, err := os.Readlink(filepath.Join(prefix, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/1.0.0" {
		t.Fatalf("after failed v2 readiness, current -> %q, want releases/1.0.0", target)
	}
	if _, err := os.Stat(filepath.Join(prefix, "releases", "1.0.0", "geocam-edge")); err != nil {
		t.Fatalf("v1 release missing after automatic rollback: %v", err)
	}

	for path, want := range map[string]string{
		identityPath:    `{"edge_id":"persist-me"}`,
		credentialsPath: `{"credential":"persist-me-too"}`,
		bufferPath:      "offline-frame",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading preserved %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("persistent data changed at %s: got %q want %q", path, got, want)
		}
	}
}
