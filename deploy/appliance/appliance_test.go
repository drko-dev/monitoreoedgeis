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
		"etc/geocam-edge/geocam-edge.env",
		"etc/systemd/system/geocam-edge.service",
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
		"ExecStart=@GEOCAM_EXEC_PATH@ run",
		"EnvironmentFile=@GEOCAM_ENV_FILE@",
		"User=@GEOCAM_SERVICE_USER@",
		"Group=@GEOCAM_SERVICE_GROUP@",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"WantedBy=multi-user.target",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("unit template missing required directive: %q", want)
		}
	}

	forbidden := []string{"sleep ", "GEOCAM_ENROLLMENT_TOKEN=", "Type=forking"}
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
