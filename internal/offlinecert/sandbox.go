package offlinecert

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxCapturedOutput = 16 * 1024

// Sandbox owns an isolated home, temp directory and PATH for a child process.
// It intentionally inherits no host environment variables.
type Sandbox struct {
	Root string
	Bin  string
}

type CommandResult struct {
	ExitCode int           `json:"exit_code"`
	TimedOut bool          `json:"timed_out"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Elapsed  time.Duration `json:"-"`
}

func NewSandbox(parent string) (*Sandbox, error) {
	root, err := os.MkdirTemp(parent, "geocam-offline-cert-")
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{bin, filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	return &Sandbox{Root: root, Bin: bin}, nil
}

func (s *Sandbox) Close() error {
	if s == nil || s.Root == "" {
		return nil
	}
	return os.RemoveAll(s.Root)
}

// Env returns a deterministic, non-inheriting environment. Caller-provided
// values are intentionally limited to variables relevant to the test process.
func (s *Sandbox) Env(overrides map[string]string) ([]string, error) {
	if s == nil || s.Root == "" {
		return nil, errors.New("sandbox is not initialized")
	}
	allowed := map[string]bool{
		"LANG": true, "LC_ALL": true, "GEOCAM_TEST_TARGET_OS": true,
		"GEOCAM_TEST_TARGET_ARCH": true, "GEOCAM_TEST_MODE": true,
	}
	values := map[string]string{
		"PATH": s.Bin, "HOME": filepath.Join(s.Root, "home"),
		"TMPDIR": filepath.Join(s.Root, "tmp"), "TMP": filepath.Join(s.Root, "tmp"),
		"TEMP": filepath.Join(s.Root, "tmp"), "LANG": "C", "LC_ALL": "C",
	}
	for key, value := range overrides {
		if !allowed[key] {
			return nil, errors.New("environment override is not allowlisted")
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env, nil
}

// Run executes an absolute program path with only the sandbox environment.
// Output is capped to bound report size; no shell is inserted implicitly.
func (s *Sandbox) Run(ctx context.Context, program string, args ...string) (CommandResult, error) {
	if !filepath.IsAbs(program) {
		return CommandResult{}, errors.New("program path must be absolute")
	}
	env, err := s.Env(nil)
	if err != nil {
		return CommandResult{}, err
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = s.Root
	cmd.Env = env
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err = cmd.Run()
	result := CommandResult{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), Elapsed: time.Since(start)}
	if err == nil {
		return result, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit == 0 {
		b.limit = maxCapturedOutput
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		_, _ = b.Buffer.WriteString("\n[output truncated]")
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func (s *Sandbox) WriteExecutable(name, contents string) (string, error) {
	if s == nil || filepath.Base(name) != name || strings.TrimSpace(name) == "" {
		return "", errors.New("executable name must be a single path component")
	}
	path := filepath.Join(s.Bin, name)
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		return "", err
	}
	return path, nil
}
