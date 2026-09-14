// Package config loads GEO CAM Edge configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Config is the full runtime configuration of the agent.
type Config struct {
	EdgeID            string
	ProcessingMode    ProcessingMode
	LogLevel          string
	SaaSURL           string
	HeartbeatInterval time.Duration
	DataDir           string
}

// Defaults. No secrets, no credentials.
const (
	DefaultProcessingMode    = ModeCloud
	DefaultLogLevel          = "info"
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultDataDir           = "/var/lib/geocam-edge"
)

var validLogLevels = []string{"debug", "info", "warn", "error"}

// Load reads configuration from the environment, applying safe defaults.
// An invalid value is a hard startup error.
func Load() (*Config, error) {
	cfg := &Config{
		EdgeID:            strings.TrimSpace(os.Getenv("GEOCAM_EDGE_ID")),
		ProcessingMode:    DefaultProcessingMode,
		LogLevel:          DefaultLogLevel,
		SaaSURL:           strings.TrimSpace(os.Getenv("GEOCAM_SAAS_URL")),
		HeartbeatInterval: DefaultHeartbeatInterval,
		DataDir:           DefaultDataDir,
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_PROCESSING_MODE")); raw != "" {
		mode, err := ParseProcessingMode(strings.ToLower(raw))
		if err != nil {
			return nil, err
		}
		cfg.ProcessingMode = mode
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_LOG_LEVEL")); raw != "" {
		level := strings.ToLower(raw)
		if !slices.Contains(validLogLevels, level) {
			return nil, fmt.Errorf("invalid log level %q: must be one of %s",
				raw, strings.Join(validLogLevels, ", "))
		}
		cfg.LogLevel = level
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_HEARTBEAT_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid heartbeat interval %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("invalid heartbeat interval %q: must be positive", raw)
		}
		cfg.HeartbeatInterval = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DATA_DIR")); raw != "" {
		cfg.DataDir = raw
	}

	return cfg, nil
}
