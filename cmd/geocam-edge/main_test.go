package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestVersionReportNoSecrets(t *testing.T) {
	out := versionReport(platform.Info{OS: "linux", GOARCH: "arm64"})
	for _, want := range []string{"geocam-edge", "0.1.0", "linux", "arm64"} {
		if !strings.Contains(out, want) {
			t.Errorf("versionReport() missing %q:\n%s", want, out)
		}
	}
}

func TestConfigReportNeverLeaksSecrets(t *testing.T) {
	cfg := &config.Config{
		SaaSURL:           "https://saas.example.com",
		ProcessingMode:    config.ModeCloud,
		DataDir:           "/var/lib/geocam-edge",
		HealthAddr:        "127.0.0.1:8091",
		HeartbeatInterval: 30 * time.Second,
		DiscoveryEnabled:  true,
		DiscoveryInterval: 5 * time.Minute,
		DiscoveryTimeout:  4 * time.Second,
		StreamRole:        "sub",
		StreamTimeout:     5 * time.Second,
	}
	host := platform.Info{OS: "linux", GOARCH: "arm64"}
	creds := credentials.Credentials{
		EdgeID: "edge-1", DeviceID: "dev-1", Credential: "edg_live_supersecret",
		Status: credentials.StatusEnrolled,
	}

	out := configReport(cfg, host, creds, true)

	for _, want := range []string{
		"saas_url:", "https://saas.example.com",
		"enrollment_token:     configured",
		"enrolled:             yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("configReport() missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "edg_live_supersecret") {
		t.Errorf("configReport() leaks the stored credential:\n%s", out)
	}
}

func TestConfigReportNotEnrolledAndTokenNotConfigured(t *testing.T) {
	out := configReport(&config.Config{}, platform.Info{}, credentials.Credentials{}, false)
	if !strings.Contains(out, "enrolled:             no") {
		t.Errorf("configReport() missing enrolled:no:\n%s", out)
	}
	if !strings.Contains(out, "enrollment_token:     not configured") {
		t.Errorf("configReport() missing enrollment_token:not configured:\n%s", out)
	}
}

func TestSaasCheckReportSuccess(t *testing.T) {
	me := transport.MeResponse{
		DeviceID:       "dev-1",
		OrganizationID: json.Number("42"),
		SiteID:         json.Number("7"),
	}
	out, ok := saasCheckReport("https://saas.example.com", "edge-1", me, nil)
	if !ok {
		t.Fatal("saasCheckReport() ok = false, want true")
	}
	for _, want := range []string{"OK", "dev-1", "42", "7", "edge-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("saasCheckReport() missing %q:\n%s", want, out)
		}
	}
}

func TestSaasCheckReportFailure(t *testing.T) {
	out, ok := saasCheckReport("https://saas.example.com", "edge-1", transport.MeResponse{}, transport.ErrUnauthorized)
	if ok {
		t.Fatal("saasCheckReport() ok = true, want false")
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "rejected") {
		t.Errorf("saasCheckReport() unexpected output:\n%s", out)
	}
}

// testBinaryOnce compiles the geocam-edge binary exactly once for the whole
// test run — dispatch-level tests below share it instead of each paying a
// full `go build` (which otherwise makes this suite an order of magnitude
// slower for no benefit, since the binary under test never changes). It is
// built into a fixed temp dir (not t.TempDir(), which would be removed after
// the first test that touches it finishes) and cleaned up in TestMain.
var (
	testBinaryOnce sync.Once
	testBinaryPath string
	testBinaryErr  error
	testBinaryDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testBinaryDir != "" {
		_ = os.RemoveAll(testBinaryDir)
	}
	os.Exit(code)
}

// buildTestBinary returns the shared compiled geocam-edge binary path, so
// dispatch-level behaviors (--help, unknown command, version) can be
// verified against the real process.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	testBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "geocam-edge-bin-*")
		if err != nil {
			testBinaryErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		testBinaryDir = dir
		bin := filepath.Join(dir, "geocam-edge")
		out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
		if err != nil {
			testBinaryErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		testBinaryPath = bin
	})
	if testBinaryErr != nil {
		t.Fatal(testBinaryErr)
	}
	return testBinaryPath
}

func TestBinaryRootHelpListsRealCommands(t *testing.T) {
	bin := buildTestBinary(t)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		for _, want := range []string{"run", "version", "identity", "config", "check", "enroll", "credential rotate", "discovery scan", "saas check"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("%v output missing command %q:\n%s", args, want, out)
			}
		}
	}
}

func TestBinaryVersion(t *testing.T) {
	bin := buildTestBinary(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "geocam-edge") {
		t.Errorf("version output missing binary name:\n%s", out)
	}

	out2, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v\n%s", err, out2)
	}
	if string(out) != string(out2) {
		t.Errorf("'version' and '--version' diverged:\n%s\nvs\n%s", out, out2)
	}
}

func TestBinaryUnknownCommandFailsWithoutStartingDaemon(t *testing.T) {
	bin := buildTestBinary(t)
	cmd := exec.Command(bin, "pepito")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown command succeeded, want non-zero exit:\n%s", out)
	}
	if !strings.Contains(string(out), `unknown command "pepito"`) {
		t.Errorf("unexpected error output:\n%s", out)
	}
	if strings.Contains(string(out), "agent") {
		t.Errorf("output suggests the daemon started for an unknown command:\n%s", out)
	}
}

func TestBinaryRunDispatchesSamePathAsNoArgs(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()

	for _, args := range [][]string{{"run", "--help"}} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if !strings.Contains(string(out), "Start the edge agent daemon") {
			t.Errorf("%v output missing daemon description:\n%s", args, out)
		}
	}

	// run and no-args must share the exact same startup failure path: an
	// invalid config makes both fail identically (rather than one silently
	// launching a daemon).
	env := append(os.Environ(), "GEOCAM_HEALTH_ADDR=not-a-valid-addr", "GEOCAM_DATA_DIR="+dataDir)
	runOut, runErr := runWithEnv(bin, env, "run")
	noArgsOut, noArgsErr := runWithEnv(bin, env)
	if (runErr == nil) != (noArgsErr == nil) {
		t.Fatalf("run vs no-args exit status diverged: run err=%v, no-args err=%v", runErr, noArgsErr)
	}
	if !strings.Contains(runOut, "configuration error") || !strings.Contains(noArgsOut, "configuration error") {
		t.Errorf("expected both to fail with a configuration error:\nrun=%s\nno-args=%s", runOut, noArgsOut)
	}
}

func runWithEnv(bin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestIdentityConfigOutputsNeverLeakSecrets(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()

	if err := credentials.Save(dataDir, credentials.Credentials{
		EdgeID: "edge-1", DeviceID: "dev-1", Credential: "edg_live_realsecretvalue",
		CredentialVersion: 1, EnrolledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	env := append(os.Environ(), "GEOCAM_DATA_DIR="+dataDir, "GEOCAM_ENROLLMENT_TOKEN=super-secret-token-value")

	for _, args := range [][]string{{"identity"}, {"config"}} {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if strings.Contains(string(out), "edg_live_realsecretvalue") {
			t.Errorf("%v output leaks the stored credential:\n%s", args, out)
		}
		if strings.Contains(string(out), "super-secret-token-value") {
			t.Errorf("%v output leaks the enrollment token:\n%s", args, out)
		}
	}
}

func TestBinaryEnrollCredentialDiscoveryHelp(t *testing.T) {
	bin := buildTestBinary(t)
	for _, args := range [][]string{
		{"enroll", "--help"},
		{"credential", "--help"},
		{"credential", "rotate", "--help"},
		{"discovery", "--help"},
		{"discovery", "scan", "--help"},
		{"saas", "--help"},
		{"saas", "check", "--help"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			t.Errorf("%v produced no output", args)
		}
	}
}

func TestBinarySaasCheckWithoutSaaSURLFails(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()
	env := append(os.Environ(), "GEOCAM_DATA_DIR="+dataDir, "GEOCAM_SAAS_URL=")

	cmd := exec.Command(bin, "saas", "check")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("saas check without SAAS_URL succeeded, want failure:\n%s", out)
	}
	if !strings.Contains(string(out), "GEOCAM_SAAS_URL is not configured") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestBinarySaasCheckNotEnrolledFails(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"GEOCAM_DATA_DIR="+dataDir,
		"GEOCAM_SAAS_URL=https://saas.example.com",
	)

	cmd := exec.Command(bin, "saas", "check")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("saas check without enrollment succeeded, want failure:\n%s", out)
	}
	if !strings.Contains(string(out), "NOT ENROLLED") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestBinarySaasCheckAgainstMockSaaS(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()

	if err := credentials.Save(dataDir, credentials.Credentials{
		EdgeID: "edge-1", DeviceID: "dev-1", Credential: "edg_live_test",
		CredentialVersion: 1, EnrolledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transport.MePath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer edg_live_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.MeResponse{
			DeviceID: "dev-1", OrganizationID: json.Number("1"), SiteID: json.Number("2"),
		})
	}))
	defer srv.Close()

	env := append(os.Environ(),
		"GEOCAM_DATA_DIR="+dataDir,
		"GEOCAM_SAAS_URL="+srv.URL,
		"GEOCAM_ALLOW_INSECURE_HTTP=true",
	)
	cmd := exec.Command(bin, "saas", "check")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("saas check against mock SaaS failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SaaS connectivity: OK") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestBinarySaasCheckAgainstMockSaaSUnauthorized(t *testing.T) {
	bin := buildTestBinary(t)
	dataDir := t.TempDir()

	if err := credentials.Save(dataDir, credentials.Credentials{
		EdgeID: "edge-1", DeviceID: "dev-1", Credential: "edg_live_test",
		CredentialVersion: 1, EnrolledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	env := append(os.Environ(),
		"GEOCAM_DATA_DIR="+dataDir,
		"GEOCAM_SAAS_URL="+srv.URL,
		"GEOCAM_ALLOW_INSECURE_HTTP=true",
	)
	cmd := exec.Command(bin, "saas", "check")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("saas check against unauthorized mock SaaS succeeded, want failure:\n%s", out)
	}
	if !strings.Contains(string(out), "rejected") {
		t.Errorf("unexpected output:\n%s", out)
	}
}
