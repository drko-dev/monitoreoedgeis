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

// FullEdgePreparationState represents the high-level preparation state machine
// for the Full Edge offline runtime contract.
type FullEdgePreparationState string

const (
	StateNotPrepared      FullEdgePreparationState = "NOT_PREPARED"
	StateChecking         FullEdgePreparationState = "CHECKING"
	StatePreparing        FullEdgePreparationState = "PREPARING"
	StateVerifying        FullEdgePreparationState = "VERIFYING"
	StateReady            FullEdgePreparationState = "READY"
	StateRecoveryRequired FullEdgePreparationState = "RECOVERY_REQUIRED"
	StateFailed           FullEdgePreparationState = "FAILED"
	StateCancelled        FullEdgePreparationState = "CANCELLED"
)

// Standard preparation state constants
const (
	PreparationNotPrepared      = StateNotPrepared
	PreparationChecking         = StateChecking
	PreparationPreparing        = StatePreparing
	PreparationVerifying        = StateVerifying
	PreparationReady            = StateReady
	PreparationRecoveryRequired = StateRecoveryRequired
	PreparationFailed           = StateFailed
	PreparationCancelled        = StateCancelled

	// Deprecated lowercase aliases mapped to canonical states
	PreparationInitial                = StateNotPrepared
	PreparationPackDetected           = StateNotPrepared
	PreparationPending                = StateChecking
	PreparationVerifyingIntegrity     = StateVerifying
	PreparationInstallingDependencies = StatePreparing
	PreparationPreparingWorker        = StatePreparing
	PreparationVerifyingMedia         = StateVerifying
	PreparationValidatingModel        = StateVerifying
	PreparationRuntimeReady           = StateReady
	PreparationRecoverableError       = StateFailed
	PreparationBlockedError           = StateFailed
	PreparationRecoveryPending        = StateRecoveryRequired
	PreparationRecovering             = StatePreparing
	PreparationRecovered              = StateNotPrepared
)

// ComponentStatus represents the health and readiness of an individual runtime subsystem.
type ComponentStatus string

const (
	ComponentStatusPending    ComponentStatus = "PENDING"
	ComponentStatusInProgress ComponentStatus = "IN_PROGRESS"
	ComponentStatusReady      ComponentStatus = "READY"
	ComponentStatusFailed     ComponentStatus = "FAILED"
)

// ComponentProgressItem reports progress for each required runtime subsystem.
type ComponentProgressItem struct {
	Key    string          `json:"key"`    // "python", "wheelhouse", "worker", "ffmpeg", "ffprobe", "authorized_models"
	Name   string          `json:"name"`   // "Python", "Wheelhouse", "Vision Worker", "FFmpeg", "FFprobe", "Modelos autorizados"
	Status ComponentStatus `json:"status"` // "PENDING", "IN_PROGRESS", "READY", "FAILED"
	Detail string          `json:"detail,omitempty"`
}

// DefaultComponentProgress returns the initial pending progress list for all 6 required components.
func DefaultComponentProgress() []ComponentProgressItem {
	return []ComponentProgressItem{
		{Key: "python", Name: "Python", Status: ComponentStatusPending},
		{Key: "wheelhouse", Name: "Wheelhouse", Status: ComponentStatusPending},
		{Key: "worker", Name: "Vision Worker", Status: ComponentStatusPending},
		{Key: "ffmpeg", Name: "FFmpeg", Status: ComponentStatusPending},
		{Key: "ffprobe", Name: "FFprobe", Status: ComponentStatusPending},
		{Key: "authorized_models", Name: "Modelos autorizados", Status: ComponentStatusPending},
	}
}

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
	Components           []ComponentProgressItem  `json:"components,omitempty"`
	Certification        *RuntimeCertification    `json:"certification,omitempty"`
}

func (s FullEdgePreparationSnapshot) CertifiedReady() bool {
	if s.State != StateReady || s.ProgressPercent != 100 || !s.Certification.Valid() {
		return false
	}
	// Require exactly one READY report for every required component. An absent,
	// incomplete, duplicate, or unknown component list must fail closed.
	required := map[string]struct{}{
		"python": {}, "wheelhouse": {}, "worker": {},
		"ffmpeg": {}, "ffprobe": {}, "authorized_models": {},
	}
	if len(s.Components) != len(required) {
		return false
	}
	for _, comp := range s.Components {
		if comp.Status != ComponentStatusReady {
			return false
		}
		if _, ok := required[comp.Key]; !ok {
			return false
		}
		delete(required, comp.Key)
	}
	if len(required) != 0 {
		return false
	}
	return true
}

func ValidateFullEdgePreparationSnapshot(s FullEdgePreparationSnapshot) error {
	if !knownPreparationState(s.State) {
		return fmt.Errorf("unknown Full Edge preparation state %q", s.State)
	}
	if s.ProgressPercent < 0 || s.ProgressPercent > 100 {
		return errors.New("Full Edge preparation progress must be between 0 and 100")
	}
	if s.State == StateReady && !s.CertifiedReady() {
		return errors.New("READY requires complete backend certification and all components READY")
	}
	if s.State != StateReady && s.Certification != nil && s.Certification.Valid() {
		return errors.New("complete certification cannot accompany a non-ready state")
	}
	if s.CanStart && s.State != StateNotPrepared && !(s.State == StateFailed && s.Retryable) && s.State != StateCancelled {
		return errors.New("preparation can start only when NOT_PREPARED, CANCELLED, or safe retry on FAILED")
	}
	if s.CancellationAllowed && !PreparationIsActive(s.State) {
		return errors.New("cancellation is allowed only while preparation is active")
	}
	if s.RecoveryAvailable && s.State != StateRecoveryRequired {
		return errors.New("recovery is available only when recovery is required")
	}
	return nil
}

func PreparationIsActive(state FullEdgePreparationState) bool {
	switch state {
	case StateChecking, StatePreparing, StateVerifying:
		return true
	default:
		return false
	}
}

func knownPreparationState(state FullEdgePreparationState) bool {
	switch state {
	case StateNotPrepared, StateChecking, StatePreparing, StateVerifying,
		StateReady, StateRecoveryRequired, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func CanTransitionPreparation(from, to FullEdgePreparationState) bool {
	allowed := map[FullEdgePreparationState]map[FullEdgePreparationState]bool{
		StateNotPrepared: {
			StateChecking:  true,
			StatePreparing: true,
			StateFailed:    true,
		},
		StateChecking: {
			StateNotPrepared:      true,
			StatePreparing:        true,
			StateVerifying:        true,
			StateReady:            true,
			StateRecoveryRequired: true,
			StateFailed:           true,
			StateCancelled:        true,
		},
		StatePreparing: {
			StateVerifying:        true,
			StateFailed:           true,
			StateCancelled:        true,
			StateRecoveryRequired: true,
		},
		StateVerifying: {
			StateReady:            true,
			StateFailed:           true,
			StateCancelled:        true,
			StateRecoveryRequired: true,
		},
		StateReady: {
			StateChecking: true,
		},
		StateRecoveryRequired: {
			StatePreparing: true,
			StateFailed:    true,
		},
		StateFailed: {
			StateChecking:         true,
			StatePreparing:        true,
			StateRecoveryRequired: true,
		},
		StateCancelled: {
			StateChecking:         true,
			StatePreparing:        true,
			StateNotPrepared:      true,
			StateRecoveryRequired: true,
		},
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
			{Name: "Python hermetic runtime", Status: "not_provisioned"},
			{Name: "Wheelhouse dependencies", Status: "not_installed"},
			{Name: "Vision Worker bundle", Status: "not_wired"},
			{Name: "FFmpeg/FFprobe binaries", Status: "not_bundled"},
			{Name: "Authorized models", Status: "unlicensed_or_pending"},
		},
	}, nil
}

func (UnavailableFullEdgePreparation) Recover(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return unavailablePreparationSnapshot(), ErrPreparationNotWired
}

func unavailablePreparationSnapshot() FullEdgePreparationSnapshot {
	return FullEdgePreparationSnapshot{
		State:                StateFailed,
		ProgressPercent:      0,
		CanStart:             false,
		Retryable:            false,
		CancellationAllowed:  false,
		RecoveryAvailable:    false,
		ErrorCode:            "PREPARATION_NOT_INTEGRATED",
		SafeMessage:          "Offline Full Edge preparation is not integrated in this build. No files or services were changed.",
		DiagnosticsAvailable: true,
		Components:           DefaultComponentProgress(),
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
