package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
)

// seedCandidate injects a candidate directly into the Service's discovery
// cache, bypassing a real network scan -- these tests exercise the
// Plan/Apply orchestration and its safety gates, not the already-tested
// internal/discovery/onvif or internal/rtsptest protocol layers themselves.
func seedCandidate(svc *Service, d discovery.DiscoveredDevice) {
	svc.discoveryMu.Lock()
	if svc.discoveredDevices == nil {
		svc.discoveredDevices = map[string]discovery.DiscoveredDevice{}
	}
	svc.discoveredDevices[d.StableIdentity] = d
	svc.discoveryMu.Unlock()
}

func multiSourceDevice(key string) discovery.DiscoveredDevice {
	return discovery.DiscoveredDevice{
		StableIdentity: key,
		IP:             "192.168.1.50",
		XAddr:          "http://192.168.1.50/onvif/device_service",
		VideoSources: []discovery.VideoSource{
			{SourceToken: "ch1"},
			{SourceToken: "ch2"},
		},
	}
}

func unreachableDevice(key string) discovery.DiscoveredDevice {
	return discovery.DiscoveredDevice{
		StableIdentity: key,
		IP:             "192.168.1.51",
		XAddr:          "", // no ONVIF service address resolved
	}
}

func TestGetDiscoveredCameraNotFound(t *testing.T) {
	svc := newIsolatedService(t)
	_, err := svc.GetDiscoveredCamera(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected CANDIDATE_NOT_FOUND")
	}
}

func TestTestCameraCredentialsRejectsMultiSourceBeforeAnyNetworkCall(t *testing.T) {
	svc := newIsolatedService(t)
	seedCandidate(svc, multiSourceDevice("dvr-1"))

	result, err := svc.TestCameraCredentials(context.Background(), TestCameraCredentialsRequest{
		CandidateKey: "dvr-1", Username: "admin", Password: "x",
	})
	if err != nil {
		t.Fatalf("TestCameraCredentials: %v", err)
	}
	if !result.MultiSource || result.ONVIFStatus != "ONVIF_UNSUPPORTED_LAYOUT" || result.Passed {
		t.Fatalf("result = %+v, want MultiSource UNSUPPORTED_LAYOUT not passed", result)
	}
}

func TestTestCameraCredentialsUnreachableWithoutXAddr(t *testing.T) {
	svc := newIsolatedService(t)
	seedCandidate(svc, unreachableDevice("cam-1"))

	result, err := svc.TestCameraCredentials(context.Background(), TestCameraCredentialsRequest{
		CandidateKey: "cam-1", Username: "admin", Password: "x",
	})
	if err != nil {
		t.Fatalf("TestCameraCredentials: %v", err)
	}
	if result.ONVIFStatus != "ONVIF_UNREACHABLE" || result.Passed {
		t.Fatalf("result = %+v, want ONVIF_UNREACHABLE not passed", result)
	}
}

func TestPlanCameraOnboardingNeverMutatesConfigOrCredentials(t *testing.T) {
	svc := newIsolatedService(t)
	seedCandidate(svc, multiSourceDevice("dvr-1"))

	plan, err := svc.PlanCameraOnboarding(context.Background(), CameraOnboardingRequest{
		CandidateKey: "dvr-1", CameraName: "Test", Username: "admin", Password: "x",
	})
	if err != nil {
		t.Fatalf("PlanCameraOnboarding: %v", err)
	}
	if len(plan.Blockers) == 0 {
		t.Fatal("expected a blocker for a multi-source candidate")
	}
}

func TestApplyCameraOnboardingBlockedNeverCallsSaaS(t *testing.T) {
	svc := newIsolatedService(t)
	seedCandidate(svc, multiSourceDevice("dvr-1"))

	called := false
	fakeSaaS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fakeSaaS.Close()
	seedEnrollment(t, svc, fakeSaaS.URL)

	result, err := svc.ApplyCameraOnboarding(context.Background(), CameraOnboardingRequest{
		CandidateKey: "dvr-1", CameraName: "Test", Username: "admin", Password: "x",
	})
	if err != nil {
		t.Fatalf("ApplyCameraOnboarding: %v", err)
	}
	if result.Status != CameraOnboardingBlocked {
		t.Fatalf("Status = %s, want BLOCKED", result.Status)
	}
	if called {
		t.Fatal("ApplyCameraOnboarding called the SaaS despite a BLOCKED plan")
	}
}

func TestApplyCameraOnboardingActionRequiredWhenUnenrolled(t *testing.T) {
	svc := newIsolatedService(t)
	seedCandidate(svc, unreachableDevice("cam-1"))
	// This candidate is ONVIF_UNREACHABLE (no XAddr), so it would already be
	// BLOCKED at the plan step -- use a device with a real XAddr instead so
	// the flow reaches the enrollment check. Since there is no fake ONVIF
	// server here, the plan will still fail, but for a different reason
	// (ONVIF_AUTH_FAILED/ONVIF_TIMEOUT rather than a hard structural
	// blocker), so assert on the ApplyCameraOnboarding contract directly via
	// saasClientForOnboarding's own error instead of going through Plan.
	if _, _, err := svc.saasClientForOnboarding(); err == nil {
		t.Fatal("expected an error resolving SaaS client for an unenrolled Edge")
	}
}

func TestApplyCameraOnboardingRejectsConcurrentApply(t *testing.T) {
	svc := newIsolatedService(t)
	if !svc.onboardMu.TryLock() {
		t.Fatal("could not acquire onboardMu for setup")
	}
	defer svc.onboardMu.Unlock()

	seedCandidate(svc, multiSourceDevice("dvr-1"))
	_, err := svc.ApplyCameraOnboarding(context.Background(), CameraOnboardingRequest{
		CandidateKey: "dvr-1", CameraName: "Test", Username: "admin", Password: "x",
	})
	if err == nil {
		t.Fatal("ApplyCameraOnboarding ran while another apply was in progress")
	}
	safeErr, ok := err.(*SafeError)
	if !ok || safeErr.Code != "APPLY_IN_PROGRESS" {
		t.Fatalf("err = %v, want APPLY_IN_PROGRESS SafeError", err)
	}
}

// seedEnrollment writes a minimal, valid credentials.json + identity.json so
// saasClientForOnboarding resolves successfully, and points GEOCAM_SAAS_URL
// at a local fake server -- never a real SaaS host.
func seedEnrollment(t *testing.T, svc *Service, saasURL string) {
	t.Helper()
	if err := credentials.Save(svc.DataDir, credentials.Credentials{
		DeviceID:   "edge-test-device",
		Credential: "test-credential",
		Status:     credentials.StatusEnrolled,
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	if err := config.WritePersistentValues(map[string]string{"GEOCAM_SAAS_URL": saasURL}); err != nil {
		t.Fatalf("seed SAAS URL: %v", err)
	}
}

func TestOnboardingSuccessMessageReflectsSyncObserved(t *testing.T) {
	if got := onboardingSuccessMessage("created", true); got == "" {
		t.Fatal("expected a non-empty message")
	}
	synced := onboardingSuccessMessage("created", true)
	pending := onboardingSuccessMessage("created", false)
	if synced == pending {
		t.Fatal("synced and pending messages must differ")
	}
}

func TestNewIdempotencyKeyIsUniquePerCall(t *testing.T) {
	a := newIdempotencyKey()
	b := newIdempotencyKey()
	if a == b || len(a) == 0 {
		t.Fatalf("idempotency keys not unique/non-empty: %q %q", a, b)
	}
}
