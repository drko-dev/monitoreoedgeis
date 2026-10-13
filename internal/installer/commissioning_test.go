package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
)

func TestCommissioningStatus_EmptyDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{
		DataDir:    tmpDir,
		HealthAddr: "127.0.0.1:9999",
		httpClient: &http.Client{},
	}

	status, err := svc.GetCommissioningStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.DaemonInstalled {
		t.Errorf("expected DaemonInstalled=false")
	}
	if status.OverallReady {
		t.Errorf("expected OverallReady=false")
	}
	if status.SafeMessage == "" {
		t.Errorf("expected non-empty SafeMessage")
	}
}

func TestEnsureDaemonBinary_CopiesExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{
		DataDir: tmpDir,
	}

	// Create fake companion executable in tmpDir/source
	sourceDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	binName := "geocam-edge"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	fakeSrc := filepath.Join(sourceDir, binName)
	if err := os.WriteFile(fakeSrc, []byte("#!/bin/sh\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	// Place candidate in svc.DataDir/bin so EnsureDaemonBinary finds it
	target := svc.StagedDaemonPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutableFile(fakeSrc, target); err != nil {
		t.Fatal(err)
	}

	res, err := svc.EnsureDaemonBinary()
	if err != nil {
		t.Fatalf("EnsureDaemonBinary failed: %v", err)
	}
	if res != target {
		t.Errorf("expected %s, got %s", target, res)
	}
}

func TestCommissionDaemon_RequiresEnrollment(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{
		DataDir: tmpDir,
	}

	res, err := svc.CommissionDaemon(context.Background())
	if err != nil {
		t.Fatalf("CommissionDaemon returned error: %v", err)
	}
	if res.Outcome != CommissionOutcomeActionRequired {
		t.Errorf("expected %s, got %s", CommissionOutcomeActionRequired, res.Outcome)
	}
}

func TestCommissionDaemon_RequiresCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{
		DataDir: tmpDir,
	}

	// Initialize identity
	if _, err := identity.Load(tmpDir, ""); err != nil {
		t.Fatal(err)
	}

	// But credentials missing
	res, err := svc.CommissionDaemon(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != CommissionOutcomeActionRequired {
		t.Errorf("expected %s, got %s", CommissionOutcomeActionRequired, res.Outcome)
	}
}

func TestCommissionDaemon_RequiresConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{
		DataDir: tmpDir,
	}

	ident, err := identity.Load(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}

	creds := credentials.Credentials{
		EdgeID:            ident.EdgeID,
		DeviceID:          "device-test-123",
		Credential:        "secret-cred",
		CredentialVersion: 1,
		TenantID:          "tenant-test",
		SiteID:            "site-test",
		EnrolledAt:        time.Now(),
		Status:            credentials.StatusEnrolled,
	}
	if err := credentials.Save(tmpDir, creds); err != nil {
		t.Fatal(err)
	}

	// Ensure fake daemon binary is staged
	binPath := svc.StagedDaemonPath()
	_ = os.MkdirAll(filepath.Dir(binPath), 0755)
	_ = os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0"), 0755)

	svc.ConfigFilePath = filepath.Join(tmpDir, "nonexistent.env")
	res, err := svc.CommissionDaemon(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != CommissionOutcomeActionRequired {
		t.Errorf("expected %s, got %s", CommissionOutcomeActionRequired, res.Outcome)
	}
}

func TestProbeHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	svc := &Service{
		HealthAddr: ts.Listener.Addr().String(),
		httpClient: ts.Client(),
	}

	code := svc.probeHTTP(context.Background(), "/healthz")
	if code != http.StatusOK {
		t.Errorf("expected 200, got %d", code)
	}

	code404 := svc.probeHTTP(context.Background(), "/nonexistent")
	if code404 != http.StatusNotFound {
		t.Errorf("expected 404, got %d", code404)
	}
}
