package installer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateFullEdgePreparationReadyRequiresCompleteCertification(t *testing.T) {
	valid := FullEdgePreparationSnapshot{
		State:           PreparationRuntimeReady,
		ProgressPercent: 100,
		Certification: &RuntimeCertification{
			CertificationID:          "cert-1",
			RuntimePackVersion:       "pack-1",
			RuntimeManifestSHA256:    strings.Repeat("a", 64),
			Platform:                 "darwin/arm64",
			EvidenceLevel:            "verified",
			RuntimePackVerified:      true,
			DependenciesVerified:     true,
			VisionWorkerReady:        true,
			FFmpegVerified:           true,
			FFprobeVerified:          true,
			ModelPackVersion:         "models-1",
			ModelManifestSHA256:      strings.Repeat("b", 64),
			ModelAuthorized:          true,
			ModelIntegrityVerified:   true,
			InferenceSmokeTestPassed: true,
			CertifiedAt:              time.Now(),
		},
	}
	if err := ValidateFullEdgePreparationSnapshot(valid); err != nil {
		t.Fatalf("complete certification rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*FullEdgePreparationSnapshot)
	}{
		{name: "certificate absent", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification = nil }},
		{name: "missing dependencies is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.DependenciesVerified = false }},
		{name: "missing media is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.FFprobeVerified = false }},
		{name: "unauthorized model is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.ModelAuthorized = false }},
		{name: "inference not validated is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.InferenceSmokeTestPassed = false }},
		{name: "malformed manifest hash", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.RuntimeManifestSHA256 = "template" }},
		{name: "not 100 percent", mutate: func(s *FullEdgePreparationSnapshot) { s.ProgressPercent = 99 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			cert := *valid.Certification
			got.Certification = &cert
			tt.mutate(&got)
			if err := ValidateFullEdgePreparationSnapshot(got); err == nil {
				t.Fatal("expected runtime_ready to fail closed")
			}
		})
	}
}

func TestPreparationSnapshotRejectsContradictoryCapabilities(t *testing.T) {
	tests := []FullEdgePreparationSnapshot{
		{State: PreparationInitial, CanStart: true},
		{State: PreparationBlockedError, CancellationAllowed: true},
		{State: PreparationRecoverableError, RecoveryAvailable: true},
		{State: PreparationPending, ProgressPercent: 101},
		{State: FullEdgePreparationState("unknown")},
	}
	for i, snapshot := range tests {
		if err := ValidateFullEdgePreparationSnapshot(snapshot); err == nil {
			t.Errorf("case %d accepted contradictory snapshot: %+v", i, snapshot)
		}
	}
}

func TestPreparationTransitions(t *testing.T) {
	tests := []struct {
		from, to FullEdgePreparationState
		want     bool
	}{
		{PreparationInitial, PreparationPackDetected, true},
		{PreparationPackDetected, PreparationPending, true},
		{PreparationPending, PreparationVerifyingIntegrity, true},
		{PreparationVerifyingIntegrity, PreparationInstallingDependencies, true},
		{PreparationValidatingModel, PreparationRuntimeReady, true},
		{PreparationRuntimeReady, PreparationPending, false},
		{PreparationBlockedError, PreparationPending, false},
		{PreparationRecoverableError, PreparationPending, true},
		{PreparationRecoveryPending, PreparationRecovering, true},
		{PreparationRecovering, PreparationRecovered, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			if got := CanTransitionPreparation(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransitionPreparation(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestUnavailablePreparationNeverClaimsReadyOrRunsLegacyProvisioning(t *testing.T) {
	adapter := UnavailableFullEdgePreparation{}
	snapshot, err := adapter.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CertifiedReady() || snapshot.CanStart || snapshot.Certification != nil {
		t.Fatalf("unavailable adapter must remain fail-closed: %+v", snapshot)
	}
	if _, err := adapter.Start(context.Background(), "request-1"); !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("Start error = %v, want ErrPreparationNotWired", err)
	}
	diagnostic, err := adapter.Diagnostic(context.Background(), "")
	if err != nil || diagnostic.Code != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("unexpected safe diagnostic: %+v, %v", diagnostic, err)
	}
	if strings.Contains(strings.ToLower(diagnostic.Summary), "stack") {
		t.Fatalf("diagnostic must not expose a stack trace: %q", diagnostic.Summary)
	}
}

// testOnlyPreparationAdapter simulates an interrupted operation; it never
// returns a RuntimeCertification and is not used by the desktop application.
type testOnlyPreparationAdapter struct{}

func (testOnlyPreparationAdapter) Status(context.Context) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{State: PreparationVerifyingIntegrity, Stage: "manifest", ProgressPercent: 20, OperationID: "test-op", CancellationAllowed: true}, nil
}
func (testOnlyPreparationAdapter) Start(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{State: PreparationRecoverableError, ProgressPercent: 20, ErrorCode: "HASH_MISMATCH", Retryable: true}, nil
}
func (testOnlyPreparationAdapter) Cancel(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{State: PreparationCancelled, ProgressPercent: 20}, nil
}
func (testOnlyPreparationAdapter) Diagnostic(context.Context, string) (FullEdgePreparationDiagnostic, error) {
	return FullEdgePreparationDiagnostic{Code: "HASH_MISMATCH", Summary: "A packaged artifact failed integrity verification."}, nil
}
func (testOnlyPreparationAdapter) Recover(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{State: PreparationRecoveryPending, ProgressPercent: 20, RecoveryAvailable: true}, nil
}

func TestTestAdapterFailureDoesNotPromoteReadiness(t *testing.T) {
	snapshot, err := testOnlyPreparationAdapter{}.Start(context.Background(), "test-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFullEdgePreparationSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.CertifiedReady() || snapshot.Certification != nil {
		t.Fatalf("test adapter fabricated a successful certificate: %+v", snapshot)
	}
}

func TestPreparationStartGateSerializesAndSupportsSameRequestRetry(t *testing.T) {
	var gate PreparationStartGate
	release, duplicate, err := gate.Begin("same-request")
	if err != nil || duplicate || release == nil {
		t.Fatalf("first Begin: release=%v duplicate=%v err=%v", release != nil, duplicate, err)
	}
	_, duplicate, err = gate.Begin("same-request")
	if err != nil || !duplicate {
		t.Fatalf("same request must be idempotent: duplicate=%v err=%v", duplicate, err)
	}
	if _, _, err := gate.Begin("different-request"); !errors.Is(err, ErrPreparationInProgress) {
		t.Fatalf("concurrent operation error = %v", err)
	}
	release()
	release()
	if _, _, err := gate.Begin("next-request"); err != nil {
		t.Fatalf("gate did not release after completion: %v", err)
	}
}

func TestPreparationStartGateConcurrentCallers(t *testing.T) {
	var gate PreparationStartGate
	release, _, err := gate.Begin("first")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, got := gate.Begin("other"); !errors.Is(got, ErrPreparationInProgress) {
				t.Errorf("concurrent Begin error = %v", got)
			}
		}()
	}
	wg.Wait()
	release()
}
