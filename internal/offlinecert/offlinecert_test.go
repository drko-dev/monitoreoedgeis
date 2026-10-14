package offlinecert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSyntheticRuntimePackValidatesRepeatedly(t *testing.T) {
	pack, err := NewSyntheticPack(t.TempDir(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pack.Close() })

	for attempt := 0; attempt < 2; attempt++ {
		report, err := pack.Validate("linux", "amd64")
		if err != nil {
			t.Fatalf("validation attempt %d: %v", attempt+1, err)
		}
		if report.TotalArtifacts != 4 || report.ValidArtifacts != 4 {
			t.Fatalf("unexpected validation report: %+v", report)
		}
	}
}

func TestSyntheticRuntimePackRejectsCorruptionAndIncompatibility(t *testing.T) {
	t.Run("malformed manifest", func(t *testing.T) {
		pack, err := NewSyntheticPack(t.TempDir(), "linux", "amd64")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pack.Close() })
		if err := os.WriteFile(pack.ManifestPath, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := pack.Validate("linux", "amd64"); err == nil {
			t.Fatal("malformed manifest was accepted")
		}
	})

	t.Run("architecture mismatch", func(t *testing.T) {
		pack, err := NewSyntheticPack(t.TempDir(), "linux", "arm64")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pack.Close() })
		if _, err := pack.Validate("linux", "amd64"); err == nil {
			t.Fatal("incompatible architecture was accepted")
		}
	})

	t.Run("path traversal", func(t *testing.T) {
		pack, err := NewSyntheticPack(t.TempDir(), "linux", "amd64")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pack.Close() })
		pack.Manifest.Python.RelativeBinaryPath = "../outside/python"
		if err := pack.WriteManifest(); err != nil {
			t.Fatal(err)
		}
		if _, err := pack.Validate("linux", "amd64"); err == nil {
			t.Fatal("traversal manifest was accepted")
		}
	})
}

func TestSyntheticRuntimePackRejectsArtifactTampering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *SyntheticPack)
	}{
		{
			name: "content altered without changing size",
			mutate: func(t *testing.T, p *SyntheticPack) {
				path := filepath.Join(p.Root, "worker/worker.py")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				contents[0] ^= 0xff
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest hash altered",
			mutate: func(t *testing.T, p *SyntheticPack) {
				p.Manifest.Worker.SHA256 = strings.Repeat("0", 64)
				if err := p.WriteManifest(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file missing",
			mutate: func(t *testing.T, p *SyntheticPack) {
				if err := os.Remove(filepath.Join(p.Root, "worker/worker.py")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "truncated file",
			mutate: func(t *testing.T, p *SyntheticPack) {
				if err := os.WriteFile(filepath.Join(p.Root, "worker/worker.py"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pack, err := NewSyntheticPack(t.TempDir(), "linux", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pack.Close() })
			tc.mutate(t, pack)
			report, err := pack.Validate("linux", "amd64")
			if err == nil && report.ValidArtifacts == report.TotalArtifacts {
				t.Fatalf("tampered pack passed: %+v", report)
			}
		})
	}
}

func TestSyntheticRuntimePackRejectsUnsafeSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges on Windows")
	}
	for _, tc := range []struct {
		name   string
		target func(*testing.T, *SyntheticPack) string
	}{
		{
			name: "external",
			target: func(t *testing.T, _ *SyntheticPack) string {
				path := filepath.Join(t.TempDir(), "external")
				if err := os.WriteFile(path, []byte("not part of pack"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "broken",
			target: func(t *testing.T, p *SyntheticPack) string {
				return filepath.Join(p.Root, "absent-target")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pack, err := NewSyntheticPack(t.TempDir(), "linux", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pack.Close() })
			worker := filepath.Join(pack.Root, "worker/worker.py")
			if err := os.Remove(worker); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target(t, pack), worker); err != nil {
				t.Skipf("symlink not available: %v", err)
			}
			report, err := pack.Validate("linux", "amd64")
			if err == nil && report.ValidArtifacts == report.TotalArtifacts {
				t.Fatalf("unsafe symlink pack passed: %+v", report)
			}
		})
	}
}

func TestSandboxUsesCleanEnvironmentAndPrivatePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based environment assertion is Unix-only")
	}
	sandbox, err := NewSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	sh, err := filepath.Abs("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Env(map[string]string{"PYTHONPATH": "/host/python"}); err == nil {
		t.Fatal("accepted Python environment contamination override")
	}
	if _, err := sandbox.Env(map[string]string{"GEOCAM_TEST_TARGET_ARCH": "arm64"}); err != nil {
		t.Fatalf("rejected allowlisted target variable: %v", err)
	}
	result, err := sandbox.Run(context.Background(), sh, "-c", `test -z "${PYTHONPATH+x}" && ! command -v python3 >/dev/null 2>&1`)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("host Python or PYTHONPATH leaked into sandbox: result=%+v err=%v", result, err)
	}
}

func TestSandboxTimeoutAndPyPIAttemptTrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell subprocess tests are Unix-only")
	}
	sandbox, err := NewSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	sh, err := filepath.Abs("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	sleep, err := filepath.Abs("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandbox.Run(ctx, sleep, "2")
	if err != nil || !result.TimedOut {
		t.Fatalf("expected timed out subprocess, result=%+v err=%v", result, err)
	}

	marker := filepath.Join(sandbox.Root, "pypi-attempt")
	trap := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\nexit 97\n"
	if _, err := sandbox.WriteExecutable("pip", trap); err != nil {
		t.Fatal(err)
	}
	result, err = sandbox.Run(context.Background(), sh, "-c", "pip install example")
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("PyPI trap did not fail closed: result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(data), "install example") {
		t.Fatalf("attempted pip invocation was not captured: data=%q err=%v", data, err)
	}
}

func TestReportIsDeterministicAndDoesNotPromotePending(t *testing.T) {
	report := NewReport("artifact-integrity-only")
	if err := report.Add(Check{ID: "runtime-imports", Category: "runtime", Status: StatusPending, Blocker: "integrated runtime pack required"}); err != nil {
		t.Fatal(err)
	}
	if err := report.Add(Check{ID: "manifest-schema", Category: "manifest", Status: StatusPassed, Evidence: "synthetic manifest parsed"}); err != nil {
		t.Fatal(err)
	}
	if err := report.Add(Check{ID: "manifest-schema", Category: "manifest", Status: StatusPassed}); err == nil {
		t.Fatal("accepted duplicate check ID")
	}
	var first, second bytes.Buffer
	if err := report.WriteJSON(&first); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteJSON(&second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("report is not deterministic:\n%s\n%s", first.Bytes(), second.Bytes())
	}
	var decoded Report
	if err := json.Unmarshal(first.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Checks[0].ID != "manifest-schema" || decoded.Checks[1].Status != StatusPending {
		t.Fatalf("pending evidence lost or checks unsorted: %+v", decoded.Checks)
	}
}

func TestFaultPlanInjectsAtRequestedOccurrence(t *testing.T) {
	want := os.ErrPermission
	plan := &FaultPlan{Operation: "rename", Occurrence: 2, Err: want}
	if err := plan.Before("write"); err != nil {
		t.Fatalf("unrelated operation failed: %v", err)
	}
	if err := plan.Before("rename"); err != nil {
		t.Fatalf("first matching operation failed: %v", err)
	}
	if err := plan.Before("rename"); !errors.Is(err, want) {
		t.Fatalf("second matching operation error = %v, want %v", err, want)
	}
}
