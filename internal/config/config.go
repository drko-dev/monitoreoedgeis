// Package config loads GEO CAM Edge configuration from environment variables.
package config

import (
	"fmt"
	"net"
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
	HealthAddr        string
	// AllowInsecureHTTP permits SaaSURL to use http:// instead of https://.
	// It never weakens TLS verification for an https:// URL — see
	// internal/transport. Development only; defaults to false.
	AllowInsecureHTTP bool
	// SaaSTimeout bounds every SaaS HTTP request (enroll, rotate, me).
	SaaSTimeout time.Duration
}

// Defaults. No secrets, no credentials.
const (
	DefaultProcessingMode    = ModeCloud
	DefaultLogLevel          = "info"
	DefaultHeartbeatInterval = 30 * time.Second
	// MinHeartbeatInterval and MaxHeartbeatInterval bound
	// GEOCAM_HEARTBEAT_INTERVAL. Below the minimum a fleet becomes a load
	// source rather than a liveness signal; above the maximum the SaaS would
	// declare the Edge offline between two healthy beats, because the
	// server-side offline threshold is a multiple of the nominal interval.
	MinHeartbeatInterval = 5 * time.Second
	MaxHeartbeatInterval = 5 * time.Minute
	DefaultDataDir       = "/var/lib/geocam-edge"
	// DefaultHealthAddr binds the local health HTTP surface to localhost
	// only: it is not meant to be exposed to the LAN.
	DefaultHealthAddr = "127.0.0.1:8091"
	// DefaultSaaSTimeout bounds SaaS HTTP requests.
	DefaultSaaSTimeout = 10 * time.Second
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
		HealthAddr:        DefaultHealthAddr,
		SaaSTimeout:       DefaultSaaSTimeout,
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
		if d < MinHeartbeatInterval || d > MaxHeartbeatInterval {
			return nil, fmt.Errorf("invalid heartbeat interval %q: must be between %s and %s",
				raw, MinHeartbeatInterval, MaxHeartbeatInterval)
		}
		cfg.HeartbeatInterval = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DATA_DIR")); raw != "" {
		cfg.DataDir = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_HEALTH_ADDR")); raw != "" {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			return nil, fmt.Errorf("invalid health addr %q: %w", raw, err)
		}
		cfg.HealthAddr = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_ALLOW_INSECURE_HTTP")); raw == "true" {
		cfg.AllowInsecureHTTP = true
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_SAAS_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid SaaS timeout %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("invalid SaaS timeout %q: must be positive", raw)
		}
		cfg.SaaSTimeout = d
	}

	// Fail-fast: reject an insecure http:// SaaS URL here, before any
	// request is ever attempted, unless explicitly allowed for development.
	if cfg.SaaSURL != "" && strings.HasPrefix(strings.ToLower(cfg.SaaSURL), "http://") && !cfg.AllowInsecureHTTP {
		return nil, fmt.Errorf("insecure GEOCAM_SAAS_URL %q: http:// is disabled by default; "+
			"set GEOCAM_ALLOW_INSECURE_HTTP=true to allow it in development", cfg.SaaSURL)
	}

	return cfg, nil
}
