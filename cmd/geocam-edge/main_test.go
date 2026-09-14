package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

func TestIdentityReportNoSecrets(t *testing.T) {
	ident := identity.Identity{EdgeID: "11111111-1111-4111-8111-111111111111", Source: identity.SourcePersisted}
	cfg := &config.Config{ProcessingMode: config.ModeCloud, DataDir: "/var/lib/geocam-edge"}
	host := platform.Info{GOARCH: "arm64"}

	report := identityReport(ident, cfg, host)

	for _, want := range []string{ident.EdgeID, string(ident.Source), "arm64", string(cfg.ProcessingMode), cfg.DataDir} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestCheckReportReady(t *testing.T) {
	snap := health.Snapshot{Status: health.StateReady, EdgeID: "edge-1", Version: "0.1.0"}

	report, ready := checkReport(snap)

	if !ready {
		t.Error("ready = false, want true for StateReady")
	}
	if !strings.Contains(report, "READY") {
		t.Errorf("report missing status:\n%s", report)
	}
}

func TestCheckReportNotReady(t *testing.T) {
	for _, s := range []health.State{health.StateStarting, health.StateDegraded, health.StateStopping} {
		_, ready := checkReport(health.Snapshot{Status: s})
		if ready {
			t.Errorf("ready = true for state %q, want false", s)
		}
	}
}

func TestEnrollSummaryNoSecret(t *testing.T) {
	const secret = "edg_live_should_never_appear"

	summary := enrollSummary("edge-42", "device-1", "org-1", "site-1")

	for _, want := range []string{"edge-42", "device-1", "org-1", "site-1"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, secret) {
		t.Errorf("summary leaks credential secret:\n%s", summary)
	}
}

func TestRotateSummaryNoSecret(t *testing.T) {
	summary := rotateSummary("edge-42", 3)

	if !strings.Contains(summary, "edge-42") || !strings.Contains(summary, "3") {
		t.Errorf("summary missing expected fields:\n%s", summary)
	}
	if strings.Contains(summary, "edg_live") {
		t.Errorf("summary leaks a credential-shaped value:\n%s", summary)
	}
}

func TestSaasErrorMessageMapping(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{transport.ErrTokenInvalid, "invalid"},
		{transport.ErrAlreadyEnrolled, "already enrolled"},
		{transport.ErrInvalidRequest, "invalid"},
		{transport.ErrUnauthorized, "rejected"},
		{transport.ErrTimeout, "timed out"},
		{transport.ErrSaaSUnavailable, "unreachable"},
	}
	for _, tt := range tests {
		msg := saasErrorMessage(tt.err)
		if !strings.Contains(msg, tt.want) {
			t.Errorf("saasErrorMessage(%v) = %q, want to contain %q", tt.err, msg, tt.want)
		}
	}
}

func TestResolveEnrollmentTokenPrefersFlagOverEmpty(t *testing.T) {
	// No stdin pipe, no env var set in this test process: the --token flag
	// must be used as the last resort.
	t.Setenv("GEOCAM_ENROLLMENT_TOKEN", "")

	tok, err := resolveEnrollmentToken("dev-token")
	if err != nil {
		t.Fatalf("resolveEnrollmentToken() error = %v", err)
	}
	if tok != "dev-token" {
		t.Errorf("token = %q, want %q", tok, "dev-token")
	}
}

func TestResolveEnrollmentTokenPrefersEnvOverFlag(t *testing.T) {
	t.Setenv("GEOCAM_ENROLLMENT_TOKEN", "env-token")

	tok, err := resolveEnrollmentToken("dev-token")
	if err != nil {
		t.Fatalf("resolveEnrollmentToken() error = %v", err)
	}
	if tok != "env-token" {
		t.Errorf("token = %q, want %q", tok, "env-token")
	}
}

func TestResolveEnrollmentTokenRequiresSomething(t *testing.T) {
	t.Setenv("GEOCAM_ENROLLMENT_TOKEN", "")

	if _, err := resolveEnrollmentToken(""); err == nil {
		t.Fatal("resolveEnrollmentToken() succeeded with no token source, want error")
	}
}

func TestCredentialsExistsGuardsDoubleEnrollment(t *testing.T) {
	dir := t.TempDir()
	enrolledAt, err := time.Parse(time.RFC3339, "2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	if err := credentials.Save(dir, credentials.Credentials{
		EdgeID: "edge-1", Credential: "edg_live_x", EnrolledAt: enrolledAt,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	creds, err := credentials.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !creds.IsEnrolled() {
		t.Fatal("IsEnrolled() = false, want true (double-enrollment guard would not trigger)")
	}
}

func TestDiscoveryScanReportEmpty(t *testing.T) {
	out := discoveryScanReport(nil)
	if !strings.Contains(out, "No network video devices discovered") {
		t.Errorf("unexpected output for nil result: %s", out)
	}

	emptyRes := &discovery.ScanResult{Duration: 500 * time.Millisecond}
	out = discoveryScanReport(emptyRes)
	if !strings.Contains(out, "No network video devices discovered") || !strings.Contains(out, "500ms") {
		t.Errorf("unexpected output for empty result: %s", out)
	}
}

func TestDiscoveryScanReportWithDevices(t *testing.T) {
	res := &discovery.ScanResult{
		Duration: 1200 * time.Millisecond,
		DevicesFound: []discovery.DiscoveredDevice{
			{
				IP:           "192.168.1.50",
				Port:         80,
				Path:         "/onvif/device_service",
				EPRAddress:   "urn:uuid:aabbccdd-1122-3344-5566-778899aabbcc",
				DeviceType:   discovery.DeviceTypeCamera,
				Manufacturer: "AcmeCorp",
				Model:        "CamX",
				Serial:       "SN0001",
				Firmware:     "1.0.4",
				AuthRequired: false,
			},
		},
	}

	out := discoveryScanReport(res)
	for _, want := range []string{
		"192.168.1.50:80/onvif/device_service",
		"urn:uuid:aabbccdd-1122-3344-5566-778899aabbcc",
		"camera",
		"AcmeCorp",
		"CamX",
		"SN0001",
		"1.0.4",
		"Auth Required: false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}
