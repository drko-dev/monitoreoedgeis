package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockFileInfo implements os.FileInfo for testing
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() any           { return nil }

// Test Case A: Configured override válido -> Ready=true, Source=configured.
func TestCaseA_ConfiguredOverrideValid(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "custom-bin")
	_ = os.MkdirAll(binDir, 0755)

	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	ffprobePath := filepath.Join(binDir, "ffprobe")
	_ = os.WriteFile(ffmpegPath, []byte("#!/bin/sh\n"), 0755)
	_ = os.WriteFile(ffprobePath, []byte("#!/bin/sh\n"), 0755)

	t.Setenv("GEOCAM_VIDEO_FFMPEG_PATH", ffmpegPath)

	resolver := NewDependencyResolver(tempDir)
	resolver.getConfiguredPath = func() string { return ffmpegPath }
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "ffmpeg") {
			return []byte("ffmpeg version 7.1 Copyright (c) 2000-2024\n"), nil
		}
		if strings.HasSuffix(name, "ffprobe") {
			return []byte("ffprobe version 7.1 Copyright (c) 2000-2024\n"), nil
		}
		return nil, errors.New("command not found")
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true, got false (message: %s)", status.Message)
	}
	if status.Source != SourceConfigured {
		t.Fatalf("expected Source=%s, got %s", SourceConfigured, status.Source)
	}
	if status.FFmpegPath != ffmpegPath {
		t.Fatalf("expected FFmpegPath=%s, got %s", ffmpegPath, status.FFmpegPath)
	}
	if !strings.Contains(status.FFmpegVersion, "7.1") {
		t.Fatalf("expected version 7.1, got %s", status.FFmpegVersion)
	}
}

// Test Case B: Configured override inválido -> no lo acepta, busca en los siguientes.
func TestCaseB_ConfiguredOverrideInvalidFallsBack(t *testing.T) {
	tempDir := t.TempDir()
	resolver := NewDependencyResolver(tempDir)
	resolver.getConfiguredPath = func() string { return "/non/existent/path/ffmpeg" }
	// Mock LookPath returning a valid system ffmpeg/ffprobe
	resolver.lookPath = func(file string) (string, error) {
		if file == "ffmpeg" {
			return "/usr/bin/ffmpeg", nil
		}
		if file == "ffprobe" {
			return "/usr/bin/ffprobe", nil
		}
		return "", errors.New("not found")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		if strings.HasPrefix(name, "/non/existent") {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasPrefix(name, "/non/existent") {
			return nil, errors.New("no such file")
		}
		return []byte("ffmpeg version 6.1\n"), nil
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true after fallback, got false")
	}
	if status.Source != SourcePath {
		t.Fatalf("expected fallback Source=%s, got %s", SourcePath, status.Source)
	}
	if status.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Fatalf("expected /usr/bin/ffmpeg, got %s", status.FFmpegPath)
	}
}

// Test Case C: PATH contiene ffmpeg válido -> Ready=true, Source=path.
func TestCaseC_PathContainsValidFFmpeg(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.lookPath = func(file string) (string, error) {
		if file == "ffmpeg" {
			return "/usr/local/bin/ffmpeg", nil
		}
		if file == "ffprobe" {
			return "/usr/local/bin/ffprobe", nil
		}
		return "", errors.New("not found")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ffmpeg version 6.0\n"), nil
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true, got false")
	}
	if status.Source != SourcePath {
		t.Fatalf("expected Source=%s, got %s", SourcePath, status.Source)
	}
	if status.FFmpegPath != "/usr/local/bin/ffmpeg" {
		t.Fatalf("expected /usr/local/bin/ffmpeg, got %s", status.FFmpegPath)
	}
}

// Test Case D: macOS: PATH vacío/reducido pero ffmpeg existe en /opt/homebrew/bin -> Ready=true, Source=homebrew.
func TestCaseD_MacOSReducedPathOptHomebrew(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.goos = "darwin"
	resolver.goarch = "arm64"
	// Simulate Finder reduced PATH (/usr/bin:/bin:/usr/sbin:/sbin) where ffmpeg is not in PATH
	resolver.lookPath = func(file string) (string, error) {
		return "", errors.New("executable file not found in $PATH")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		if name == "/opt/homebrew/bin/ffmpeg" || name == "/opt/homebrew/bin/ffprobe" {
			return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
		}
		return nil, os.ErrNotExist
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasPrefix(name, "/opt/homebrew/bin/") {
			return []byte("ffmpeg version 7.1.1\n"), nil
		}
		return nil, errors.New("not found")
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true, got false (message: %s)", status.Message)
	}
	if status.Source != SourceHomebrew {
		t.Fatalf("expected Source=%s, got %s", SourceHomebrew, status.Source)
	}
	if status.FFmpegPath != "/opt/homebrew/bin/ffmpeg" {
		t.Fatalf("expected /opt/homebrew/bin/ffmpeg, got %s", status.FFmpegPath)
	}
}

// Test Case E: macOS: PATH vacío/reducido pero ffmpeg existe en /usr/local/bin -> Ready=true, Source=homebrew.
func TestCaseE_MacOSReducedPathUsrLocalBin(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.goos = "darwin"
	resolver.goarch = "amd64"
	resolver.lookPath = func(file string) (string, error) {
		return "", errors.New("executable file not found in $PATH")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		if name == "/usr/local/bin/ffmpeg" || name == "/usr/local/bin/ffprobe" {
			return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
		}
		return nil, os.ErrNotExist
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasPrefix(name, "/usr/local/bin/") {
			return []byte("ffmpeg version 6.1\n"), nil
		}
		return nil, errors.New("not found")
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true, got false (message: %s)", status.Message)
	}
	if status.Source != SourceHomebrew {
		t.Fatalf("expected Source=%s, got %s", SourceHomebrew, status.Source)
	}
	if status.FFmpegPath != "/usr/local/bin/ffmpeg" {
		t.Fatalf("expected /usr/local/bin/ffmpeg, got %s", status.FFmpegPath)
	}
}

// Test Case F: Managed path contiene binario válido -> Ready=true, Source=managed.
func TestCaseF_ManagedPathContainsValidBinary(t *testing.T) {
	tempDir := t.TempDir()
	managedBinDir := filepath.Join(tempDir, "bin")
	_ = os.MkdirAll(managedBinDir, 0755)
	ffmpegPath := filepath.Join(managedBinDir, "ffmpeg")
	ffprobePath := filepath.Join(managedBinDir, "ffprobe")
	_ = os.WriteFile(ffmpegPath, []byte("#!/bin/sh\n"), 0755)
	_ = os.WriteFile(ffprobePath, []byte("#!/bin/sh\n"), 0755)

	resolver := NewDependencyResolver(tempDir)
	resolver.getConfiguredPath = func() string { return "" }
	resolver.lookPath = func(file string) (string, error) {
		return "", errors.New("not found in path")
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasPrefix(name, managedBinDir) {
			return []byte("ffmpeg version 5.1-managed\n"), nil
		}
		return nil, errors.New("not found")
	}

	status := resolver.Resolve(context.Background())
	if !status.Ready {
		t.Fatalf("expected Ready=true, got false (message: %s)", status.Message)
	}
	if status.Source != SourceManaged {
		t.Fatalf("expected Source=%s, got %s", SourceManaged, status.Source)
	}
	if status.FFmpegPath != ffmpegPath {
		t.Fatalf("expected %s, got %s", ffmpegPath, status.FFmpegPath)
	}
}

// Test Case G: Binario existe pero no es ejecutable -> Ready=false.
func TestCaseG_BinaryExistsButNotExecutable(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.lookPath = func(file string) (string, error) {
		return "/usr/bin/ffmpeg", nil
	}
	// Non-executable permissions (0644)
	resolver.stat = func(name string) (os.FileInfo, error) {
		return mockFileInfo{name: filepath.Base(name), mode: 0644}, nil
	}

	status := resolver.Resolve(context.Background())
	if status.Ready {
		t.Fatalf("expected Ready=false for non-executable file, got true")
	}
}

// Test Case H: Binario ejecutable pero `ffmpeg -version` falla o timeout -> Ready=false.
func TestCaseH_ExecutableFailsVersionCheck(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
	}
	// execCmd returns error / timeout
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("signal: killed (timeout)")
	}

	status := resolver.Resolve(context.Background())
	if status.Ready {
		t.Fatalf("expected Ready=false when version execution fails, got true")
	}
}

// Test Case I: ffmpeg existe pero ffprobe falta -> Ready=false.
func TestCaseI_FFmpegExistsFFprobeMissing(t *testing.T) {
	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	resolver.lookPath = func(file string) (string, error) {
		if file == "ffmpeg" {
			return "/usr/bin/ffmpeg", nil
		}
		return "", errors.New("ffprobe not found")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		if strings.Contains(name, "ffmpeg") {
			return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
		}
		return nil, os.ErrNotExist
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(name, "ffmpeg") {
			return []byte("ffmpeg version 7.0\n"), nil
		}
		return nil, errors.New("not found")
	}

	status := resolver.Resolve(context.Background())
	if status.Ready {
		t.Fatalf("expected Ready=false when ffprobe is missing, got true")
	}
	if !status.FFmpegFound {
		t.Fatalf("expected FFmpegFound=true")
	}
	if status.FFprobeFound {
		t.Fatalf("expected FFprobeFound=false")
	}
}

// Test Case J: Ninguno existe -> Ready=false, Source=none, install_supported según plataforma.
func TestCaseJ_NoneExists(t *testing.T) {
	// 1. On macOS with Homebrew present
	resolverDarwin := NewDependencyResolver(t.TempDir())
	resolverDarwin.getConfiguredPath = func() string { return "" }
	resolverDarwin.goos = "darwin"
	resolverDarwin.lookPath = func(file string) (string, error) {
		if file == "brew" {
			return "/opt/homebrew/bin/brew", nil
		}
		return "", errors.New("not found")
	}
	resolverDarwin.stat = func(name string) (os.FileInfo, error) {
		if name == "/opt/homebrew/bin/brew" {
			return mockFileInfo{name: "brew", mode: 0755}, nil
		}
		return nil, os.ErrNotExist
	}

	statusDarwin := resolverDarwin.Resolve(context.Background())
	if statusDarwin.Ready {
		t.Fatalf("expected Ready=false")
	}
	if statusDarwin.Source != SourceNone {
		t.Fatalf("expected Source=none, got %s", statusDarwin.Source)
	}
	if !statusDarwin.InstallSupported {
		t.Fatalf("expected InstallSupported=true on macOS with brew")
	}
	if statusDarwin.InstallMethod != "homebrew" {
		t.Fatalf("expected InstallMethod=homebrew, got %s", statusDarwin.InstallMethod)
	}

	// 2. On Linux
	resolverLinux := NewDependencyResolver(t.TempDir())
	resolverLinux.getConfiguredPath = func() string { return "" }
	resolverLinux.goos = "linux"
	resolverLinux.lookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	resolverLinux.stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}

	statusLinux := resolverLinux.Resolve(context.Background())
	if statusLinux.Ready {
		t.Fatalf("expected Ready=false")
	}
	if statusLinux.InstallSupported {
		t.Fatalf("expected InstallSupported=false on Linux (manual instructions)")
	}
	if statusLinux.InstallMethod != "manual" {
		t.Fatalf("expected InstallMethod=manual, got %s", statusLinux.InstallMethod)
	}
}

// Test Case K: Homebrew install command: se ejecuta exactamente `brew install ffmpeg` sin shell, sin argumentos extras.
func TestCaseK_HomebrewInstallExactArguments(t *testing.T) {
	tempDir := t.TempDir()

	resolver := NewDependencyResolver(tempDir)
	resolver.getConfiguredPath = func() string { return "" }
	resolver.goos = "darwin"
	resolver.goarch = "arm64"

	var executedCmd string
	var executedArgs []string
	isInstalled := false

	resolver.lookPath = func(file string) (string, error) {
		if file == "brew" {
			return "/opt/homebrew/bin/brew", nil
		}
		if isInstalled && (file == "ffmpeg" || file == "ffprobe") {
			return "/opt/homebrew/bin/" + file, nil
		}
		return "", errors.New("not found")
	}
	resolver.stat = func(name string) (os.FileInfo, error) {
		if name == "/opt/homebrew/bin/brew" {
			return mockFileInfo{name: "brew", mode: 0755}, nil
		}
		if isInstalled && strings.HasPrefix(name, "/opt/homebrew/bin/") {
			return mockFileInfo{name: filepath.Base(name), mode: 0755}, nil
		}
		return nil, os.ErrNotExist
	}
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if isInstalled {
			return []byte("ffmpeg version 7.1\n"), nil
		}
		return nil, errors.New("not found")
	}

	installer := NewDependencyInstaller(resolver)
	installer.brewRunner = func(ctx context.Context, brewPath string, args ...string) ([]byte, error) {
		executedCmd = brewPath
		executedArgs = args
		isInstalled = true
		return []byte("Installed ffmpeg\n"), nil
	}

	status, err := installer.Install(context.Background())
	if err != nil {
		t.Fatalf("unexpected install error: %v", err)
	}
	if !status.Ready {
		t.Fatalf("expected Ready=true after install, got false")
	}

	// Verify exact invocation: brewPath, ["install", "ffmpeg"]
	if executedCmd != "/opt/homebrew/bin/brew" {
		t.Fatalf("expected command /opt/homebrew/bin/brew, got %s", executedCmd)
	}
	if len(executedArgs) != 2 || executedArgs[0] != "install" || executedArgs[1] != "ffmpeg" {
		t.Fatalf("expected exact args ['install', 'ffmpeg'], got %v", executedArgs)
	}
}

// Test Case L: Managed download: verifica SHA-256; si hash no coincide, borra temporal y falla.
func TestCaseL_ManagedDownloadHashVerification(t *testing.T) {
	tempDir := t.TempDir()
	content := "fake binary payload"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	validSHA := hex.EncodeToString(hasher.Sum(nil))
	invalidSHA := "0000000000000000000000000000000000000000000000000000000000000000"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()

	destPath := filepath.Join(tempDir, "bin", "downloaded-ffmpeg")

	// 1. Mismatch SHA: must fail and cleanup temp file
	err := downloadAndVerifySHA256(context.Background(), server.URL, invalidSHA, destPath)
	if err == nil {
		t.Fatalf("expected error on SHA-256 mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
	if _, statErr := os.Stat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("destination file should not exist on hash mismatch")
	}

	// 2. Valid SHA: must succeed and create executable destination file
	err = downloadAndVerifySHA256(context.Background(), server.URL, validSHA, destPath)
	if err != nil {
		t.Fatalf("unexpected error with valid SHA: %v", err)
	}
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("destination file missing: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("destination file should have executable bits set (got mode %v)", info.Mode())
	}
}

// TestRealMacFinderPathResolution verifies real execution on the local Mac with reduced Finder PATH.
func TestRealMacFinderPathResolution(t *testing.T) {
	if _, err := os.Stat("/opt/homebrew/bin/ffmpeg"); err != nil {
		t.Skip("local /opt/homebrew/bin/ffmpeg not present on this machine")
	}
	// Simulate Finder reduced PATH (/usr/bin:/bin:/usr/sbin:/sbin)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	t.Setenv("GEOCAM_VIDEO_FFMPEG_PATH", "")

	resolver := NewDependencyResolver(t.TempDir())
	resolver.getConfiguredPath = func() string { return "" }
	status := resolver.Resolve(context.Background())

	t.Logf("Real Mac Finder PATH resolution result:")
	t.Logf("  Ready:        %v", status.Ready)
	t.Logf("  Source:       %s", status.Source)
	t.Logf("  FFmpegPath:   %s", status.FFmpegPath)
	t.Logf("  FFprobePath:  %s", status.FFprobePath)
	t.Logf("  Version:      %s", status.FFmpegVersion)

	if !status.Ready {
		t.Fatalf("expected Ready=true on real Mac, got false: %s", status.Message)
	}
	if status.Source != SourceHomebrew {
		t.Fatalf("expected Source=homebrew, got %s", status.Source)
	}
	if status.FFmpegPath != "/opt/homebrew/bin/ffmpeg" {
		t.Fatalf("expected /opt/homebrew/bin/ffmpeg, got %s", status.FFmpegPath)
	}
	if status.FFprobePath != "/opt/homebrew/bin/ffprobe" {
		t.Fatalf("expected /opt/homebrew/bin/ffprobe, got %s", status.FFprobePath)
	}
}
