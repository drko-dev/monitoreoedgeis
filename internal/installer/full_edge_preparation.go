package installer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type FullEdgePreparationState string

const (
	PreparationInitial                FullEdgePreparationState = "initial"
	PreparationPackDetected           FullEdgePreparationState = "runtime_pack_detected"
	PreparationPending                FullEdgePreparationState = "preparation_pending"
	PreparationVerifyingIntegrity     FullEdgePreparationState = "verifying_integrity"
	PreparationInstallingDependencies FullEdgePreparationState = "installing_dependencies"
	PreparationPreparingWorker        FullEdgePreparationState = "preparing_worker"
	PreparationVerifyingMedia         FullEdgePreparationState = "verifying_media"
	PreparationValidatingModel        FullEdgePreparationState = "validating_model"
	PreparationRuntimeReady           FullEdgePreparationState = "runtime_ready"
	PreparationRecoverableError       FullEdgePreparationState = "recoverable_error"
	PreparationBlockedError           FullEdgePreparationState = "blocked_error"
	PreparationCancelled              FullEdgePreparationState = "cancelled"
	PreparationRecoveryPending        FullEdgePreparationState = "recovery_pending"
	PreparationRecovering             FullEdgePreparationState = "recovering"
	PreparationRecovered              FullEdgePreparationState = "recovered"
)

var (
	ErrPreparationInProgress = errors.New("Full Edge preparation is already in progress")
	ErrPreparationNotReady   = errors.New("Full Edge preparation is not in a recoverable state")
	ErrPreparationNotWired   = errors.New("offline Full Edge preparation is not integrated in this build")
)

// RuntimeCertification is backend evidence for the complete Full Edge runtime.
// A frontend must not infer readiness from Python or dependencies alone.
type RuntimeCertification struct {
	CertificationID          string    `json:"certification_id"`
	RuntimePackVersion       string    `json:"runtime_pack_version"`
	RuntimeManifestSHA256    string    `json:"runtime_manifest_sha256"`
	Platform                 string    `json:"platform"`
	EvidenceLevel            string    `json:"evidence_level"`
	RuntimePackVerified      bool      `json:"runtime_pack_verified"`
	DependenciesVerified     bool      `json:"dependencies_verified"`
	VisionWorkerReady        bool      `json:"vision_worker_ready"`
	FFmpegVerified           bool      `json:"ffmpeg_verified"`
	FFprobeVerified          bool      `json:"ffprobe_verified"`
	ModelPackVersion         string    `json:"model_pack_version"`
	ModelManifestSHA256      string    `json:"model_manifest_sha256"`
	ModelAuthorized          bool      `json:"model_authorized"`
	ModelIntegrityVerified   bool      `json:"model_integrity_verified"`
	InferenceSmokeTestPassed bool      `json:"inference_smoke_test_passed"`
	CertifiedAt              time.Time `json:"certified_at"`
}

func (c *RuntimeCertification) Valid() bool {
	return c != nil &&
		strings.TrimSpace(c.CertificationID) != "" &&
		strings.TrimSpace(c.RuntimePackVersion) != "" &&
		isSHA256Digest(c.RuntimeManifestSHA256) &&
		strings.TrimSpace(c.Platform) != "" &&
		c.EvidenceLevel == "verified" &&
		c.RuntimePackVerified && c.DependenciesVerified && c.VisionWorkerReady &&
		c.FFmpegVerified && c.FFprobeVerified &&
		strings.TrimSpace(c.ModelPackVersion) != "" &&
		isSHA256Digest(c.ModelManifestSHA256) && c.ModelAuthorized &&
		c.ModelIntegrityVerified && c.InferenceSmokeTestPassed && !c.CertifiedAt.IsZero()
}

func isSHA256Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type FullEdgePreparationSnapshot struct {
	State                FullEdgePreparationState `json:"state"`
	Stage                string                   `json:"stage,omitempty"`
	ProgressPercent      int                      `json:"progress_percent"`
	OperationID          string                   `json:"operation_id,omitempty"`
	CanStart             bool                     `json:"can_start"`
	Retryable            bool                     `json:"retryable"`
	CancellationAllowed  bool                     `json:"cancellation_allowed"`
	RecoveryAvailable    bool                     `json:"recovery_available"`
	ErrorCode            string                   `json:"error_code,omitempty"`
	SafeMessage          string                   `json:"safe_message,omitempty"`
	DiagnosticsAvailable bool                     `json:"diagnostics_available"`
	Certification        *RuntimeCertification    `json:"certification,omitempty"`
}

func (s FullEdgePreparationSnapshot) CertifiedReady() bool {
	return s.State == PreparationRuntimeReady && s.ProgressPercent == 100 && s.Certification.Valid()
}

func ValidateFullEdgePreparationSnapshot(s FullEdgePreparationSnapshot) error {
	if !knownPreparationState(s.State) {
		return fmt.Errorf("unknown Full Edge preparation state %q", s.State)
	}
	if s.ProgressPercent < 0 || s.ProgressPercent > 100 {
		return errors.New("Full Edge preparation progress must be between 0 and 100")
	}
	if s.State == PreparationRuntimeReady && !s.CertifiedReady() {
		return errors.New("runtime_ready requires complete backend certification")
	}
	if s.State != PreparationRuntimeReady && s.Certification != nil && s.Certification.Valid() {
		return errors.New("complete certification cannot accompany a non-ready state")
	}
	if s.CanStart && s.State != PreparationPackDetected && !(s.State == PreparationRecoverableError && s.Retryable) {
		return errors.New("preparation can start only when a pack is detected or a retry is safe")
	}
	if s.CancellationAllowed && !PreparationIsActive(s.State) {
		return errors.New("cancellation is allowed only while preparation is active")
	}
	if s.RecoveryAvailable && s.State != PreparationRecoveryPending {
		return errors.New("recovery is available only when recovery is pending")
	}
	return nil
}

func PreparationIsActive(state FullEdgePreparationState) bool {
	switch state {
	case PreparationPending, PreparationVerifyingIntegrity, PreparationInstallingDependencies,
		PreparationPreparingWorker, PreparationVerifyingMedia, PreparationValidatingModel,
		PreparationRecovering:
		return true
	default:
		return false
	}
}

func knownPreparationState(state FullEdgePreparationState) bool {
	switch state {
	case PreparationInitial, PreparationPackDetected, PreparationPending, PreparationVerifyingIntegrity,
		PreparationInstallingDependencies, PreparationPreparingWorker, PreparationVerifyingMedia,
		PreparationValidatingModel, PreparationRuntimeReady, PreparationRecoverableError,
		PreparationBlockedError, PreparationCancelled, PreparationRecoveryPending,
		PreparationRecovering, PreparationRecovered:
		return true
	default:
		return false
	}
}

func CanTransitionPreparation(from, to FullEdgePreparationState) bool {
	allowed := map[FullEdgePreparationState]map[FullEdgePreparationState]bool{
		PreparationInitial:                {PreparationPackDetected: true, PreparationBlockedError: true},
		PreparationPackDetected:           {PreparationPending: true, PreparationBlockedError: true},
		PreparationPending:                {PreparationVerifyingIntegrity: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationVerifyingIntegrity:     {PreparationInstallingDependencies: true, PreparationPreparingWorker: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationInstallingDependencies: {PreparationPreparingWorker: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationPreparingWorker:        {PreparationVerifyingMedia: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationVerifyingMedia:         {PreparationValidatingModel: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationValidatingModel:        {PreparationRuntimeReady: true, PreparationRecoverableError: true, PreparationBlockedError: true, PreparationCancelled: true},
		PreparationRecoverableError:       {PreparationPending: true, PreparationRecoveryPending: true, PreparationBlockedError: true},
		PreparationCancelled:              {PreparationPending: true, PreparationRecoveryPending: true},
		PreparationRecoveryPending:        {PreparationRecovering: true, PreparationBlockedError: true},
		PreparationRecovering:             {PreparationRecovered: true, PreparationRecoverableError: true, PreparationBlockedError: true},
		PreparationRecovered:              {PreparationPackDetected: true, PreparationPending: true},
	}
	return allowed[from][to]
}

type FullEdgePreparationDiagnostic struct {
	Code    string                   `json:"code"`
	Summary string                   `json:"summary"`
	Checks  []PreparationCheckResult `json:"checks,omitempty"`
}

type PreparationCheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// FullEdgePreparationAPI is the desktop boundary. Implementations must be
// offline-only, serialize preparations across callers, and make Start
// idempotent for a request ID. It must never use legacy host Python/FFmpeg or
// report READY without RuntimeCertification.Valid().
type FullEdgePreparationAPI interface {
	Status(context.Context) (FullEdgePreparationSnapshot, error)
	Start(context.Context, string) (FullEdgePreparationSnapshot, error)
	Cancel(context.Context, string) (FullEdgePreparationSnapshot, error)
	Diagnostic(context.Context, string) (FullEdgePreparationDiagnostic, error)
	Recover(context.Context, string) (FullEdgePreparationSnapshot, error)
}

// UnavailableFullEdgePreparation is the production-safe adapter until H3/H4/H6
// contracts are integrated. It cannot invoke the legacy Python/pip/Homebrew
// provisioners and never fabricates progress or readiness.
type UnavailableFullEdgePreparation struct{}

func (UnavailableFullEdgePreparation) Status(context.Context) (FullEdgePreparationSnapshot, error) {
	return unavailablePreparationSnapshot(), nil
}

func (UnavailableFullEdgePreparation) Start(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return unavailablePreparationSnapshot(), ErrPreparationNotWired
}

func (UnavailableFullEdgePreparation) Cancel(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return unavailablePreparationSnapshot(), ErrPreparationNotWired
}

func (UnavailableFullEdgePreparation) Diagnostic(context.Context, string) (FullEdgePreparationDiagnostic, error) {
	return FullEdgePreparationDiagnostic{
		Code:    "PREPARATION_NOT_INTEGRATED",
		Summary: "Offline Full Edge preparation is not available in this build; no provisioning was attempted.",
		Checks: []PreparationCheckResult{
			{Name: "Runtime Pack executor", Status: "not_integrated"},
			{Name: "H3/H4/H6 preparation adapters", Status: "pending"},
		},
	}, nil
}

func (UnavailableFullEdgePreparation) Recover(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return unavailablePreparationSnapshot(), ErrPreparationNotWired
}

func unavailablePreparationSnapshot() FullEdgePreparationSnapshot {
	return FullEdgePreparationSnapshot{
		State:                PreparationBlockedError,
		ProgressPercent:      0,
		CanStart:             false,
		Retryable:            false,
		CancellationAllowed:  false,
		RecoveryAvailable:    false,
		ErrorCode:            "PREPARATION_NOT_INTEGRATED",
		SafeMessage:          "Offline Full Edge preparation is not integrated in this build. No files or services were changed.",
		DiagnosticsAvailable: true,
	}
}

// PreparationStartGate provides an in-process single-flight guard to adapters
// that perform real preparation. Cross-process serialization remains the
// responsibility of the eventual Runtime Pack backend (for example, its file lock).
type PreparationStartGate struct {
	mu      sync.Mutex
	active  bool
	request string
}

func (g *PreparationStartGate) Begin(requestID string) (release func(), duplicate bool, err error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, false, errors.New("preparation request ID is required")
	}
	g.mu.Lock()
	if g.active {
		if g.request == requestID {
			g.mu.Unlock()
			return func() {}, true, nil
		}
		g.mu.Unlock()
		return nil, false, ErrPreparationInProgress
	}
	g.active = true
	g.request = requestID
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.active = false
			g.request = ""
			g.mu.Unlock()
		})
	}, false, nil
}
