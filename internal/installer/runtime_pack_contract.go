package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrRuntimePackNotRelease          = errors.New("runtime pack is not a sealed release")
	ErrRuntimePackMediaMissing        = errors.New("runtime pack is missing a required media binary")
	ErrRuntimePackMediaNotExecutable  = errors.New("runtime pack media binary is not executable")
	ErrRuntimePackPythonNotExecutable = errors.New("runtime pack Python interpreter is not executable")
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
	Command      string
	Args         []string
	Environment  []string
	FFmpeg       string
	FFprobe      string
	ModelsDir    string
	StartTimeout time.Duration
}

// BuildRuntimePackWorkerLaunchSpec creates a hermetic command/environment
// contract from verified pack paths and caller-authorized persistent paths.
// Python's isolated mode and a pack-only PATH prevent host Python packages and
// host FFmpeg from becoming implicit fallbacks.
func BuildRuntimePackWorkerLaunchSpec(manifest *RuntimeManifest, rootDir, modelsDir, targetOS, targetArch string) (RuntimePackWorkerLaunchSpec, error) {
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
	cleanModelsDir := filepath.Clean(modelsDir)
	mediaDir := filepath.Dir(media.FFmpeg)
	return RuntimePackWorkerLaunchSpec{
		Command:      pythonPath,
		Args:         []string{"-I", workerPath},
		Environment:  []string{"PATH=" + mediaDir, "GEOCAM_VIDEO_FFMPEG_PATH=" + media.FFmpeg, "GEOCAM_VIDEO_FFPROBE_PATH=" + media.FFprobe, "GEOCAM_EDGE_YOLO_MODELS_DIR=" + cleanModelsDir, "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1"},
		FFmpeg:       media.FFmpeg,
		FFprobe:      media.FFprobe,
		ModelsDir:    cleanModelsDir,
		StartTimeout: 30 * time.Second,
	}, nil
}
