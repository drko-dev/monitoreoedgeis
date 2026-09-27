package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/instance"
)

func TestGetSystemReportDoesNotExposeSecrets(t *testing.T) {
	tempDir := t.TempDir()
	svc := NewService(tempDir, "")

	report, err := svc.GetSystemReport(context.Background())
	if err != nil {
		t.Fatalf("GetSystemReport failed: %v", err)
	}

	// Verify no secrets or sensitive keywords are in the report
	if report.OS == "" {
		t.Error("Expected OS to be populated")
	}
	if report.Arch == "" {
		t.Error("Expected Arch to be populated")
	}
	if report.Hostname == "" {
		t.Error("Expected Hostname to be populated")
	}

	// Verify instance lock is strictly false
	if report.OwnsInstanceLock {
		t.Error("Installer service must NEVER claim ownership of instance lock")
	}

	// Check string representations do not contain secrets
	reportStr := report.OS + report.Arch + report.Hostname + report.EdgeVersion
	sensitiveKeywords := []string{"password", "token", "jwt", "secret", "private_key", "bearer"}
	for _, kw := range sensitiveKeywords {
		if strings.Contains(strings.ToLower(reportStr), kw) {
			t.Errorf("System report exposes sensitive keyword: %q", kw)
		}
	}
}

func TestInstanceLockRemainsAvailable(t *testing.T) {
	tempDir := t.TempDir()
	svc := NewService(tempDir, "")

	// 1. Run installer operations
	_, err := svc.GetSystemReport(context.Background())
	if err != nil {
		t.Fatalf("GetSystemReport failed: %v", err)
	}
	_, err = svc.GetInstallerState(context.Background())
	if err != nil {
		t.Fatalf("GetInstallerState failed: %v", err)
	}

	// 2. Demonstrate that a daemon CAN still acquire the instance lock on this directory!
	lock, err := instance.Acquire(tempDir)
	if err != nil {
		t.Fatalf("Expected daemon to be able to acquire lock after installer calls, got err: %v", err)
	}
	defer lock.Release()

	// Verify installer report still reports OwnsInstanceLock = false
	report, err := svc.GetSystemReport(context.Background())
	if err != nil {
		t.Fatalf("GetSystemReport failed: %v", err)
	}
	if report.OwnsInstanceLock {
		t.Error("Installer must not report owning instance lock even when daemon is running")
	}
}

func TestDeriveInstallerState(t *testing.T) {
	baseReport := &SystemReport{
		PlatformSupported: true,
		ConfigPresent:     false,
	}

	// 1. Unsupported platform
	unsupportedReport := &SystemReport{PlatformSupported: false}
	state := DeriveInstallerState(unsupportedReport, identity.Identity{}, credentials.Credentials{}, nil)
	if state.State != StateBlocked || state.ReasonCode != ReasonUnsupportedPlatform {
		t.Errorf("Expected BLOCKED/UNSUPPORTED_PLATFORM, got %+v", state)
	}
	if state.Recoverable {
		t.Error("Unsupported platform state should not be recoverable")
	}

	// 2. Corrupt credentials
	state = DeriveInstallerState(baseReport, identity.Identity{}, credentials.Credentials{}, credentials.ErrCorrupt)
	if state.State != StateActionRequired || state.ReasonCode != ReasonCredentialsCorrupt {
		t.Errorf("Expected ACTION_REQUIRED/CREDENTIALS_CORRUPT, got %+v", state)
	}
	if !state.Recoverable {
		t.Error("Corrupt credentials state should be recoverable")
	}

	// 3. Pristine Machine (New)
	state = DeriveInstallerState(baseReport, identity.Identity{}, credentials.Credentials{}, nil)
	if state.State != StateNew || state.ReasonCode != ReasonFreshInstall {
		t.Errorf("Expected NEW/FRESH_INSTALL, got %+v", state)
	}

	// 4. Fully Enrolled Device
	enrolledID := identity.Identity{
		EdgeID: "edge-test-1234",
		Status: identity.StatusEnrolled,
	}
	enrolledCreds := credentials.Credentials{
		EdgeID:     "edge-test-1234",
		Status:     credentials.StatusEnrolled,
		EnrolledAt: time.Now().UTC(),
	}
	state = DeriveInstallerState(baseReport, enrolledID, enrolledCreds, nil)
	if state.State != StateEnrolled || state.ReasonCode != ReasonDeviceEnrolled {
		t.Errorf("Expected ENROLLED/DEVICE_ENROLLED, got %+v", state)
	}
	if state.DeviceID != "edge-test-1234" {
		t.Errorf("Expected DeviceID 'edge-test-1234', got %q", state.DeviceID)
	}

	// 5. Config present but unenrolled
	configuredReport := &SystemReport{
		PlatformSupported: true,
		ConfigPresent:     true,
	}
	state = DeriveInstallerState(configuredReport, identity.Identity{}, credentials.Credentials{}, nil)
	if state.State != StateNeedsEnrollment || state.ReasonCode != ReasonDeviceNotEnrolled {
		t.Errorf("Expected NEEDS_ENROLLMENT/DEVICE_NOT_ENROLLED, got %+v", state)
	}
}

func TestConfigPresenceDetection(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "custom.env")

	svc := NewService(tempDir, configPath)
	if svc.checkConfigPresent() {
		t.Error("Expected config to be reported absent before creation")
	}

	if err := os.WriteFile(configPath, []byte("GEOCAM_DATA_DIR="+tempDir+"\n"), 0o600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	if !svc.checkConfigPresent() {
		t.Error("Expected config to be reported present after file write")
	}
}

func TestSanitizeError(t *testing.T) {
	// Raw stream error with credentials
	rawStreamErr := errors.New("failed to dial rtsp://admin:secret123@192.168.1.50:554/live")
	safeErr := SanitizeError(rawStreamErr)

	if strings.Contains(safeErr.Error(), "secret123") || strings.Contains(safeErr.Error(), "admin") {
		t.Errorf("SanitizeError leaked credentials in stream error: %s", safeErr.Error())
	}
	if safeErr.Code != "STREAM_ERROR" {
		t.Errorf("Expected code STREAM_ERROR, got %s", safeErr.Code)
	}

	// Corrupt credentials error
	corruptErr := credentials.ErrCorrupt
	safeErr = SanitizeError(corruptErr)
	if safeErr.Code != "CREDENTIALS_CORRUPT" {
		t.Errorf("Expected code CREDENTIALS_CORRUPT, got %s", safeErr.Code)
	}

	// Nil error
	safeErr = SanitizeError(nil)
	if safeErr.Code != "OK" {
		t.Errorf("Expected code OK for nil error, got %s", safeErr.Code)
	}
}
