package installer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCertifyInstalledRuntimeReal certifies a really installed runtime.
// Opt-in: H8_DATA_DIR (a data dir after one-click preparation), H8_FIXTURE.
func TestCertifyInstalledRuntimeReal(t *testing.T) {
	dataDir, fixture := os.Getenv("H8_DATA_DIR"), os.Getenv("H8_FIXTURE")
	if dataDir == "" || fixture == "" {
		t.Skip("H8_DATA_DIR/H8_FIXTURE not set")
	}
	pin := strings.Repeat("a", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	cert, err := CertifyInstalledRuntime(ctx, dataDir, fixture, "test", pin)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.Valid() || !CertifiedSnapshot(cert).CertifiedReady() {
		t.Fatalf("certification not valid: %+v", cert)
	}
	t.Logf("certified in %s: %+v", time.Since(start), cert)
}

func TestRuntimeCertificationPersistenceIsBound(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(dataDir+"/models", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{certPersonModel, certVehicleModel} {
		if err := os.WriteFile(dataDir+"/models/"+name, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := modelsManifestSHA256(dataDir+"/models", certPersonModel, certVehicleModel)
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("b", 64)
	cert := &RuntimeCertification{
		CertificationID: "x", RuntimePackVersion: "v", RuntimeManifestSHA256: pin, Platform: "darwin/arm64",
		EvidenceLevel: "verified", RuntimePackVerified: true, DependenciesVerified: true, VisionWorkerReady: true,
		FFmpegVerified: true, FFprobeVerified: true, ModelPackVersion: "m", ModelManifestSHA256: digest,
		ModelAuthorized: true, ModelIntegrityVerified: true, InferenceSmokeTestPassed: true, CertifiedAt: time.Now(),
	}
	if err := SaveRuntimeCertification(dataDir, cert); err != nil {
		t.Fatal(err)
	}
	if LoadRuntimeCertification(dataDir, pin) == nil {
		t.Fatal("matching certification not loaded")
	}
	if LoadRuntimeCertification(dataDir, strings.Repeat("c", 64)) != nil {
		t.Fatal("certification for another manifest accepted")
	}
	if err := os.WriteFile(dataDir+"/models/"+certVehicleModel, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if LoadRuntimeCertification(dataDir, pin) != nil {
		t.Fatal("certification accepted after model weights changed")
	}
}
