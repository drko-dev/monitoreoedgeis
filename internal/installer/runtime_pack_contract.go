package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/workerbootstrap"
)

var (
	ErrRuntimePackNotRelease          = errors.New("runtime pack is not a sealed release")
	ErrRuntimePackMediaMissing        = errors.New("runtime pack is missing a required media binary")
	ErrRuntimePackMediaNotExecutable  = errors.New("runtime pack media binary is not executable")
	ErrRuntimePackPythonNotExecutable = errors.New("runtime pack Python interpreter is not executable")
	ErrRuntimePackArtifactsUnverified = errors.New("runtime pack artifacts were not verified")
)

// RuntimePackMediaPaths contains only media executables declared by and resolved
// from a verified Runtime Pack. It never consults PATH, Homebrew, host locations,
// or network sources.
type RuntimePackMediaPaths struct {
	FFmpeg  string
	FFprobe string
}

// ResolveRuntimePackMedia verifies a sealed darwin/arm64 pack and returns its
// bundled FFmpeg and FFprobe paths. The pack's complete artifact set is checked
// before any path is returned; this function does not execute either binary.
func ResolveRuntimePackMedia(manifest *RuntimeManifest, rootDir, targetOS, targetArch string) (RuntimePackMediaPaths, error) {
	var paths RuntimePackMediaPaths
	if manifest == nil {
		return paths, errors.New("runtime manifest is nil")
	}
	if manifest.ManifestType != ManifestTypeRelease {
		return paths, ErrRuntimePackNotRelease
	}
	if err := manifest.ValidateSchema(); err != nil {
		return paths, fmt.Errorf("validate runtime manifest: %w", err)
	}
	if err := manifest.ValidateCompatibility(targetOS, targetArch); err != nil {
		return paths, fmt.Errorf("validate runtime compatibility: %w", err)
	}
	if targetOS != PlatformDarwin || targetArch != ArchArm64 {
		return paths, fmt.Errorf("H4A media pack target must be darwin/arm64: %s/%s", targetOS, targetArch)
	}
	if _, err := manifest.ValidateArtifacts(rootDir); err != nil {
		return paths, fmt.Errorf("verify runtime pack artifacts: %w", err)
	}

	root, err := filepath.Abs(filepath.Clean(rootDir))
	if err != nil {
		return paths, fmt.Errorf("resolve runtime pack root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return paths, fmt.Errorf("resolve runtime pack root: %w", err)
	}

	for _, binary := range manifest.Binaries {
		var dest *string
		switch binary.Name {
		case "ffmpeg":
			dest = &paths.FFmpeg
		case "ffprobe":
			dest = &paths.FFprobe
		default:
			continue
		}
		if *dest != "" {
			return RuntimePackMediaPaths{}, fmt.Errorf("duplicate %s declaration", binary.Name)
		}
		resolved, err := resolvePackArtifact(root, binary.RelativePath)
		if err != nil {
			return RuntimePackMediaPaths{}, fmt.Errorf("resolve bundled %s: %w", binary.Name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return RuntimePackMediaPaths{}, fmt.Errorf("stat bundled %s: %w", binary.Name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return RuntimePackMediaPaths{}, fmt.Errorf("%w: %s", ErrRuntimePackMediaNotExecutable, binary.Name)
		}
		*dest = resolved
	}
	if paths.FFmpeg == "" || paths.FFprobe == "" {
		return RuntimePackMediaPaths{}, ErrRuntimePackMediaMissing
	}
	return paths, nil
}

func resolvePackArtifact(root, relativePath string) (string, error) {
	if err := validateRelativePath(relativePath, "runtime artifact"); err != nil {
		return "", fmt.Errorf("%w: %s", ErrArtifactSecurity, relativePath)
	}
	joined := filepath.Join(root, filepath.FromSlash(relativePath))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s", ErrArtifactSecurity, relativePath)
	}
	return resolved, nil
}

// RuntimePackWorkerLaunchSpec is a non-executing launch contract for the
// existing internal/vision.Worker supervisor. H4A builds this metadata only;
// it does not start Python, load models, or assert inference readiness.
type RuntimePackWorkerLaunchSpec struct {
	RuntimePackRoot   string
	Command           string
	Args              []string
	Environment       []string
	FFmpeg            string
	FFprobe           string
	ModelsDir         string
	StartTimeout      time.Duration
	modelsAuthorized  bool
	artifactsVerified bool
}

// ArtifactsVerified reports whether the launch contract was built after
// validating the pack manifest, every declared artifact, and executable modes.
func (s RuntimePackWorkerLaunchSpec) ArtifactsVerified() bool { return s.artifactsVerified }

func (s RuntimePackWorkerLaunchSpec) RuntimePackRootPath() string { return s.RuntimePackRoot }
func (s RuntimePackWorkerLaunchSpec) CommandPath() string         { return s.Command }
func (s RuntimePackWorkerLaunchSpec) CommandArgs() []string       { return append([]string(nil), s.Args...) }
func (s RuntimePackWorkerLaunchSpec) ProcessEnvironment() []string {
	return append([]string(nil), s.Environment...)
}
func (s RuntimePackWorkerLaunchSpec) PersistentModelsDir() string { return s.ModelsDir }
func (s RuntimePackWorkerLaunchSpec) ProcessStartTimeout() time.Duration {
	return s.StartTimeout
}

// ModelsAuthorized reports whether this release was explicitly authorized to
// ship a model pack. H4B keeps this false until model provenance and licensing
// are approved; a healthy worker must not imply authorized inference.
func (s RuntimePackWorkerLaunchSpec) ModelsAuthorized() bool { return s.modelsAuthorized }

// BuildRuntimePackWorkerLaunchSpec creates a hermetic command/environment
// contract from verified pack paths and caller-authorized persistent paths.
// Python's isolated mode and a pack-only PATH prevent host Python packages and
// host FFmpeg from becoming implicit fallbacks.
func BuildRuntimePackWorkerLaunchSpec(manifest *RuntimeManifest, rootDir, modelsDir, stateDir, targetOS, targetArch string) (RuntimePackWorkerLaunchSpec, error) {
	var spec RuntimePackWorkerLaunchSpec
	if manifest == nil {
		return spec, errors.New("runtime manifest is nil")
	}
	media, err := ResolveRuntimePackMedia(manifest, rootDir, targetOS, targetArch)
	if err != nil {
		return spec, err
	}
	if !filepath.IsAbs(modelsDir) {
		return spec, errors.New("persistent models directory must be an absolute path")
	}
	if !filepath.IsAbs(stateDir) {
		return spec, errors.New("persistent worker state directory must be an absolute path")
	}
	root, err := filepath.Abs(filepath.Clean(rootDir))
	if err != nil {
		return spec, fmt.Errorf("resolve runtime pack root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return spec, fmt.Errorf("resolve runtime pack root: %w", err)
	}
	pythonPath, err := resolvePackArtifact(root, manifest.Python.RelativeBinaryPath)
	if err != nil {
		return spec, fmt.Errorf("resolve bundled Python: %w", err)
	}
	pythonInfo, err := os.Stat(pythonPath)
	if err != nil {
		return spec, fmt.Errorf("stat bundled Python: %w", err)
	}
	if !pythonInfo.Mode().IsRegular() || pythonInfo.Mode().Perm()&0o111 == 0 {
		return spec, ErrRuntimePackPythonNotExecutable
	}
	workerPath, err := resolvePackArtifact(root, manifest.Worker.Entrypoint)
	if err != nil {
		return spec, fmt.Errorf("resolve bundled worker: %w", err)
	}
	// Python -I will not include worker.py's own directory. The fixed
	// bootstrap imports backend.py from that verified pack directory only.
	// A release must additionally authenticate backend.py in the full
	// artifact inventory; file existence alone is not an integrity proof.
	backendRel := filepath.ToSlash(filepath.Join(filepath.Dir(manifest.Worker.Entrypoint), "backend.py"))
	if filepath.ToSlash(filepath.Clean(manifest.Worker.BackendRelativePath)) != backendRel ||
		manifest.Worker.BackendSHA256 == "" || manifest.Worker.BackendSizeBytes <= 0 {
		return spec, fmt.Errorf("%w: hermetic worker backend is not pinned in the manifest", ErrRuntimePackArtifactsUnverified)
	}
	backendPath, err := resolvePackArtifact(root, backendRel)
	if err != nil {
		return spec, fmt.Errorf("resolve bundled worker backend: %w", err)
	}
	backendInfo, err := os.Stat(backendPath)
	if err != nil || !backendInfo.Mode().IsRegular() {
		return spec, fmt.Errorf("%w: bundled worker backend is missing or invalid", ErrRuntimePackArtifactsUnverified)
	}
	cleanModelsDir := filepath.Clean(modelsDir)
	cleanStateDir := filepath.Clean(stateDir)
	mediaDir := filepath.Dir(media.FFmpeg)
	return RuntimePackWorkerLaunchSpec{
		RuntimePackRoot:   root,
		Command:           pythonPath,
		Args:              workerbootstrap.Args(workerPath),
		Environment:       []string{"PATH=" + mediaDir, "HOME=" + cleanStateDir, "YOLO_CONFIG_DIR=" + cleanStateDir, "TMPDIR=" + filepath.Join(cleanStateDir, "tmp"), "GEOCAM_VIDEO_FFMPEG_PATH=" + media.FFmpeg, "GEOCAM_VIDEO_FFPROBE_PATH=" + media.FFprobe, "GEOCAM_EDGE_YOLO_MODELS_DIR=" + cleanModelsDir},
		FFmpeg:            media.FFmpeg,
		FFprobe:           media.FFprobe,
		ModelsDir:         cleanModelsDir,
		StartTimeout:      30 * time.Second,
		modelsAuthorized:  false,
		artifactsVerified: true,
	}, nil
}
