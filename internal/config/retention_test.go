package config

import (
	"testing"
	"time"
)

// TestRetentionDefaultsAreDisabled pins the rule that retention has no
// production default: an appliance that configures nothing must retain
// everything, exactly as it did before retention existed.
func TestRetentionDefaultsAreDisabled(t *testing.T) {
	for _, k := range []string{
		"GEOCAM_EDGE_RETENTION_MAX_EVENTS", "GEOCAM_EDGE_RETENTION_MAX_EVENT_BYTES",
		"GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE", "GEOCAM_EDGE_RETENTION_MAX_CAPTURES",
		"GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES", "GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE",
	} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RetentionMaxEvents != 0 || cfg.RetentionMaxEventBytes != 0 || cfg.RetentionMaxEventAge != 0 {
		t.Errorf("event retention defaults are not disabled: %+v", cfg)
	}
	if cfg.RetentionMaxCaptures != 0 || cfg.RetentionMaxCaptureBytes != 0 || cfg.RetentionMaxCaptureAge != 0 {
		t.Errorf("capture retention defaults are not disabled: %+v", cfg)
	}
}

func TestRetentionBoundsAreParsed(t *testing.T) {
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_EVENTS", "500")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_EVENT_BYTES", "1048576")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE", "72h")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_CAPTURES", "1000")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES", "2097152")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RetentionMaxEvents != 500 || cfg.RetentionMaxEventBytes != 1048576 {
		t.Errorf("event count/bytes = %d/%d", cfg.RetentionMaxEvents, cfg.RetentionMaxEventBytes)
	}
	if cfg.RetentionMaxEventAge != 72*time.Hour {
		t.Errorf("event age = %v, want 72h", cfg.RetentionMaxEventAge)
	}
	if cfg.RetentionMaxCaptures != 1000 || cfg.RetentionMaxCaptureBytes != 2097152 {
		t.Errorf("capture count/bytes = %d/%d", cfg.RetentionMaxCaptures, cfg.RetentionMaxCaptureBytes)
	}
	if cfg.RetentionMaxCaptureAge != 30*time.Minute {
		t.Errorf("capture age = %v, want 30m", cfg.RetentionMaxCaptureAge)
	}
}

// TestRetentionZeroIsExplicitlyAllowed is the difference from
// GEOCAM_EDGE_MAX_CLIP_SIZE_BYTES, whose parse rejects 0. Retention must be
// able to express "disabled" from the environment.
func TestRetentionZeroIsExplicitlyAllowed(t *testing.T) {
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_EVENTS", "0")
	t.Setenv("GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE", "0s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() must accept an explicit 0: %v", err)
	}
	if cfg.RetentionMaxEvents != 0 || cfg.RetentionMaxEventAge != 0 {
		t.Errorf("explicit zero not preserved: %+v", cfg)
	}
}

func TestRetentionInvalidValuesAreHardErrors(t *testing.T) {
	cases := []struct{ name, key, val string }{
		{"negative count", "GEOCAM_EDGE_RETENTION_MAX_EVENTS", "-1"},
		{"negative bytes", "GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES", "-5"},
		{"non-numeric", "GEOCAM_EDGE_RETENTION_MAX_CAPTURES", "many"},
		{"negative age", "GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE", "-1h"},
		{"unparsable age", "GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE", "soon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("expected a hard error for %s=%q", tc.key, tc.val)
			}
		})
	}
}
