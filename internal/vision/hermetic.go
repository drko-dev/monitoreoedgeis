package vision

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrHermeticRuntimePackInvalid    = errors.New("hermetic vision runtime pack is invalid")
	ErrHermeticModelPackUnauthorized = errors.New("hermetic vision model pack is not authorized")
	ErrHermeticModelsUnavailable     = errors.New("hermetic vision models are unavailable")
)

var hermeticEnvAllowlist = map[string]bool{
	"PATH":                        true,
	"HOME":                        true,
	"TMPDIR":                      true,
	"GEOCAM_VIDEO_FFMPEG_PATH":    true,
	"GEOCAM_VIDEO_FFPROBE_PATH":   true,
	"GEOCAM_EDGE_YOLO_MODELS_DIR": true,
	"LANG":                        true,
	"LC_ALL":                      true,
}

func (w *Worker) validateHermeticConfig() error {
	if !w.cfg.ModelPackAuthorized {
		return ErrHermeticModelPackUnauthorized
	}
	if w.models == nil || !w.models.Ready() {
		return ErrHermeticModelsUnavailable
	}
	if !filepath.IsAbs(w.cfg.RuntimePackRoot) || !filepath.IsAbs(w.cfg.WorkerCmd) || len(w.cfg.WorkerArgs) != 3 || w.cfg.WorkerArgs[0] != "-I" || w.cfg.WorkerArgs[1] != "-B" || !filepath.IsAbs(w.cfg.WorkerArgs[2]) {
		return ErrHermeticRuntimePackInvalid
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(w.cfg.RuntimePackRoot))
	if err != nil {
		return ErrHermeticRuntimePackInvalid
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return ErrHermeticRuntimePackInvalid
	}
	if err := requirePackExecutable(root, w.cfg.WorkerCmd); err != nil {
		return err
	}
	if err := requirePackArtifact(root, w.cfg.WorkerArgs[2]); err != nil {
		return err
	}

	env, err := parseHermeticEnvironment(w.cfg.ProcessEnv)
	if err != nil {
		return err
	}
	for _, required := range []string{"PATH", "HOME", "TMPDIR", "GEOCAM_VIDEO_FFMPEG_PATH", "GEOCAM_VIDEO_FFPROBE_PATH", "GEOCAM_EDGE_YOLO_MODELS_DIR"} {
		if strings.TrimSpace(env[required]) == "" {
			return ErrHermeticRuntimePackInvalid
		}
	}
	for _, name := range []string{"HOME", "TMPDIR", "GEOCAM_EDGE_YOLO_MODELS_DIR"} {
		if !filepath.IsAbs(env[name]) {
			return ErrHermeticRuntimePackInvalid
		}
	}
	for _, name := range []string{"HOME", "TMPDIR"} {
		info, err := os.Stat(env[name])
		if err != nil || !info.IsDir() {
			return ErrHermeticRuntimePackInvalid
		}
	}
	stateRoot, err := filepath.EvalSymlinks(env["HOME"])
	if err != nil {
		return ErrHermeticRuntimePackInvalid
	}
	tempRoot, err := filepath.EvalSymlinks(env["TMPDIR"])
	if err != nil {
		return ErrHermeticRuntimePackInvalid
	}
	tempRel, err := filepath.Rel(stateRoot, tempRoot)
	if err != nil || tempRel == ".." || strings.HasPrefix(tempRel, ".."+string(filepath.Separator)) || filepath.IsAbs(tempRel) {
		return ErrHermeticRuntimePackInvalid
	}
	if filepath.Clean(env["GEOCAM_EDGE_YOLO_MODELS_DIR"]) != filepath.Clean(w.cfg.ModelsDir) || filepath.Clean(w.models.dir) != filepath.Clean(w.cfg.ModelsDir) {
		return ErrHermeticRuntimePackInvalid
	}
	modelDir, err := os.Stat(w.cfg.ModelsDir)
	if err != nil || !modelDir.IsDir() {
		return ErrHermeticModelsUnavailable
	}
	ffmpeg, ffprobe := env["GEOCAM_VIDEO_FFMPEG_PATH"], env["GEOCAM_VIDEO_FFPROBE_PATH"]
	if err := requirePackExecutable(root, ffmpeg); err != nil {
		return err
	}
	if err := requirePackExecutable(root, ffprobe); err != nil {
		return err
	}
	if filepath.Clean(env["PATH"]) != filepath.Dir(filepath.Clean(ffmpeg)) || filepath.Dir(filepath.Clean(ffmpeg)) != filepath.Dir(filepath.Clean(ffprobe)) {
		return ErrHermeticRuntimePackInvalid
	}
	return nil
}

func parseHermeticEnvironment(entries []string) (map[string]string, error) {
	parsed := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || !hermeticEnvAllowlist[key] {
			return nil, ErrHermeticRuntimePackInvalid
		}
		if _, exists := parsed[key]; exists {
			return nil, ErrHermeticRuntimePackInvalid
		}
		parsed[key] = value
	}
	return parsed, nil
}

func requirePackExecutable(root, path string) error {
	resolved, err := resolvePackPath(root, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ErrHermeticRuntimePackInvalid
	}
	return nil
}

func requirePackArtifact(root, path string) error {
	resolved, err := resolvePackPath(root, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return ErrHermeticRuntimePackInvalid
	}
	return nil
}

func resolvePackPath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrHermeticRuntimePackInvalid
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", ErrHermeticRuntimePackInvalid
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: artifact path escapes runtime pack", ErrHermeticRuntimePackInvalid)
	}
	return resolved, nil
}
