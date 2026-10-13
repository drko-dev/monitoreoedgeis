package installer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func validReadySnapshot() FullEdgePreparationSnapshot {
	readyComponents := []ComponentProgressItem{
		{Key: "python", Name: "Python", Status: ComponentStatusReady},
		{Key: "wheelhouse", Name: "Wheelhouse", Status: ComponentStatusReady},
		{Key: "worker", Name: "Vision Worker", Status: ComponentStatusReady},
		{Key: "ffmpeg", Name: "FFmpeg", Status: ComponentStatusReady},
		{Key: "ffprobe", Name: "FFprobe", Status: ComponentStatusReady},
		{Key: "authorized_models", Name: "Modelos autorizados", Status: ComponentStatusReady},
	}
	return FullEdgePreparationSnapshot{
		State:           StateReady,
		ProgressPercent: 100,
		Components:      readyComponents,
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
}

func TestValidateFullEdgePreparationReadyRequiresCompleteCertification(t *testing.T) {
	valid := validReadySnapshot()
	if err := ValidateFullEdgePreparationSnapshot(valid); err != nil {
		t.Fatalf("complete certification rejected: %v", err)
	}
	if !valid.CertifiedReady() {
		t.Fatal("valid snapshot must report CertifiedReady() == true")
	}

	tests := []struct {
		name   string
		mutate func(*FullEdgePreparationSnapshot)
	}{
		{name: "certificate absent", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification = nil }},
		{name: "missing dependencies is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.DependenciesVerified = false }},
		{name: "missing media is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.FFprobeVerified = false }},
		{name: "unauthorized model is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.ModelAuthorized = false }},
		{name: "unverified model integrity is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.ModelIntegrityVerified = false }},
		{name: "inference not validated is not ready", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.InferenceSmokeTestPassed = false }},
		{name: "malformed manifest hash", mutate: func(s *FullEdgePreparationSnapshot) { s.Certification.RuntimeManifestSHA256 = "template" }},
		{name: "not 100 percent", mutate: func(s *FullEdgePreparationSnapshot) { s.ProgressPercent = 99 }},
		{name: "component python pending", mutate: func(s *FullEdgePreparationSnapshot) { s.Components[0].Status = ComponentStatusPending }},
		{name: "component model failed", mutate: func(s *FullEdgePreparationSnapshot) { s.Components[5].Status = ComponentStatusFailed }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validReadySnapshot()
			tt.mutate(&got)
			if err := ValidateFullEdgePreparationSnapshot(got); err == nil {
				t.Fatal("expected READY validation to fail closed")
			}
			if got.CertifiedReady() {
				t.Fatal("CertifiedReady() must return false when any certification gate fails")
			}
		})
	}
}

func TestFullEdgePreparation_AllEightStatesValidAndClassified(t *testing.T) {
	states := []FullEdgePreparationState{
		StateNotPrepared,
		StateChecking,
		StatePreparing,
		StateVerifying,
		StateReady,
		StateRecoveryRequired,
		StateFailed,
		StateCancelled,
	}

	for _, st := range states {
		if !knownPreparationState(st) {
			t.Errorf("expected state %s to be recognized", st)
		}
	}

	if knownPreparationState("UNKNOWN_STATE") {
		t.Error("unknown state must not be recognized")
	}

	// Active states classification
	if !PreparationIsActive(StateChecking) || !PreparationIsActive(StatePreparing) || !PreparationIsActive(StateVerifying) {
		t.Error("CHECKING, PREPARING, and VERIFYING must be active")
	}
	if PreparationIsActive(StateNotPrepared) || PreparationIsActive(StateReady) || PreparationIsActive(StateFailed) || PreparationIsActive(StateCancelled) || PreparationIsActive(StateRecoveryRequired) {
		t.Error("inactive states must not be reported as active")
	}
}

func TestComponentProgressReporting(t *testing.T) {
	comps := DefaultComponentProgress()
	if len(comps) != 6 {
		t.Fatalf("expected 6 default components, got %d", len(comps))
	}

	expectedKeys := map[string]string{
		"python":            "Python",
		"wheelhouse":        "Wheelhouse",
		"worker":            "Vision Worker",
		"ffmpeg":            "FFmpeg",
		"ffprobe":           "FFprobe",
		"authorized_models": "Modelos autorizados",
	}

	for _, c := range comps {
		label, ok := expectedKeys[c.Key]
		if !ok {
			t.Errorf("unexpected component key: %s", c.Key)
		}
		if c.Name != label {
			t.Errorf("key %s: expected label %s, got %s", c.Key, label, c.Name)
		}
		if c.Status != ComponentStatusPending {
			t.Errorf("key %s: expected initial status PENDING, got %s", c.Key, c.Status)
		}
	}
}

func TestPreparationSnapshotRejectsContradictoryCapabilities(t *testing.T) {
	tests := []FullEdgePreparationSnapshot{
		{State: StatePreparing, CanStart: true},
		{State: StateFailed, CancellationAllowed: true},
		{State: StateReady, RecoveryAvailable: true},
		{State: StateChecking, ProgressPercent: 101},
		{State: StateChecking, ProgressPercent: -1},
		{State: FullEdgePreparationState("unknown")},
		{State: StateChecking, Certification: validReadySnapshot().Certification},
	}
	for i, snapshot := range tests {
		if err := ValidateFullEdgePreparationSnapshot(snapshot); err == nil {
			t.Errorf("case %d accepted contradictory snapshot: %+v", i, snapshot)
		}
	}
}

func TestPreparationTransitionsMatrix(t *testing.T) {
	tests := []struct {
		from, to FullEdgePreparationState
		want     bool
	}{
		// Legal progressions
		{StateNotPrepared, StateChecking, true},
		{StateNotPrepared, StatePreparing, true},
		{StateChecking, StatePreparing, true},
		{StateChecking, StateVerifying, true},
		{StatePreparing, StateVerifying, true},
		{StateVerifying, StateReady, true},

		// Cancellations from active states
		{StateChecking, StateCancelled, true},
		{StatePreparing, StateCancelled, true},
		{StateVerifying, StateCancelled, true},

		// Errors & Failures
		{StateChecking, StateFailed, true},
		{StatePreparing, StateFailed, true},
		{StateVerifying, StateFailed, true},

		// Recovery transitions
		{StatePreparing, StateRecoveryRequired, true},
		{StateVerifying, StateRecoveryRequired, true},
		{StateRecoveryRequired, StatePreparing, true},

		// Retries
		{StateFailed, StatePreparing, true},
		{StateCancelled, StatePreparing, true},

		// Illegal jumps
		{StateNotPrepared, StateReady, false},
		{StateFailed, StateReady, false},
		{StateReady, StatePreparing, false},
		{StateReady, StateVerifying, false},
		{StateCancelled, StateReady, false},
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
	if snapshot.State != StateFailed || snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected StateFailed and PREPARATION_NOT_INTEGRATED, got state=%s code=%s", snapshot.State, snapshot.ErrorCode)
	}
	if _, err := adapter.Start(context.Background(), "request-1"); !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("Start error = %v, want ErrPreparationNotWired", err)
	}
	if _, err := adapter.Cancel(context.Background(), "op-1"); !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("Cancel error = %v, want ErrPreparationNotWired", err)
	}
	if _, err := adapter.Recover(context.Background(), "op-1"); !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("Recover error = %v, want ErrPreparationNotWired", err)
	}
	diagnostic, err := adapter.Diagnostic(context.Background(), "")
	if err != nil || diagnostic.Code != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("unexpected safe diagnostic: %+v, %v", diagnostic, err)
	}
	if strings.Contains(strings.ToLower(diagnostic.Summary), "stack") {
		t.Fatalf("diagnostic must not expose a stack trace: %q", diagnostic.Summary)
	}
	if len(diagnostic.Checks) != 5 {
		t.Errorf("expected 5 checks in diagnostic, got %d", len(diagnostic.Checks))
	}
}

type testOnlyPreparationAdapter struct{}

func (testOnlyPreparationAdapter) Status(context.Context) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{
		State:               StateVerifying,
		Stage:               "manifest",
		ProgressPercent:     20,
		OperationID:         "test-op",
		CancellationAllowed: true,
		Components:          DefaultComponentProgress(),
	}, nil
}

func (testOnlyPreparationAdapter) Start(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{
		State:           StateFailed,
		ProgressPercent: 20,
		ErrorCode:       "HASH_MISMATCH",
		Retryable:       true,
		Components:      DefaultComponentProgress(),
	}, nil
}

func (testOnlyPreparationAdapter) Cancel(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{
		State:           StateCancelled,
		ProgressPercent: 20,
		Components:      DefaultComponentProgress(),
	}, nil
}

func (testOnlyPreparationAdapter) Diagnostic(context.Context, string) (FullEdgePreparationDiagnostic, error) {
	return FullEdgePreparationDiagnostic{
		Code:    "HASH_MISMATCH",
		Summary: "A packaged artifact failed integrity verification.",
	}, nil
}

func (testOnlyPreparationAdapter) Recover(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return FullEdgePreparationSnapshot{
		State:             StateRecoveryRequired,
		ProgressPercent:   20,
		RecoveryAvailable: true,
		Components:        DefaultComponentProgress(),
	}, nil
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
