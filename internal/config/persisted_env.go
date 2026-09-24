package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const ConfigFileEnv = "GEOCAM_CONFIG_FILE"

var persistedEnvMu sync.Mutex

// PersistentConfigPath returns the selected non-secret operator config path.
// GEOCAM_CONFIG_FILE overrides the per-user default.
func PersistentConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(ConfigFileEnv)); path != "" {
		return path, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: locate user config directory: %w", err)
	}
	return filepath.Join(configDir, "geocam-edge", "edge.env"), nil
}

// PersistentValue reports an explicitly configured value from the process
// environment or the persistent non-secret config file.
func PersistentValue(key string) (string, bool, error) {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value, true, nil
	}
	if _, allowed := persistentConfigKeys[key]; !allowed {
		return "", false, fmt.Errorf("config: %s is not a supported persistent setting", key)
	}
	values, err := readPersistentEnvironment()
	if err != nil {
		return "", false, err
	}
	value, ok := values[key]
	return value, ok, nil
}

// PersistentFileValue returns only a value stored in the config file, ignoring
// process-environment overrides. Service installers use it to ensure the
// configuration will still be present after the interactive shell exits.
func PersistentFileValue(key string) (string, bool, error) {
	if _, allowed := persistentConfigKeys[key]; !allowed {
		return "", false, fmt.Errorf("config: %s is not a supported persistent setting", key)
	}
	path, err := PersistentConfigPath()
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(os.Getenv(ConfigFileEnv)) == "" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
	}
	values, err := readPersistentEnvironment()
	if err != nil {
		return "", false, err
	}
	value, ok := values[key]
	return value, ok, nil
}

// persistentConfigKeys is the allowlist for the non-secret operator config
// file. Enrollment tokens and device/camera credentials are deliberately not
// representable here; credential material stays in its existing protected
// storage and enrollment input remains one-shot.
var persistentConfigKeys = map[string]struct{}{
	"GEOCAM_SAAS_URL": {}, "GEOCAM_PROCESSING_MODE": {}, "GEOCAM_LOG_LEVEL": {},
	"GEOCAM_HEARTBEAT_INTERVAL": {}, "GEOCAM_HEARTBEAT_AUTH_FAILURE_INTERVAL": {},
	"GEOCAM_DATA_DIR": {}, "GEOCAM_HEALTH_ADDR": {}, "GEOCAM_ALLOW_INSECURE_HTTP": {},
	"GEOCAM_OTA_PUBLIC_KEY_FILE": {}, "GEOCAM_SAAS_TIMEOUT": {},
	"GEOCAM_DISCOVERY_ENABLED": {}, "GEOCAM_DISCOVERY_INTERVAL": {},
	"GEOCAM_DISCOVERY_TIMEOUT": {}, "GEOCAM_DISCOVERY_INTERFACES": {},
	"GEOCAM_CONNECTIVITY_ENABLED": {}, "GEOCAM_STREAM_ROLE": {}, "GEOCAM_STREAM_TIMEOUT": {},
	"GEOCAM_VIDEO_PIPELINE_ENABLED": {}, "GEOCAM_VIDEO_TARGET_FPS": {},
	"GEOCAM_VIDEO_OUTPUT_WIDTH": {}, "GEOCAM_VIDEO_OUTPUT_HEIGHT": {},
	"GEOCAM_VIDEO_RINGBUFFER_SIZE": {}, "GEOCAM_VIDEO_QUEUE_DEPTH": {},
	"GEOCAM_VIDEO_DECODE_QUEUE_DEPTH": {}, "GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES": {},
	"GEOCAM_VIDEO_FFMPEG_PATH": {}, "GEOCAM_VIDEO_DECODE_TIMEOUT": {},
	"GEOCAM_VIDEO_HYBRID_MOTION_THRESHOLD": {}, "GEOCAM_VIDEO_HYBRID_MIN_CHANGED_AREA": {},
	"GEOCAM_VIDEO_HYBRID_BLOCK_SIZE": {}, "GEOCAM_VIDEO_HYBRID_IDLE_FPS": {},
	"GEOCAM_VIDEO_HYBRID_IDLE_AFTER": {}, "GEOCAM_VIDEO_HYBRID_ROI": {},
	"GEOCAM_CLOUD_BUFFER_MAX_BYTES": {}, "GEOCAM_CLOUD_BUFFER_MAX_FRAMES": {},
	"GEOCAM_CLOUD_BUFFER_MAX_AGE": {}, "GEOCAM_CLOUD_JPEG_QUALITY": {},
	"GEOCAM_CLOUD_MAX_BYTES_PER_SEC": {}, "GEOCAM_CLOUD_BURST_BYTES": {},
	"GEOCAM_CLOUD_MAX_FPS": {}, "GEOCAM_LOCAL_EVENT_BACKLOG_MAX_OPERATIONS": {},
	"GEOCAM_LOCAL_EVENT_BACKLOG_MAX_BYTES": {}, "GEOCAM_EDGE_MAX_CLIP_SIZE_BYTES": {},
	"GEOCAM_EDGE_YOLO_WORKER_CMD": {}, "GEOCAM_EDGE_YOLO_WORKER_ARGS": {},
	"GEOCAM_EDGE_YOLO_MODELS_DIR": {}, "GEOCAM_EDGE_YOLO_PERSON_MODEL": {},
	"GEOCAM_EDGE_YOLO_VEHICLE_MODEL": {}, "GEOCAM_EDGE_YOLO_SOCKET_PATH": {},
	"GEOCAM_EDGE_YOLO_DEVICE": {}, "GEOCAM_EDGE_YOLO_IMGSZ": {},
	"GEOCAM_EDGE_YOLO_PERSON_CONFIDENCE": {}, "GEOCAM_EDGE_YOLO_VEHICLE_CONFIDENCE": {},
	"GEOCAM_EDGE_YOLO_NMS_IOU": {}, "GEOCAM_EDGE_YOLO_START_TIMEOUT": {},
	"GEOCAM_EDGE_YOLO_INFER_TIMEOUT": {}, "GEOCAM_EDGE_MAX_CONCURRENT_INFERENCE": {},
	"GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH": {}, "GEOCAM_EDGE_MIN_FREE_DISK_BYTES": {},
	"GEOCAM_EDGE_MAX_MEMORY_PERCENT": {}, "GEOCAM_EDGE_RETENTION_EVICT_PENDING": {},
	"GEOCAM_EDGE_RETENTION_SWEEP_INTERVAL": {},
	"GEOCAM_EDGE_RETENTION_MAX_EVENTS":     {}, "GEOCAM_EDGE_RETENTION_MAX_EVENT_BYTES": {},
	"GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE": {}, "GEOCAM_EDGE_RETENTION_MAX_CAPTURES": {},
	"GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES": {}, "GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE": {},
	"GEOCAM_EDGE_RETENTION_MAX_CLIPS": {}, "GEOCAM_EDGE_RETENTION_MAX_CLIP_BYTES": {},
	"GEOCAM_EDGE_RETENTION_MAX_CLIP_AGE": {},
}

// withPersistentEnvironment temporarily overlays persistent non-secret
// settings only where the process environment has not explicitly set a value.
// Load holds a mutex around parsing and restoration so concurrent config loads
// cannot observe a partially applied overlay.
func withPersistentEnvironment(parse func() (*Config, error)) (*Config, error) {
	persistedEnvMu.Lock()
	defer persistedEnvMu.Unlock()

	values, err := readPersistentEnvironment()
	if err != nil {
		return nil, err
	}
	var changed []string
	defer func() {
		for i := len(changed) - 1; i >= 0; i-- {
			key := changed[i]
			_ = os.Unsetenv(key)
		}
	}()
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return nil, fmt.Errorf("config: apply persistent setting %s: %w", key, err)
		}
		changed = append(changed, key)
	}
	return parse()
}

func readPersistentEnvironment() (map[string]string, error) {
	path := strings.TrimSpace(os.Getenv(ConfigFileEnv))
	explicitPath := path != ""
	if !explicitPath {
		defaultPath, err := PersistentConfigPath()
		if err != nil {
			return nil, err
		}
		path = defaultPath
	}

	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicitPath {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: open persistent config: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("config: %s:%d must be KEY=VALUE", path, lineNumber)
		}
		if _, allowed := persistentConfigKeys[key]; !allowed {
			return nil, fmt.Errorf("config: %s:%d contains unsupported or secret setting %s", path, lineNumber, key)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("config: %s:%d has invalid quoted value for %s", path, lineNumber, key)
			}
			value = parsed
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("config: %s:%d duplicates setting %s", path, lineNumber, key)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: read persistent config: %w", err)
	}
	return values, nil
}
