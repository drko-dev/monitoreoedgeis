package vision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrHermeticRuntimePackInvalid    = errors.New("hermetic vision runtime pack is invalid")
	ErrHermeticModelPackUnauthorized = errors.New("hermetic vision model pack is not authorized")
	ErrHermeticModelsUnavailable     = errors.New("hermetic vision models are unavailable")

	ErrHermeticProxyForbidden = fmt.Errorf("%w: proxy environment variable forbidden in hermetic mode", ErrHermeticRuntimePackInvalid)
	ErrHermeticNetworkEscape  = fmt.Errorf("%w: network egress escape attempt detected", ErrHermeticRuntimePackInvalid)
	ErrHermeticDNSForbidden   = fmt.Errorf("%w: external DNS resolution forbidden in hermetic mode", ErrHermeticRuntimePackInvalid)
	ErrHermeticSocketEscape   = fmt.Errorf("%w: unix socket path escapes containment or is insecure", ErrHermeticRuntimePackInvalid)
)

var forbiddenProxyEnvKeys = map[string]bool{
	"HTTP_PROXY":     true,
	"HTTPS_PROXY":    true,
	"ALL_PROXY":      true,
	"FTP_PROXY":      true,
	"NO_PROXY":       true,
	"http_proxy":     true,
	"https_proxy":    true,
	"all_proxy":      true,
	"ftp_proxy":      true,
	"no_proxy":       true,
	"CURL_CA_BUNDLE": true,
	"SSL_CERT_FILE":  true,
}

// DetectForbiddenProxyEnv returns any proxy or network redirection environment variable
// names found in the given slice of KEY=VALUE strings.
func DetectForbiddenProxyEnv(entries []string) []string {
	var detected []string
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if forbiddenProxyEnvKeys[key] || forbiddenProxyEnvKeys[strings.ToUpper(key)] {
			detected = append(detected, key)
		}
	}
	return detected
}

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
	if w.cfg.SocketPath != "" {
		if err := validateHermeticSocketPath(w.cfg.SocketPath, stateRoot); err != nil {
			return err
		}
	}
	return nil
}

func parseHermeticEnvironment(entries []string) (map[string]string, error) {
	if forbidden := DetectForbiddenProxyEnv(entries); len(forbidden) > 0 {
		return nil, fmt.Errorf("%w: detected %s", ErrHermeticProxyForbidden, strings.Join(forbidden, ", "))
	}
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

func validateHermeticSocketPath(socketPath, stateRoot string) error {
	if !filepath.IsAbs(socketPath) {
		return fmt.Errorf("%w: socket path %q must be absolute", ErrHermeticSocketEscape, socketPath)
	}
	cleaned := filepath.Clean(socketPath)
	if fi, err := os.Lstat(cleaned); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: socket path %q is a symlink", ErrHermeticSocketEscape, cleaned)
		}
	}
	dir := filepath.Dir(cleaned)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if fi.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("%w: socket directory %q is world-writable", ErrHermeticSocketEscape, dir)
		}
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err == nil {
		rel, err := filepath.Rel(stateRoot, resolvedDir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%w: socket directory %q escapes state root %q", ErrHermeticSocketEscape, resolvedDir, stateRoot)
		}
	} else {
		rel, err := filepath.Rel(stateRoot, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%w: socket directory %q escapes state root %q", ErrHermeticSocketEscape, dir, stateRoot)
		}
	}
	return nil
}

// ValidateHermeticAddress verifies that a network address or host:port string
// points strictly to the local loopback interface and does not require external DNS.
// Any non-loopback IP, non-localhost hostname, or external DNS target is rejected.
func ValidateHermeticAddress(addr string) error {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return fmt.Errorf("%w: empty network address", ErrHermeticNetworkEscape)
	}
	if idx := strings.Index(trimmed, "://"); idx != -1 {
		trimmed = trimmed[idx+3:]
	}
	if idx := strings.IndexAny(trimmed, "/?#"); idx != -1 {
		trimmed = trimmed[:idx]
	}

	host := trimmed
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")

	if strings.EqualFold(host, "localhost") {
		return nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: hostname %q requires external DNS resolution", ErrHermeticDNSForbidden, host)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("%w: target IP %s is not loopback", ErrHermeticNetworkEscape, ip.String())
	}

	return nil
}

// HermeticDialContext dials a network address only if it is strictly confined to
// local loopback or unix sockets. Any attempt to dial an external IP or hostname
// is blocked before opening a network socket.
func HermeticDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "unix", "unixgram", "unixpacket":
		// No state-root is supplied to this generic network hook, so allowing
		// an arbitrary local socket would permit contacting host relay daemons.
		// Worker IPC uses its separately validated private socket path.
		return nil, ErrHermeticSocketEscape
	case "tcp", "tcp4", "tcp6":
		if err := ValidateHermeticAddress(addr); err != nil {
			return nil, err
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	default:
		return nil, fmt.Errorf("%w: unsupported network protocol %q in hermetic mode", ErrHermeticNetworkEscape, network)
	}
}

// HermeticResolver returns a *net.Resolver that rejects all lookups for non-localhost names
// and refuses to contact external DNS servers.
func HermeticResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := ValidateHermeticAddress(address); err != nil {
				return nil, fmt.Errorf("%w: DNS query to %s blocked: %v", ErrHermeticDNSForbidden, address, err)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, address)
		},
	}
}
