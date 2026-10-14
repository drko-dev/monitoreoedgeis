package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

type h6cBackendErrorSnapshot struct {
	installer.UnavailableFullEdgePreparation
	snapshot installer.FullEdgePreparationSnapshot
	err      error
}

func (m h6cBackendErrorSnapshot) Status(context.Context) (installer.FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m h6cBackendErrorSnapshot) Start(context.Context, string) (installer.FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m h6cBackendErrorSnapshot) Cancel(context.Context, string) (installer.FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m h6cBackendErrorSnapshot) Recover(context.Context, string) (installer.FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}

func h6cCertifiedSnapshotFixture() installer.FullEdgePreparationSnapshot {
	components := installer.DefaultComponentProgress()
	for i := range components {
		components[i].Status = installer.ComponentStatusReady
	}
	return installer.FullEdgePreparationSnapshot{
		State: installer.StateReady, ProgressPercent: 100,
		Components: components,
		Certification: &installer.RuntimeCertification{
			CertificationID:       "synthetic-only",
			RuntimePackVersion:    "synthetic-1",
			RuntimeManifestSHA256: strings.Repeat("a", 64),
			Platform:              "darwin/arm64", EvidenceLevel: "verified",
			RuntimePackVerified: true, DependenciesVerified: true,
			VisionWorkerReady: true, FFmpegVerified: true,
			FFprobeVerified: true, ModelPackVersion: "synthetic-model",
			ModelManifestSHA256: strings.Repeat("b", 64),
			ModelAuthorized:     true, ModelIntegrityVerified: true,
			InferenceSmokeTestPassed: true, CertifiedAt: time.Now(),
		},
	}
}

func TestH6CWailsBackendErrorNeverLeaksReadyOrInvalidSnapshot(t *testing.T) {
	backendErr := errors.New("synthetic backend error")
	tests := []struct {
		name     string
		snapshot installer.FullEdgePreparationSnapshot
		wantCode string
	}{
		{
			name: "invalid ready with error",
			snapshot: installer.FullEdgePreparationSnapshot{
				State: installer.StateReady, ProgressPercent: 100,
			},
			wantCode: "INVALID_PREPARATION_STATUS",
		},
		{
			name:     "valid certification but operation failed",
			snapshot: h6cCertifiedSnapshotFixture(),
			wantCode: "PREPARATION_BACKEND_ERROR",
		},
		{
			name: "unknown status with backend error",
			snapshot: installer.FullEdgePreparationSnapshot{
				State: "UNRECOGNIZED",
			},
			wantCode: "INVALID_PREPARATION_STATUS",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewApp()
			a.preparation = h6cBackendErrorSnapshot{snapshot: tc.snapshot, err: backendErr}
			a.startup(context.Background())
			operations := []struct {
				name string
				fn   func() (installer.FullEdgePreparationSnapshot, error)
			}{
				{"Status", a.GetFullEdgePreparationStatus},
				{"Start", func() (installer.FullEdgePreparationSnapshot, error) {
					return a.StartFullEdgePreparation("synthetic-request")
				}},
				{"Cancel", func() (installer.FullEdgePreparationSnapshot, error) {
					return a.CancelFullEdgePreparation("synthetic-request")
				}},
				{"Recover", func() (installer.FullEdgePreparationSnapshot, error) {
					return a.RecoverFullEdgeRuntime("synthetic-request")
				}},
			}
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					got, err := op.fn()
					if !errors.Is(err, backendErr) {
						t.Fatalf("backend error lost: %v", err)
					}
					if got.State != installer.StateFailed || got.ErrorCode != tc.wantCode ||
						got.CertifiedReady() || got.Certification != nil ||
						got.CanStart || got.RecoveryAvailable {
						t.Fatalf("unsafe snapshot escaped Wails: %+v", got)
					}
					if err := installer.ValidateFullEdgePreparationSnapshot(got); err != nil {
						t.Fatalf("sanitized snapshot not valid: %v", err)
					}
				})
			}
		})
	}
}

func TestH6CWailsPreservesValidRecoveryRequiredAlongsideBackendError(t *testing.T) {
	errBackend := errors.New("synthetic recovery requires inspection")
	snap := installer.FullEdgePreparationSnapshot{
		State:             installer.StateRecoveryRequired,
		RecoveryAvailable: true, ErrorCode: "RUNTIME_RECOVERY_REQUIRED",
		DiagnosticsAvailable: true, Components: installer.DefaultComponentProgress(),
	}
	app := NewApp()
	app.preparation = h6cBackendErrorSnapshot{snapshot: snap, err: errBackend}
	got, err := app.RecoverFullEdgeRuntime("restore-synthetic")
	if !errors.Is(err, errBackend) || got.State != installer.StateRecoveryRequired ||
		!got.RecoveryAvailable || got.CertifiedReady() {
		t.Fatalf("lost recovery evidence or false readiness: %+v err=%v", got, err)
	}
}

func TestH6CDefaultWailsAppCannotPrepareWithoutReleaseOptIn(t *testing.T) {
	app := NewApp()
	app.startup(context.Background())
	s, err := app.StartFullEdgePreparation("synthetic-request")
	if !errors.Is(err, installer.ErrPreparationNotWired) || s.CertifiedReady() ||
		s.State == installer.StateReady || s.CanStart {
		t.Fatalf("default Wails app unexpectedly enabled provisioning: %+v err=%v", s, err)
	}
}
