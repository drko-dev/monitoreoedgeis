package installer_test

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run assemble-release.sh, the last gate before the installer
// release is published, against real cross-compiled binaries packaged the way
// installer-build.yml packages them.

const (
	version = "1.2.3"
	commit  = "0123456789abcdef0123456789abcdef01234567"
)

var platforms = []string{"windows-amd64", "macos-arm64", "linux-amd64", "linux-arm64"}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "file", "unzip", "tar", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// buildBinary cross-compiles a trivial program; cached per GOOS/GOARCH.
var binCache = map[string]string{}

func buildBinary(t *testing.T, goos, goarch string) string {
	t.Helper()
	key := goos + "/" + goarch
	if p, ok := binCache[key]; ok {
		return p
	}
	dir, err := os.MkdirTemp("", "assemble-bin-")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bin")
	cmd := exec.Command("go", "build", "-o", out, src)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", key, err, b)
	}
	binCache[key] = out
	return out
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, mode); err != nil {
		t.Fatal(err)
	}
}

func sha(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type pkgOpts struct {
	version, commit string
	linuxArm64Arch  string // GOARCH put inside the linux-arm64 package
}

// writeArtifacts lays out artifacts/installer-<platform>/ like
// actions/download-artifact does after installer-build.yml.
func writeArtifacts(t *testing.T, o pkgOpts) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range platforms {
		dir := filepath.Join(root, "installer-"+p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var asset string
		switch p {
		case "windows-amd64":
			asset = "geocam-edge-installer-windows-amd64.exe"
			copyFile(t, buildBinary(t, "windows", "amd64"), filepath.Join(dir, asset), 0o755)
		case "macos-arm64":
			asset = "geocam-edge-installer-macos-arm64.zip"
			writeAppZip(t, filepath.Join(dir, asset), buildBinary(t, "darwin", "arm64"), o.version)
		default:
			arch := strings.TrimPrefix(p, "linux-")
			if p == "linux-arm64" && o.linuxArm64Arch != "" {
				arch = o.linuxArm64Arch
			}
			asset = "geocam-edge-installer-" + p + ".tar.gz"
			stage := t.TempDir()
			name := "geocam-edge-installer-" + o.version + "-" + p
			copyFile(t, buildBinary(t, "linux", arch), filepath.Join(stage, name, "geocam-edge-ui"), 0o755)
			if err := os.WriteFile(filepath.Join(stage, name, "VERSION"), []byte(o.version+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if b, err := exec.Command("tar", "-C", stage, "-czf", filepath.Join(dir, asset), name).CombinedOutput(); err != nil {
				t.Fatalf("tar: %v\n%s", err, b)
			}
		}
		info := fmt.Sprintf("version=%s\ncommit=%s\nasset=%s\nsha256=%s\n", o.version, o.commit, asset, sha(t, filepath.Join(dir, asset)))
		if err := os.WriteFile(filepath.Join(dir, "BUILD_INFO-"+p+".txt"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeAppZip(t *testing.T, dst, bin, ver string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	binBytes, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	plist := "<plist><dict>\n<key>CFBundleShortVersionString</key>\n<string>" + ver + "</string>\n</dict></plist>\n"
	for name, body := range map[string][]byte{
		"geocam-edge-ui.app/Contents/MacOS/geocam-edge-ui": binBytes,
		"geocam-edge-ui.app/Contents/Info.plist":           []byte(plist),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func runAssemble(t *testing.T, artifacts string) (string, string, error) {
	t.Helper()
	out := t.TempDir()
	b, err := exec.Command("bash", "assemble-release.sh", version, commit, artifacts, out).CombinedOutput()
	return out, string(b), err
}

func TestAssembleRelease_AcceptsFourConsistentPackages(t *testing.T) {
	requireTools(t)
	out, log, err := runAssemble(t, writeArtifacts(t, pkgOpts{version: version, commit: commit}))
	if err != nil {
		t.Fatalf("assemble failed: %v\n%s", err, log)
	}
	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 5 || strings.Count(string(sums), "\n") != 4 {
		t.Fatalf("want 4 packages + SHA256SUMS.txt, got %d files:\n%s", len(entries), sums)
	}
	for _, p := range platforms {
		if !strings.Contains(string(sums), "geocam-edge-installer-"+p) {
			t.Errorf("SHA256SUMS.txt lacks %s:\n%s", p, sums)
		}
	}
}

func TestAssembleRelease_FailsClosed(t *testing.T) {
	requireTools(t)
	cases := map[string]struct {
		opts   pkgOpts
		mutate func(t *testing.T, artifacts string)
		want   string
	}{
		"missing platform": {
			opts:   pkgOpts{version: version, commit: commit},
			mutate: func(t *testing.T, a string) { os.RemoveAll(filepath.Join(a, "installer-macos-arm64")) },
			want:   "macos-arm64: no build artifact",
		},
		"different commit": {
			opts: pkgOpts{version: version, commit: strings.Repeat("f", 40)},
			want: "expected '" + commit + "'",
		},
		"different version": {
			opts: pkgOpts{version: "1.2.4", commit: commit},
			want: "built as version '1.2.4'",
		},
		"package changed after build": {
			opts: pkgOpts{version: version, commit: commit},
			mutate: func(t *testing.T, a string) {
				p := filepath.Join(a, "installer-windows-amd64", "geocam-edge-installer-windows-amd64.exe")
				f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.Write([]byte("x"))
				f.Close()
			},
			want: "does not match the SHA-256",
		},
		"wrong architecture": {
			opts: pkgOpts{version: version, commit: commit, linuxArm64Arch: "amd64"},
			want: "linux-arm64: binary is not ELF ARM aarch64",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := writeArtifacts(t, tc.opts)
			if tc.mutate != nil {
				tc.mutate(t, a)
			}
			out, log, err := runAssemble(t, a)
			if err == nil {
				t.Fatalf("assemble succeeded; it must fail closed.\n%s", log)
			}
			if !strings.Contains(log, tc.want) {
				t.Errorf("want error containing %q, got:\n%s", tc.want, log)
			}
			if _, err := os.Stat(filepath.Join(out, "SHA256SUMS.txt")); err == nil {
				t.Error("SHA256SUMS.txt was written despite the failure")
			}
		})
	}
}
