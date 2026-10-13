package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

// DependencySource identifies where the FFmpeg binary was discovered.
type DependencySource string

const (
	SourceConfigured DependencySource = "configured"
	SourcePath       DependencySource = "path"
	SourceHomebrew   DependencySource = "homebrew"
	SourceSystem     DependencySource = "system"
	SourceManaged    DependencySource = "managed"
	SourceNone       DependencySource = "none"
)

// DependencyStatus describes the detected state of FFmpeg and FFprobe on the host.
type DependencyStatus struct {
	FFmpegFound      bool             `json:"ffmpeg_found"`
	FFmpegPath       string           `json:"ffmpeg_path"`
	FFmpegVersion    string           `json:"ffmpeg_version"`
	FFprobeFound     bool             `json:"ffprobe_found"`
	FFprobePath      string           `json:"ffprobe_path"`
	Ready            bool             `json:"ready"`
	Source           DependencySource `json:"source"`
	InstallSupported bool             `json:"install_supported"`
	InstallMethod    string           `json:"install_method"` // "homebrew", "managed", "manual"
	Message          string           `json:"message,omitempty"`
}

// CommandExecutor executes an external binary safely without a shell.
type CommandExecutor func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandExecutor runs commands with exec.CommandContext and limits output.
func DefaultCommandExecutor(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		combined := bytes.TrimSpace(append(stdout.Bytes(), stderr.Bytes()...))
		if len(combined) > 0 {
			return combined, fmt.Errorf("%w: %s", err, string(combined))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// DependencyResolver locates and validates FFmpeg and FFprobe on the host.
type DependencyResolver struct {
	lookPath          func(file string) (string, error)
	stat              func(name string) (os.FileInfo, error)
	execCmd           CommandExecutor
	getConfiguredPath func() string
	dataDir           string
	goos              string
	goarch            string
}

// NewDependencyResolver creates a DependencyResolver using the real OS environment.
func NewDependencyResolver(dataDir string) *DependencyResolver {
	return &DependencyResolver{
		lookPath: exec.LookPath,
		stat:     os.Stat,
		execCmd:  DefaultCommandExecutor,
		dataDir:  dataDir,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
	}
}

// exeName appends .exe on Windows.
func (r *DependencyResolver) exeName(base string) string {
	if r.goos == "windows" {
		return base + ".exe"
	}
	return base
}

// parseVersionString extracts the first line or version identifier from -version output.
func parseVersionString(output []byte) string {
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ffmpeg version") || strings.HasPrefix(trimmed, "ffprobe version") {
			return trimmed
		}
		if trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

// validateExecutable verifies that a file exists, is executable, and runs <name> -version.
func (r *DependencyResolver) validateExecutable(ctx context.Context, path string) (version string, ok bool) {
	if path == "" {
		return "", false
	}
	info, err := r.stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	// Check execute permission on non-Windows platforms
	if r.goos != "windows" && info.Mode()&0o111 == 0 {
		return "", false
	}

	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := r.execCmd(callCtx, path, "-version")
	if err != nil {
		return "", false
	}
	ver := parseVersionString(out)
	if ver == "" {
		return "", false
	}
	return ver, true
}

// candidateDirs returns known platform directories where FFmpeg is typically located.
func (r *DependencyResolver) candidateDirs() []string {
	var dirs []string
	switch r.goos {
	case "darwin":
		if r.goarch == "arm64" {
			dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
		} else {
			dirs = append(dirs, "/usr/local/bin", "/opt/homebrew/bin")
		}
		// Also probe Homebrew prefix if brew is available
		if brewPath, err := r.findBrew(); err == nil {
			prefixDir := filepath.Dir(brewPath) // e.g. /opt/homebrew/bin
			if !containsString(dirs, prefixDir) {
				dirs = append(dirs, prefixDir)
			}
		}
	case "linux":
		dirs = append(dirs, "/usr/bin", "/usr/local/bin")
	case "windows":
		// On Windows, Program Files or chocolatey/scoop standard paths could exist
		dirs = append(dirs, `C:\ProgramData\chocolatey\bin`, `C:\ffmpeg\bin`)
	}
	return dirs
}

func containsString(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

// findBrew locates the Homebrew executable on macOS.
func (r *DependencyResolver) findBrew() (string, error) {
	if r.goos != "darwin" {
		return "", errors.New("homebrew is only supported on macOS")
	}
	candidates := []string{
		"/opt/homebrew/bin/brew",
		"/usr/local/bin/brew",
	}
	for _, cand := range candidates {
		if info, err := r.stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	if p, err := r.lookPath("brew"); err == nil {
		return p, nil
	}
	return "", errors.New("brew not found")
}

// getConfigured returns the configured FFmpeg path from hooks, env, or persistent config.
func (r *DependencyResolver) getConfigured() string {
	if r.getConfiguredPath != nil {
		return r.getConfiguredPath()
	}
	if v := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_FFMPEG_PATH")); v != "" {
		return v
	}
	cfgPath, hasCfg, _ := config.PersistentValue("GEOCAM_VIDEO_FFMPEG_PATH")
	if hasCfg && strings.TrimSpace(cfgPath) != "" {
		return strings.TrimSpace(cfgPath)
	}
	return ""
}

// Resolve identifies the current FFmpeg and FFprobe status on the host.
func (r *DependencyResolver) Resolve(ctx context.Context) *DependencyStatus {
	status := &DependencyStatus{
		Source: SourceNone,
	}

	// Priority A: Configured override GEOCAM_VIDEO_FFMPEG_PATH
	cfgPath := r.getConfigured()
	if cfgPath != "" {
		if ver, ok := r.validateExecutable(ctx, cfgPath); ok {
			status.FFmpegFound = true
			status.FFmpegPath = cfgPath
			status.FFmpegVersion = ver
			status.Source = SourceConfigured

			// Check ffprobe alongside ffmpeg
			probeCandidate := filepath.Join(filepath.Dir(cfgPath), r.exeName("ffprobe"))
			if _, okProbe := r.validateExecutable(ctx, probeCandidate); okProbe {
				status.FFprobeFound = true
				status.FFprobePath = probeCandidate
			} else if pPath, err := r.lookPath(r.exeName("ffprobe")); err == nil {
				if _, okProbePath := r.validateExecutable(ctx, pPath); okProbePath {
					status.FFprobeFound = true
					status.FFprobePath = pPath
				}
			}

			status.Ready = status.FFmpegFound && status.FFprobeFound
			if !status.FFprobeFound {
				status.Message = "Configured ffmpeg was found, but ffprobe is missing."
			}
			return status
		}
	}

	// Priority B: System PATH
	ffmpegExe := r.exeName("ffmpeg")
	ffprobeExe := r.exeName("ffprobe")
	if pathFFmpeg, err := r.lookPath(ffmpegExe); err == nil {
		if ver, ok := r.validateExecutable(ctx, pathFFmpeg); ok {
			status.FFmpegFound = true
			status.FFmpegPath = pathFFmpeg
			status.FFmpegVersion = ver
			status.Source = SourcePath

			if pathProbe, errProbe := r.lookPath(ffprobeExe); errProbe == nil {
				if _, okProbe := r.validateExecutable(ctx, pathProbe); okProbe {
					status.FFprobeFound = true
					status.FFprobePath = pathProbe
				}
			}

			status.Ready = status.FFmpegFound && status.FFprobeFound
			if !status.FFprobeFound {
				status.Message = "ffmpeg found in PATH, but ffprobe is missing."
			}
			return status
		}
	}

	// Priority C: Known platform locations
	for _, dir := range r.candidateDirs() {
		candidateFFmpeg := filepath.Join(dir, ffmpegExe)
		if ver, ok := r.validateExecutable(ctx, candidateFFmpeg); ok {
			status.FFmpegFound = true
			status.FFmpegPath = candidateFFmpeg
			status.FFmpegVersion = ver

			if r.goos == "darwin" || strings.Contains(dir, "homebrew") {
				status.Source = SourceHomebrew
			} else {
				status.Source = SourceSystem
			}

			candidateProbe := filepath.Join(dir, ffprobeExe)
			if _, okProbe := r.validateExecutable(ctx, candidateProbe); okProbe {
				status.FFprobeFound = true
				status.FFprobePath = candidateProbe
			} else if pathProbe, errProbe := r.lookPath(ffprobeExe); errProbe == nil {
				if _, okProbePath := r.validateExecutable(ctx, pathProbe); okProbePath {
					status.FFprobeFound = true
					status.FFprobePath = pathProbe
				}
			}

			status.Ready = status.FFmpegFound && status.FFprobeFound
			if !status.FFprobeFound {
				status.Message = "ffmpeg found, but ffprobe is missing."
			}
			return status
		}
	}

	// Priority D: GEO CAM Managed location in DataDir
	if r.dataDir != "" {
		managedFFmpeg := filepath.Join(r.dataDir, "bin", ffmpegExe)
		if ver, ok := r.validateExecutable(ctx, managedFFmpeg); ok {
			status.FFmpegFound = true
			status.FFmpegPath = managedFFmpeg
			status.FFmpegVersion = ver
			status.Source = SourceManaged

			managedProbe := filepath.Join(r.dataDir, "bin", ffprobeExe)
			if _, okProbe := r.validateExecutable(ctx, managedProbe); okProbe {
				status.FFprobeFound = true
				status.FFprobePath = managedProbe
			}

			status.Ready = status.FFmpegFound && status.FFprobeFound
			if !status.FFprobeFound {
				status.Message = "Managed ffmpeg found, but ffprobe is missing."
			}
			return status
		}
	}

	// Priority E: Missing dependency — detect install capabilities
	status.FFmpegFound = false
	status.Ready = false
	status.Source = SourceNone

	if r.goos == "darwin" {
		if _, err := r.findBrew(); err == nil {
			status.InstallSupported = true
			status.InstallMethod = "homebrew"
			status.Message = "FFmpeg is not installed. Homebrew is available to install it."
		} else {
			status.InstallSupported = false
			status.InstallMethod = "manual"
			status.Message = "FFmpeg is not installed and Homebrew was not found. Please install FFmpeg via Homebrew or configure GEOCAM_VIDEO_FFMPEG_PATH."
		}
	} else if r.goos == "linux" {
		status.InstallSupported = false
		status.InstallMethod = "manual"
		status.Message = "FFmpeg is not installed. Please install it using your system package manager (e.g. apt install ffmpeg)."
	} else {
		status.InstallSupported = false
		status.InstallMethod = "manual"
		status.Message = "FFmpeg is not installed. Please install FFmpeg and ensure it is available on PATH."
	}

	return status
}

// DependencyInstaller handles safe installation of FFmpeg without shell execution.
type DependencyInstaller struct {
	resolver       *DependencyResolver
	brewRunner     func(ctx context.Context, brewPath string, args ...string) ([]byte, error)
	downloadStream func(ctx context.Context, url string, expectedSHA string, dest string) error
	persistConfig  func(updates map[string]string) error
}

// NewDependencyInstaller creates a DependencyInstaller backed by real commands.
func NewDependencyInstaller(resolver *DependencyResolver) *DependencyInstaller {
	return &DependencyInstaller{
		resolver: resolver,
		brewRunner: func(ctx context.Context, brewPath string, args ...string) ([]byte, error) {
			return resolver.execCmd(ctx, brewPath, args...)
		},
		downloadStream: downloadAndVerifySHA256,
		persistConfig:  config.WritePersistentValues,
	}
}

// Install attempts to install FFmpeg using the safe platform package manager (Homebrew on macOS).
func (in *DependencyInstaller) Install(ctx context.Context) (*DependencyStatus, error) {
	// Rule 14-K: If already ready, do not reinstall.
	current := in.resolver.Resolve(ctx)
	if current.Ready {
		return current, nil
	}

	if in.resolver.goos != "darwin" {
		return current, &SafeError{
			Code:        "PLATFORM_INSTALL_UNSUPPORTED",
			SafeMessage: "Automated installation is only supported on macOS via Homebrew. Please install FFmpeg using your system package manager.",
			Recoverable: true,
		}
	}

	brewPath, err := in.resolver.findBrew()
	if err != nil {
		return current, &SafeError{
			Code:        "HOMEBREW_NOT_FOUND",
			SafeMessage: "Homebrew is not installed on this Mac. Please install Homebrew or install FFmpeg manually.",
			Recoverable: true,
		}
	}

	// Run brew install ffmpeg directly without shell
	installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	_, err = in.brewRunner(installCtx, brewPath, "install", "ffmpeg")
	if err != nil {
		return current, &SafeError{
			Code:        "INSTALL_FAILED",
			SafeMessage: "Homebrew installation of FFmpeg failed.",
			Details:     err.Error(),
			Recoverable: true,
		}
	}

	// Re-resolve
	after := in.resolver.Resolve(ctx)
	if !after.Ready {
		return after, &SafeError{
			Code:        "VERIFICATION_FAILED",
			SafeMessage: "FFmpeg installation completed but the binaries could not be verified.",
			Recoverable: true,
		}
	}

	// Persist GEOCAM_VIDEO_FFMPEG_PATH if resolved path is outside default Finder PATH
	if after.FFmpegPath != "" && in.persistConfig != nil {
		_ = in.persistConfig(map[string]string{
			"GEOCAM_VIDEO_FFMPEG_PATH": after.FFmpegPath,
		})
	}

	return after, nil
}

// InstallManaged verifies checksum and installs a pinned managed binary into a destination.
func (in *DependencyInstaller) InstallManaged(ctx context.Context, url string, expectedSHA string, destFile string) error {
	if url == "" || expectedSHA == "" || destFile == "" {
		return errors.New("url, expected SHA-256 and destination file path are required")
	}
	if in.downloadStream == nil {
		return errors.New("managed download provider is not configured")
	}
	return in.downloadStream(ctx, url, expectedSHA, destFile)
}

// downloadAndVerifySHA256 streams from HTTPS into a temp file, checks SHA-256, and atomically installs.
func downloadAndVerifySHA256(ctx context.Context, rawURL string, expectedSHA string, dest string) error {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://127.0.0.1") && !strings.HasPrefix(rawURL, "http://localhost") {
		return errors.New("managed downloads require https://")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	tempDir := filepath.Dir(dest)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(tempDir, "ffmpeg-download-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		// Clean up temporary file if not atomically committed
		_ = os.Remove(tmpPath)
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	// Stream with size bound (e.g. 200MB max)
	const maxDownloadBytes = 200 * 1024 * 1024
	lr := io.LimitReader(resp.Body, maxDownloadBytes)

	if _, err := io.Copy(writer, lr); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("download error: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSHA, strings.TrimSpace(expectedSHA)) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA, actualSHA)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}

	return os.Rename(tmpPath, dest)
}
