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

// ── B2: Full Edge vision worker packaging ───────────────────────────────────

// visionWorkerEntries are the worker sources every artifact must carry.
var visionWorkerEntries = []string{
	"./vision-worker/worker.py",
	"./vision-worker/backend.py",
	"./vision-worker/requirements.txt",
}

// TestPackageScript_ShipsVisionWorkerInBothArchitectures covers B2: the
// appliance used to require hand provisioning of the Full Edge worker because
// the package did not carry it at all.
func TestPackageScript_ShipsVisionWorkerInBothArchitectures(t *testing.T) {
	dist := t.TempDir()

	out, err := runPackageScript(t, nil, "b2-worker", dist)
	if err != nil {
		t.Fatalf("package.sh failed: %v\noutput:\n%s", err, out)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		artifact := filepath.Join(dist, "geocam-edge-b2-worker-linux-"+arch+".tar.gz")
		entries := tarEntrySet(t, artifact)
		for _, want := range visionWorkerEntries {
			if !entries[want] {
				t.Errorf("%s does not ship %s", filepath.Base(artifact), want)
			}
		}
		// The worker sources must be the real ones, not placeholders.
		workerPy := readTarEntry(t, artifact, "./vision-worker/worker.py")
		if !strings.Contains(workerPy, "GEO CAM Edge local vision worker") {
			t.Errorf("%s carries a worker.py that is not the real worker", filepath.Base(artifact))
		}
		backendPy := readTarEntry(t, artifact, "./vision-worker/backend.py")
		if !strings.Contains(backendPy, "InferenceBackend") {
			t.Errorf("%s carries a backend.py that is not the real backend", filepath.Base(artifact))
		}
		// And the requirements file must list the real runtime dependencies.
		reqs := readTarEntry(t, artifact, "./vision-worker/requirements.txt")
		for _, dep := range []string{"ultralytics", "pillow"} {
			if !strings.Contains(reqs, dep) {
				t.Errorf("%s requirements.txt does not mention %s", filepath.Base(artifact), dep)
			}
		}
	}
}

// TestPackageScript_ShipsNoPythonRuntimeOrModels pins what B2 must NOT do:
// no interpreter, no virtualenv, no wheels and no weights may travel in the
// artifact. A "portable venv" across amd64 and arm64 cannot exist honestly.
func TestPackageScript_ShipsNoPythonRuntimeOrModels(t *testing.T) {
	dist := t.TempDir()
	if _, err := runPackageScript(t, nil, "b2-norpuntime", dist); err != nil {
		t.Fatalf("package.sh failed: %v", err)
	}
	artifact := filepath.Join(dist, "geocam-edge-b2-norpuntime-linux-amd64.tar.gz")
	entries := tarEntrySet(t, artifact)

	for entry := range entries {
		lower := strings.ToLower(entry)
		for _, forbidden := range []string{
			"/bin/python", "python3", "venv", ".whl", "site-packages",
			"torch", "ultralytics/", ".pt",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("artifact ships %q, which looks like a runtime or weights (%q)", entry, forbidden)
			}
		}
	}
	// The worker sources are the ONLY worker content.
	for entry := range entries {
		if strings.HasPrefix(entry, "./vision-worker/") {
			switch entry {
			case "./vision-worker/worker.py", "./vision-worker/backend.py", "./vision-worker/requirements.txt", "./vision-worker/":
			default:
				t.Errorf("unexpected entry in vision-worker/: %q", entry)
			}
		}
	}
}

// TestInstallStagesVisionWorkerIntoTheRelease covers the install half of B2.
func TestInstallStagesVisionWorkerIntoTheRelease(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "geocam-edge")
	fakeBinary(t, filepath.Dir(binPath), "geocam-edge")

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, binPath)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	releaseDir := filepath.Join(root, "opt/geocam-edge/releases/1.0.0")
	for _, f := range []string{"worker.py", "backend.py", "requirements.txt"} {
		p := filepath.Join(releaseDir, "vision-worker", f)
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("install.sh did not install %s: %v", p, statErr)
		}
	}

	// The unit must derive the worker SCRIPT path from the release, so no
	// operator has to invent it.
	unit, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/geocam-edge.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(unit), "GEOCAM_EDGE_YOLO_WORKER_ARGS=/opt/geocam-edge/current/vision-worker/worker.py") {
		t.Errorf("unit does not derive the vision worker script path:\n%s", unit)
	}
	// The INTERPRETER must NOT be hardcoded: the package ships no Python.
	if strings.Contains(string(unit), "GEOCAM_EDGE_YOLO_WORKER_CMD=") {
		t.Errorf("unit hardcodes GEOCAM_EDGE_YOLO_WORKER_CMD, but no interpreter is shipped:\n%s", unit)
	}
	// The preflight must be installed with the release.
	if _, statErr := os.Stat(filepath.Join(releaseDir, "scripts", "check-vision-runtime.sh")); statErr != nil {
		t.Errorf("check-vision-runtime.sh was not installed: %v", statErr)
	}
}

// TestInstallAndRollbackKeepVisionWorkerVersioned checks the worker travels
// with its release across an upgrade and a rollback, because the socket
// protocol lives in both the agent and the worker.
func TestInstallAndRollbackKeepVisionWorkerVersioned(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	fakeBinary(t, binDir, "geocam-edge")
	binPath := filepath.Join(binDir, "geocam-edge")

	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, binPath); err != nil {
		t.Fatalf("install 1.0.0: %v\n%s", err, out)
	}
	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.1.0"}, binPath); err != nil {
		t.Fatalf("install 1.1.0: %v\n%s", err, out)
	}
	for _, version := range []string{"1.0.0", "1.1.0"} {
		p := filepath.Join(root, "opt/geocam-edge/releases", version, "vision-worker", "worker.py")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("release %s lost its vision worker: %v", version, err)
		}
	}

	if out, err := runScript(t, root, "rollback.sh", nil); err != nil {
		t.Fatalf("rollback failed: %v\n%s", err, out)
	}
	target, err := os.Readlink(filepath.Join(root, "opt/geocam-edge/current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/1.0.0" {
		t.Fatalf("rollback target = %q, want releases/1.0.0", target)
	}
	// The rolled-back release still has its own worker.
	p := filepath.Join(root, "opt/geocam-edge/releases/1.0.0/vision-worker/worker.py")
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the rolled-back release has no vision worker: %v", err)
	}
}

// TestInstallNeverTouchesModelWeights covers the explicit prohibition: model
// weights are external and must never be read, moved or deleted by the
// appliance scripts.
func TestInstallNeverTouchesModelWeights(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	binDir := t.TempDir()
	fakeBinary(t, binDir, "geocam-edge")
	binPath := filepath.Join(binDir, "geocam-edge")

	// Pre-provision operator-supplied weights.
	modelsDir := filepath.Join(root, "var/lib/geocam-edge/models")
	if err := os.MkdirAll(modelsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const weightBody = "operator-supplied-weights-do-not-touch"
	for _, name := range []string{"yolo11s-pose.pt", "yolo11n.pt"} {
		if err := os.WriteFile(filepath.Join(modelsDir, name), []byte(weightBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=2.0.0"}, binPath); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, name := range []string{"yolo11s-pose.pt", "yolo11n.pt"} {
		got, err := os.ReadFile(filepath.Join(modelsDir, name))
		if err != nil {
			t.Fatalf("install destroyed %s: %v", name, err)
		}
		if string(got) != weightBody {
			t.Errorf("install modified %s", name)
		}
	}
	if out, err := runScript(t, root, "uninstall.sh", nil); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	for _, name := range []string{"yolo11s-pose.pt", "yolo11n.pt"} {
		if _, err := os.Stat(filepath.Join(modelsDir, name)); err != nil {
			t.Errorf("uninstall destroyed the external model %s: %v", name, err)
		}
	}
}

// ── B2: Full Edge runtime preflight ────────────────────────────────────────

func runVisionPreflight(t *testing.T, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	requireBash(t)
	cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(t), "check-vision-runtime.sh")}, args...)...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestVisionRuntimePreflight_MissingSourcesFailsClosed: an appliance without
// the worker sources must be reported as NOT ready, with a diagnosable reason.
func TestVisionRuntimePreflight_MissingSourcesFailsClosed(t *testing.T) {
	out, err := runVisionPreflight(t, nil, "--worker-dir", t.TempDir())
	if err == nil {
		t.Fatalf("preflight passed with no worker sources:\n%s", out)
	}
	if !strings.Contains(out, "worker.py") {
		t.Errorf("diagnostic does not name the missing file:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("diagnostic does not mark the failure:\n%s", out)
	}
}

// TestVisionRuntimePreflight_UnusableInterpreterFailsClosed covers the
// interpreter being absent or not runnable.
func TestVisionRuntimePreflight_UnusableInterpreterFailsClosed(t *testing.T) {
	workerDir := filepath.Join(repoRootForTest(t), "deploy", "vision-worker")
	for _, tc := range []struct{ name, python string }{
		{"not executable", "/bin/false"},
		{"does not exist", "/nonexistent/python"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runVisionPreflight(t, nil, "--worker-dir", workerDir, "--python", tc.python)
			if err == nil {
				t.Fatalf("preflight passed with an unusable interpreter %q:\n%s", tc.python, out)
			}
		})
	}
}

// TestVisionRuntimePreflight_MissingDependenciesFailsClosed is the check that
// matters most: worker.py and backend.py import ultralytics/torch LAZILY inside
// load(), so the sources import cleanly with nothing installed. A preflight
// that only imported the worker would report a healthy Full Edge that cannot
// infer at all. This test asserts the script does NOT report success when the
// runtime is absent — and it is skipped only where the ML stack genuinely
// exists, in which case success is the correct answer.
func TestVisionRuntimePreflight_MissingDependenciesFailsClosed(t *testing.T) {
	requireBash(t)
	workerDir := filepath.Join(repoRootForTest(t), "deploy", "vision-worker")

	// Probe whether this sandbox has the runtime at all.
	probe := exec.Command("bash", "-c", `exec "$1" -c 'import ultralytics, torch, PIL' 2>&1`, "_", pythonForTest(t))
	if probe.Run() == nil {
		t.Skip("this host has ultralytics+torch+PIL installed, so the missing-dependency path cannot be exercised here")
	}

	out, err := runVisionPreflight(t, nil, "--worker-dir", workerDir)
	if err == nil {
		t.Fatalf("preflight reported success with the ML runtime absent — a worker that cannot infer must not pass:\n%s", out)
	}
	if !strings.Contains(out, "runtime dependencies are NOT satisfied") {
		t.Errorf("diagnostic does not identify the missing runtime dependencies:\n%s", out)
	}
	// It must give a reproducible remediation, not a silent pip install.
	if !strings.Contains(out, "pip install -r") {
		t.Errorf("diagnostic does not give a reproducible install command:\n%s", out)
	}
	// And it must state that weights are not downloaded.
	if !strings.Contains(out, "NOT downloaded") && !strings.Contains(out, "nothing is auto-downloaded") {
		t.Errorf("diagnostic does not state that no model is auto-downloaded:\n%s", out)
	}
}

// TestVisionRuntimePreflight_NeverDownloadsModelsOrRunsInference asserts the
// preflight is inert: running it must not create or fetch any weight file.
func TestVisionRuntimePreflight_NeverDownloadsModelsOrRunsInference(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	workerDir := filepath.Join(repoRootForTest(t), "deploy", "vision-worker")
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(modelsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Skip imports so the run reaches the model-reporting branch in a sandbox
	// without the ML stack; the model handling is identical either way.
	_, _ = runVisionPreflight(t,
		[]string{"GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS=1", "GEOCAM_DATA_DIR=" + root},
		"--worker-dir", workerDir)

	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("preflight created %d file(s) in the models directory; it must never fetch weights", len(entries))
	}
}

// TestVisionRuntimePreflight_SkipImportsIsLoud documents that the escape hatch
// used by sandboxed tests cannot silently claim readiness.
func TestVisionRuntimePreflight_SkipImportsIsLoud(t *testing.T) {
	workerDir := filepath.Join(repoRootForTest(t), "deploy", "vision-worker")
	out, _ := runVisionPreflight(t,
		[]string{"GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS=1"},
		"--worker-dir", workerDir)
	if !strings.Contains(out, "NOT established") {
		t.Errorf("skip-imports mode must warn loudly that readiness is not established:\n%s", out)
	}
}

// TestVisionRuntimePreflight_ScriptIsPackagedAndInstalled keeps the preflight
// itself shipped: it is useless if it never reaches an appliance.
func TestVisionRuntimePreflight_ScriptIsPackagedAndInstalled(t *testing.T) {
	dist := t.TempDir()
	if _, err := runPackageScript(t, nil, "b2-preflight", dist); err != nil {
		t.Fatalf("package.sh failed: %v", err)
	}
	artifact := filepath.Join(dist, "geocam-edge-b2-preflight-linux-amd64.tar.gz")
	entries := tarEntrySet(t, artifact)
	if !entries["./scripts/check-vision-runtime.sh"] {
		t.Error("check-vision-runtime.sh is not shipped in the artifact")
	}
	out, err := runScript(t, t.TempDir(), "install.sh",
		[]string{"GEOCAM_VERSION=1.0.0"}, func() string {
			d := t.TempDir()
			fakeBinary(t, d, "geocam-edge")
			return filepath.Join(d, "geocam-edge")
		}())
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(filepath.Dir(scriptsDir(t))))
}

func pythonForTest(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{os.Getenv("GEOCAM_EDGE_YOLO_WORKER_CMD"), "python3"} {
		if candidate == "" {
			continue
		}
		if p, err := exec.LookPath(candidate); err == nil {
			return p
		}
	}
	t.Skip("no python3 available")
	return ""
}
