package remoteconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// Supported production models for Edge YOLO inference.
const (
	SupportedPersonModel  = "yolo11s-pose.pt"
	SupportedVehicleModel = "yolo11n.pt"
)

// Technical limits sourced from existing config/processing definitions.
const (
	MinVideoTargetFPS    = config.MinVideoTargetFPS
	MaxVideoTargetFPS    = config.MaxVideoTargetFPS
	MaxVideoOutputWidth  = config.MaxVideoOutputWidth
	MaxVideoOutputHeight = config.MaxVideoOutputHeight
)

// DisallowedKeys are keys that must never be accepted in remote configuration.
var DisallowedKeys = []string{
	"tenant_id",
	"organization_id",
	"site_id",
	"rtsp_url",
	"rtsp_path",
	"rtsp_password",
	"password",
	"username",
	"url",
	"path",
	"data_dir",
	"models_dir",
	"worker_cmd",
	"ffmpeg_path",
}

// RuntimeConfig represents the remote configuration payload applied to Edge runtime.
type RuntimeConfig struct {
	// ProcessingMode selects inference/video location: "cloud", "hybrid", "edge".
	ProcessingMode *config.ProcessingMode `json:"processing_mode,omitempty"`

	// TargetFPS is the global video output rate ceiling.
	TargetFPS *float64 `json:"target_fps,omitempty"`

	// OutputWidth is the processed frame width for resizing (even, >0, <=1920).
	OutputWidth *int `json:"output_width,omitempty"`

	// OutputHeight is the processed frame height for resizing (even, >0, <=1080).
	OutputHeight *int `json:"output_height,omitempty"`

	// HybridROIs defines the global normalized regions of interest for hybrid motion detection.
	HybridROIs []config.HybridROI `json:"hybrid_rois,omitempty"`

	// PersonModel selects the person detection model (must be "yolo11s-pose.pt").
	PersonModel *string `json:"person_model,omitempty"`

	// VehicleModel selects the vehicle detection model (must be "yolo11n.pt").
	VehicleModel *string `json:"vehicle_model,omitempty"`

	// Cameras holds per-camera configuration overrides keyed by candidate_key.
	Cameras map[string]CameraConfig `json:"cameras,omitempty"`
}

// CameraConfig holds camera-specific overrides for a candidate_key.
type CameraConfig struct {
	TargetFPS    *float64           `json:"target_fps,omitempty"`
	OutputWidth  *int               `json:"output_width,omitempty"`
	OutputHeight *int               `json:"output_height,omitempty"`
	HybridROIs   []config.HybridROI `json:"hybrid_rois,omitempty"`

	// ANPR carries the minimal desired effective state for Hito J6
	// (plate_recognition) SaaS already resolved (catalog/entitlement/
	// override/kill-switch) -- this field is only ever a fail-closed cache
	// of that already-resolved decision, never a second place J2/J3 logic
	// gets re-derived (item 37: "el SaaS ya resolvió effective capability;
	// Edge sólo consume desired effective state").
	ANPR *CameraANPRConfig `json:"anpr,omitempty"`
}

// CameraANPRConfig is the per-camera ANPR/LPR desired state (item 37).
// Absent (nil) or Enabled=false means DENY -- the same fail-closed default
// as anpr.DenyAllAuthorizer (item 38: "cuando no exista config válida: deny
// ANPR").
type CameraANPRConfig struct {
	Enabled bool `json:"enabled"`
	// HighSpeedLPR opts a camera into the bounded high-sampling burst
	// profile (item 36/79). Never auto-activated by this config alone --
	// the sampler still applies its own caps/TTL/revert-to-baseline.
	HighSpeedLPR bool `json:"high_speed_lpr,omitempty"`
}

// Validate checks that cfg satisfies all technical and security bounds.
func Validate(cfg RuntimeConfig, knownCameras []string) error {
	// 1. ProcessingMode validation
	if cfg.ProcessingMode != nil {
		if _, err := config.ParseProcessingMode(string(*cfg.ProcessingMode)); err != nil {
			return fmt.Errorf("remoteconfig: invalid processing_mode: %w", err)
		}
	}

	// 2. Global TargetFPS validation
	if cfg.TargetFPS != nil {
		if *cfg.TargetFPS < MinVideoTargetFPS || *cfg.TargetFPS > MaxVideoTargetFPS {
			return fmt.Errorf("remoteconfig: target_fps %.2f out of technical range [%.1f, %.1f]",
				*cfg.TargetFPS, MinVideoTargetFPS, MaxVideoTargetFPS)
		}
	}

	// 3. Global Resolution validation
	if cfg.OutputWidth != nil || cfg.OutputHeight != nil {
		if cfg.OutputWidth == nil || cfg.OutputHeight == nil {
			return fmt.Errorf("remoteconfig: output_width and output_height must both be specified")
		}
		if err := validateResolution(*cfg.OutputWidth, *cfg.OutputHeight); err != nil {
			return fmt.Errorf("remoteconfig: invalid global resolution: %w", err)
		}
	}

	// 4. Global HybridROIs validation
	for i, roi := range cfg.HybridROIs {
		if err := validateROI(roi); err != nil {
			return fmt.Errorf("remoteconfig: invalid global hybrid_rois[%d]: %w", i, err)
		}
	}

	// 5. Model validation
	if cfg.PersonModel != nil {
		m := *cfg.PersonModel
		if containsPathOrURL(m) {
			return fmt.Errorf("remoteconfig: arbitrary model path or url %q is not allowed", m)
		}
		if m != SupportedPersonModel {
			return fmt.Errorf("remoteconfig: unsupported person model %q: only %q is supported",
				m, SupportedPersonModel)
		}
	}
	if cfg.VehicleModel != nil {
		m := *cfg.VehicleModel
		if containsPathOrURL(m) {
			return fmt.Errorf("remoteconfig: arbitrary model path or url %q is not allowed", m)
		}
		if m != SupportedVehicleModel {
			return fmt.Errorf("remoteconfig: unsupported vehicle model %q: only %q is supported",
				m, SupportedVehicleModel)
		}
	}

	// 6. Cameras validation
	knownMap := make(map[string]bool, len(knownCameras))
	for _, k := range knownCameras {
		knownMap[k] = true
	}

	for candidateKey, camCfg := range cfg.Cameras {
		if strings.TrimSpace(candidateKey) == "" {
			return fmt.Errorf("remoteconfig: camera candidate_key cannot be empty")
		}
		if containsPathOrURL(candidateKey) {
			return fmt.Errorf("remoteconfig: invalid camera candidate_key %q", candidateKey)
		}
		if !knownMap[candidateKey] {
			return fmt.Errorf("remoteconfig: camera %q is not known by runtime", candidateKey)
		}

		if camCfg.TargetFPS != nil {
			if *camCfg.TargetFPS < MinVideoTargetFPS || *camCfg.TargetFPS > MaxVideoTargetFPS {
				return fmt.Errorf("remoteconfig: camera %q target_fps %.2f out of technical range [%.1f, %.1f]",
					candidateKey, *camCfg.TargetFPS, MinVideoTargetFPS, MaxVideoTargetFPS)
			}
		}

		if camCfg.OutputWidth != nil || camCfg.OutputHeight != nil {
			if camCfg.OutputWidth == nil || camCfg.OutputHeight == nil {
				return fmt.Errorf("remoteconfig: camera %q output_width and output_height must both be specified", candidateKey)
			}
			if err := validateResolution(*camCfg.OutputWidth, *camCfg.OutputHeight); err != nil {
				return fmt.Errorf("remoteconfig: camera %q invalid resolution: %w", candidateKey, err)
			}
		}

		for i, roi := range camCfg.HybridROIs {
			if err := validateROI(roi); err != nil {
				return fmt.Errorf("remoteconfig: camera %q invalid hybrid_rois[%d]: %w", candidateKey, i, err)
			}
		}
	}

	return nil
}

// ParseAndValidateJSON unmarshals raw JSON bytes, strictly checks for disallowed fields,
// and validates the resulting RuntimeConfig against known cameras.
func ParseAndValidateJSON(data []byte, knownCameras []string) (RuntimeConfig, error) {
	// First pass: verify no disallowed field is present anywhere
	var rawMap map[string]any
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return RuntimeConfig{}, fmt.Errorf("remoteconfig: json unmarshal error: %w", err)
	}
	if err := checkDisallowedKeys(rawMap); err != nil {
		return RuntimeConfig{}, err
	}

	// Second pass: strictly decode with DisallowUnknownFields
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg RuntimeConfig
	if err := dec.Decode(&cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("remoteconfig: unknown or invalid field in configuration: %w", err)
	}

	if err := Validate(cfg, knownCameras); err != nil {
		return RuntimeConfig{}, err
	}

	return cfg, nil
}

func checkDisallowedKeys(obj any) error {
	switch v := obj.(type) {
	case map[string]any:
		for k, val := range v {
			lower := strings.ToLower(strings.TrimSpace(k))
			for _, disallowed := range DisallowedKeys {
				if lower == disallowed {
					return fmt.Errorf("remoteconfig: disallowed field %q is not accepted in remote configuration", k)
				}
			}
			if err := checkDisallowedKeys(val); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := checkDisallowedKeys(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResolution(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("dimensions %dx%d must be positive", width, height)
	}
	if width > MaxVideoOutputWidth || height > MaxVideoOutputHeight {
		return fmt.Errorf("dimensions %dx%d exceed maximum allowed %dx%d",
			width, height, MaxVideoOutputWidth, MaxVideoOutputHeight)
	}
	return processing.ValidateOutputDimensions(width, height)
}

func validateROI(roi config.HybridROI) error {
	if roi.XMin < 0 || roi.YMin < 0 || roi.XMax > 1 || roi.YMax > 1 || roi.XMin >= roi.XMax || roi.YMin >= roi.YMax {
		return fmt.Errorf("coordinates [%g,%g,%g,%g] must be within 0..1 with min < max",
			roi.XMin, roi.YMin, roi.XMax, roi.YMax)
	}
	return nil
}

func containsPathOrURL(s string) bool {
	lower := strings.ToLower(s)
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return true
	}
	if strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") ||
		strings.HasPrefix(lower, "rtsp:") || strings.HasPrefix(lower, "file:") {
		return true
	}
	return false
}
