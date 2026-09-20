package appliance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These are the first tests that execute package.sh itself. Before B10 the
// packaging script had no direct test: other tests synthesised a tarball with
// buildArtifact(), which could not catch package.sh shipping an appliance
// without ffmpeg.

// runPackageScript runs package.sh with the given extra args.
func runPackageScript(t *testing.T, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	requireBash(t)
	cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(t), "package.sh")}, args...)...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeFakeFfmpeg creates the dist/ffmpeg-linux-<arch> files that
// build-ffmpeg-static.sh would normally produce, so packaging can be tested
// without Docker.
func writeFakeFfmpeg(t *testing.T, distDir string) {
	t.Helper()
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(distDir, "ffmpeg-linux-"+arch)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho fake static ffmpeg\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func tarEntrySet(t *testing.T, artifact string) map[string]bool {
	t.Helper()
	out, err := exec.Command("tar", "tzf", artifact).Output()
	if err != nil {
		t.Fatalf("tar tzf %s: %v", artifact, err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		set[strings.TrimSpace(line)] = true
	}
	return set
}

// TestPackageScript_RequireFfmpegFailsClosedWhenMissing is the B10 regression
// guard: a release must not be able to publish an appliance artifact without
// the static ffmpeg binary.
func TestPackageScript_RequireFfmpegFailsClosedWhenMissing(t *testing.T) {
	dist := t.TempDir()

	out, err := runPackageScript(t, nil, "--require-ffmpeg", "b10-noffmpeg", dist)
	if err == nil {
		t.Fatalf("package.sh --require-ffmpeg succeeded without ffmpeg; it must fail closed.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "ffmpeg") {
		t.Errorf("failure message does not mention ffmpeg, so it is not diagnosable:\n%s", out)
	}
	if !strings.Contains(out, "--require-ffmpeg") {
		t.Errorf("failure message does not name the flag that caused it:\n%s", out)
	}

	// No artifact may have been left behind claiming to be a release.
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(dist, "geocam-edge-b10-noffmpeg-linux-"+arch+".tar.gz")
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("a tarball was produced despite the missing ffmpeg: %s", p)
		}
	}
}

// TestPackageScript_RequireFfmpegIncludesItAndVerifiesLayout covers the happy
// path: with ffmpeg present for both architectures the artifact is produced and
// carries every entry a released appliance needs.
func TestPackageScript_RequireFfmpegIncludesItAndVerifiesLayout(t *testing.T) {
	dist := t.TempDir()
	writeFakeFfmpeg(t, dist)

	out, err := runPackageScript(t, nil, "--require-ffmpeg", "b10-withffmpeg", dist)
	if err != nil {
		t.Fatalf("package.sh --require-ffmpeg failed with ffmpeg present: %v\noutput:\n%s", err, out)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		artifact := filepath.Join(dist, "geocam-edge-b10-withffmpeg-linux-"+arch+".tar.gz")
		if _, statErr := os.Stat(artifact); statErr != nil {
			t.Fatalf("artifact not produced for %s: %v", arch, statErr)
		}
		entries := tarEntrySet(t, artifact)

		// Exactly the required layout, verified on the ARTIFACT rather than on
		// the staging directory.
		for _, required := range []string{
			"./geocam-edge", "./ffmpeg", "./VERSION", "./ARCH",
			"./scripts/install.sh", "./scripts/update.sh", "./scripts/rollback.sh",
			"./scripts/uninstall.sh", "./scripts/wait-ready.sh", "./scripts/bootstrap.sh",
			"./scripts/ota-updater.sh", "./scripts/lib.sh",
			"./systemd/geocam-edge.service.in",
			"./systemd/geocam-edge-ota-updater.service.in",
			"./systemd/geocam-edge-ota-updater.path.in",
			"./config/geocam-edge.env.example",
		} {
			if !entries[required] {
				t.Errorf("%s is missing required entry %q", artifact, required)
			}
		}

		// VERSION/ARCH must identify the artifact contents.
		if archFromFile := readTarEntry(t, artifact, "./ARCH"); archFromFile != arch {
			t.Errorf("ARCH inside %s = %q, want %q", artifact, archFromFile, arch)
		}
		if v := readTarEntry(t, artifact, "./VERSION"); v != "b10-withffmpeg" {
			t.Errorf("VERSION inside %s = %q, want %q", artifact, v, "b10-withffmpeg")
		}
	}
}

// TestPackageScript_LocalModeStillAllowsMissingFfmpeg preserves the historical
// developer behaviour: without the flag, a machine without Docker can still
// package the appliance, and ffmpeg is simply absent.
func TestPackageScript_LocalModeStillAllowsMissingFfmpeg(t *testing.T) {
	dist := t.TempDir()

	out, err := runPackageScript(t, nil, "b10-local", dist)
	if err != nil {
		t.Fatalf("plain package.sh must still work without ffmpeg (dev behaviour): %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "packaging without it") {
		t.Errorf("expected the missing-ffmpeg warning, got:\n%s", out)
	}

	artifact := filepath.Join(dist, "geocam-edge-b10-local-linux-amd64.tar.gz")
	entries := tarEntrySet(t, artifact)
	if entries["./ffmpeg"] {
		t.Error("ffmpeg should be absent from a local-mode artifact when no ffmpeg binary exists")
	}
	// The rest of the layout is still complete.
	for _, required := range []string{"./geocam-edge", "./VERSION", "./ARCH", "./scripts/install.sh", "./config/geocam-edge.env.example"} {
		if !entries[required] {
			t.Errorf("local-mode artifact missing %q", required)
		}
	}
}

// TestFfmpegRecipeStaysLgplOnly guards the licensing decision B10 must not
// change. The static ffmpeg is built from the Dockerfile's ffmpeg-build stage;
// adding any of these flags would relicense the shipped binary.
func TestFfmpegRecipeStaysLgplOnly(t *testing.T) {
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(scriptsDir(t)))) // deploy/appliance/scripts -> repo root
	data, err := os.ReadFile(filepath.Join(repoRoot, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(data))

	// Only inspect the configure invocation's option lines, so an explanatory
	// comment mentioning a flag is not mistaken for its use.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "--") {
			continue
		}
		for _, forbidden := range []string{"--enable-gpl", "--enable-nonfree", "--enable-libx264", "--enable-libx265"} {
			if strings.HasPrefix(trimmed, forbidden) {
				t.Errorf("ffmpeg build enables %s, which breaks the LGPL-only licensing decision: %q", forbidden, trimmed)
			}
		}
	}
}

// TestBuildFfmpegRecipeIsReusedNotDuplicated makes sure the release path calls
// the existing recipe rather than a second, divergent ffmpeg build.
func TestBuildFfmpegRecipeIsReusedNotDuplicated(t *testing.T) {
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(scriptsDir(t))))
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	// Both architectures must be built before packaging.
	for _, arch := range []string{"amd64", "arm64"} {
		want := "build-ffmpeg-static.sh " + arch
		if !strings.Contains(body, want) {
			t.Errorf("release.yml does not run %q, so a release can still ship without ffmpeg", want)
		}
	}
	if !strings.Contains(body, "--require-ffmpeg") {
		t.Error("release.yml does not pass --require-ffmpeg, so a missing ffmpeg would only warn")
	}
	// The ffmpeg build must come before packaging. Match the actual invocations
	// (prefixed by their scripts/ path) rather than bare filenames, so an
	// explanatory comment earlier in the file cannot satisfy this check.
	ffmpegAt := strings.Index(body, "scripts/build-ffmpeg-static.sh amd64")
	packageAt := strings.Index(body, "scripts/package.sh --require-ffmpeg")
	if ffmpegAt < 0 || packageAt < 0 || ffmpegAt > packageAt {
		t.Error("the static ffmpeg build must run before package.sh in release.yml")
	}
	// And no second ffmpeg build recipe may be introduced here.
	if strings.Contains(body, "enable-gpl") || strings.Contains(body, "libx264") || strings.Contains(body, "libx265") {
		t.Error("release.yml appears to contain its own ffmpeg build flags instead of reusing build-ffmpeg-static.sh")
	}
}

func readTarEntry(t *testing.T, artifact, entry string) string {
	t.Helper()
	out, err := exec.Command("tar", "xzOf", artifact, entry).Output()
	if err != nil {
		t.Fatalf("reading %s from %s: %v", entry, artifact, err)
	}
	return strings.TrimSpace(string(out))
}
