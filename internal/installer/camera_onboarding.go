package installer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// CameraOnboardingRequest is the only shape the frontend can send for
// planning or applying an onboarding: a candidate this Service itself just
// discovered, a display name, and the operator-entered credential. There is
// no organization_id, device_id, or raw SaaS payload here -- the SaaS
// resolves identity from this Edge's own enrollment credential (see
// docs/product/UX4_CAMERA_IP_ONBOARDING.md).
type CameraOnboardingRequest struct {
	CandidateKey string `json:"candidate_key"`
	CameraName   string `json:"camera_name"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// CameraOnboardingPlan is what PlanCameraOnboarding returns: a preview,
// never a mutation. It re-validates the candidate fresh (ONVIF + RTSP) every
// time, so a stale plan can never be replayed against a camera that has
// since gone offline or changed.
type CameraOnboardingPlan struct {
	CandidateKey     string         `json:"candidate_key"`
	CameraName       string         `json:"camera_name"`
	Profile          *StreamProfile `json:"profile,omitempty"`
	ProcessingMode   ProcessingMode `json:"processing_mode"`
	CredentialsValid bool           `json:"credentials_valid"`
	Blockers         []string       `json:"blockers,omitempty"`
	Warnings         []string       `json:"warnings,omitempty"`
}

// PlanCameraOnboarding never mutates anything: no credential is saved, no
// SaaS call is made, no local file changes. It only re-runs the same local
// validation TestCameraCredentials already performs.
func (s *Service) PlanCameraOnboarding(ctx context.Context, req CameraOnboardingRequest) (*CameraOnboardingPlan, error) {
	validation, err := s.TestCameraCredentials(ctx, TestCameraCredentialsRequest{
		CandidateKey: req.CandidateKey,
		Username:     req.Username,
		Password:     req.Password,
	})
	if err != nil {
		return nil, err
	}

	current, err := s.GetCurrentProcessingMode(ctx)
	if err != nil {
		return nil, err
	}

	plan := &CameraOnboardingPlan{
		CandidateKey:   req.CandidateKey,
		CameraName:     req.CameraName,
		ProcessingMode: current.Mode,
	}
	if validation.MultiSource {
		plan.Blockers = append(plan.Blockers, "DVR/NVR and other multi-channel devices are not supported yet.")
		return plan, nil
	}
	if validation.ONVIFStatus != "ok" {
		plan.Blockers = append(plan.Blockers, safeOr(validation.ONVIFReason, validation.ONVIFStatus))
		return plan, nil
	}
	if validation.RTSPStatus != "ok" {
		plan.Blockers = append(plan.Blockers, safeOr(validation.RTSPReason, validation.RTSPStatus))
		return plan, nil
	}

	plan.CredentialsValid = true
	plan.Profile = validation.Profile
	return plan, nil
}

func safeOr(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

// CameraOnboardingApplyStatus is the terminal outcome of
// ApplyCameraOnboarding.
type CameraOnboardingApplyStatus string

const (
	CameraOnboardingSuccess        CameraOnboardingApplyStatus = "SUCCESS"
	CameraOnboardingActionRequired CameraOnboardingApplyStatus = "ACTION_REQUIRED"
	CameraOnboardingBlocked        CameraOnboardingApplyStatus = "BLOCKED"
	CameraOnboardingRolledBack     CameraOnboardingApplyStatus = "ROLLED_BACK"
)

// CameraOnboardingApplyResult never claims SUCCESS for a camera the SaaS did
// not confirm, and never includes the password.
type CameraOnboardingApplyResult struct {
	Status       CameraOnboardingApplyStatus `json:"status"`
	OperationID  int64                       `json:"operation_id,omitempty"`
	CandidateKey string                      `json:"candidate_key"`
	SafeMessage  string                      `json:"safe_message"`
	SyncObserved bool                        `json:"sync_observed"`
}

// saasClientForOnboarding resolves the exact same enrollment credential and
// SaaS URL the daemon's own cameracreds Syncer uses (internal/agent's
// newCameraCredsModule): config.PersistentValue("GEOCAM_SAAS_URL") and
// credentials.Load(s.DataDir). An unenrolled Edge, or one with no SaaS URL
// configured, cannot onboard a camera -- there is no separate credential
// store to fall back to.
func (s *Service) saasClientForOnboarding() (*transport.Client, credentials.Credentials, error) {
	creds, err := credentials.Load(s.DataDir)
	if err != nil {
		return nil, credentials.Credentials{}, err
	}
	if !creds.IsEnrolled() || creds.DeviceID == "" || creds.Credential == "" {
		return nil, creds, errNotEnrolled
	}
	saasURL, ok, err := config.PersistentValue("GEOCAM_SAAS_URL")
	if err != nil {
		return nil, creds, err
	}
	if !ok || saasURL == "" {
		return nil, creds, errNoSaaSURL
	}
	allowInsecure, _, _ := config.PersistentValue("GEOCAM_ALLOW_INSECURE_HTTP")
	client, err := transport.New(saasURL, allowInsecure == "true", 10*time.Second, agent.Version)
	if err != nil {
		return nil, creds, err
	}
	return client, creds, nil
}

var (
	errNotEnrolled = errors.New("edge is not enrolled")
	errNoSaaSURL   = errors.New("GEOCAM_SAAS_URL is not configured")
)

func newIdempotencyKey() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ApplyCameraOnboarding re-validates the candidate (step 1-3 of
// docs/product/UX4_CAMERA_IP_ONBOARDING.md's Apply flow), then asks the SaaS
// to create the authoritative camera/binding/credential/assignment
// (POST /api/v1/edge/camera-onboarding — SaaS side:
// docs/saas/20-edge-camera-onboarding.md in monitoreoia). It never writes a
// local credential store itself: cameracreds.Store.Apply stays exclusively
// the Syncer's job, so the only "local persistence" here is the SaaS's own
// durable record, which the already-running Syncer converges on its own
// schedule. A short bounded poll of the local encrypted cache reports
// whether that convergence already happened by the time this call returns,
// without ever blocking on it.
func (s *Service) ApplyCameraOnboarding(ctx context.Context, req CameraOnboardingRequest) (*CameraOnboardingApplyResult, error) {
	if !s.onboardMu.TryLock() {
		return nil, &SafeError{
			Code:        "APPLY_IN_PROGRESS",
			SafeMessage: "Another camera onboarding is already being applied.",
			Recoverable: true,
		}
	}
	defer s.onboardMu.Unlock()

	plan, err := s.PlanCameraOnboarding(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		return &CameraOnboardingApplyResult{
			Status:       CameraOnboardingBlocked,
			CandidateKey: req.CandidateKey,
			SafeMessage:  "This camera failed validation and was not added.",
		}, nil
	}

	client, creds, err := s.saasClientForOnboarding()
	if err != nil {
		if errors.Is(err, errNotEnrolled) || errors.Is(err, errNoSaaSURL) {
			return &CameraOnboardingApplyResult{
				Status:       CameraOnboardingActionRequired,
				CandidateKey: req.CandidateKey,
				SafeMessage:  "This Edge must be enrolled with a configured SaaS URL before a camera can be added.",
			}, nil
		}
		return nil, &SafeError{
			Code:        "CREDENTIALS_UNAVAILABLE",
			SafeMessage: "Could not read this Edge's own enrollment credential.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	result, err := client.OnboardCamera(ctx, creds.DeviceID, creds.Credential, transport.CameraOnboardingRequest{
		CandidateKey:   req.CandidateKey,
		CameraName:     req.CameraName,
		Manufacturer:   req.Manufacturer,
		Model:          req.Model,
		Username:       req.Username,
		Password:       req.Password,
		IdempotencyKey: newIdempotencyKey(),
	})
	if err != nil {
		var rejection *transport.OnboardingRejection
		if errors.As(err, &rejection) {
			return &CameraOnboardingApplyResult{
				Status:       CameraOnboardingBlocked,
				CandidateKey: req.CandidateKey,
				SafeMessage:  rejection.Detail,
			}, nil
		}
		if errors.Is(err, transport.ErrUnauthorized) {
			return &CameraOnboardingApplyResult{
				Status:       CameraOnboardingActionRequired,
				CandidateKey: req.CandidateKey,
				SafeMessage:  "This Edge's credential was rejected by the SaaS. Re-enrollment may be required.",
			}, nil
		}
		return &CameraOnboardingApplyResult{
			Status:       CameraOnboardingActionRequired,
			CandidateKey: req.CandidateKey,
			SafeMessage:  "Could not reach the SaaS to complete onboarding. No local change was made.",
		}, nil
	}

	syncObserved := s.pollLocalCredentialSync(req.CandidateKey, 3*time.Second)

	return &CameraOnboardingApplyResult{
		Status:       CameraOnboardingSuccess,
		OperationID:  result.OperationID,
		CandidateKey: req.CandidateKey,
		SafeMessage:  onboardingSuccessMessage(result.Status, syncObserved),
		SyncObserved: syncObserved,
	}, nil
}

func onboardingSuccessMessage(resultStatus string, syncObserved bool) string {
	verb := "added"
	if resultStatus == "rotated" {
		verb = "updated"
	}
	if syncObserved {
		return "Camera " + verb + " and already synced to this Edge."
	}
	return "Camera " + verb + ". It will sync to this Edge on its next credential sync cycle."
}

// pollLocalCredentialSync briefly polls this Edge's own local encrypted
// credential cache (the same file cameracreds.Provider.Resolve reads at
// runtime) for candidateKey to appear, without blocking the wizard on the
// Syncer's full interval (DefaultSyncInterval is 5 minutes). It never writes
// to the store -- only OpenStore + Snapshot, a read.
func (s *Service) pollLocalCredentialSync(candidateKey string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if s.localCredentialSynced(candidateKey) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *Service) localCredentialSynced(candidateKey string) bool {
	masterKey, err := cameracreds.LoadOrCreateMasterKey(s.DataDir)
	if err != nil {
		return false
	}
	store, err := cameracreds.OpenStore(s.DataDir, masterKey)
	if err != nil {
		return false
	}
	provider := cameracreds.NewProvider(store)
	_, ok := provider.Resolve(candidateKey)
	return ok
}

// CancelCameraOnboarding reverses a SaaS onboarding operation this same
// Edge just created (docs/saas/20-edge-camera-onboarding.md's DELETE
// endpoint) -- e.g. the operator cancels the wizard, or a subsequent local
// step fails. It is safe to call more than once: the SaaS side is
// idempotent.
func (s *Service) CancelCameraOnboarding(ctx context.Context, operationID int64) error {
	client, creds, err := s.saasClientForOnboarding()
	if err != nil {
		return err
	}
	return client.CancelCameraOnboarding(ctx, creds.DeviceID, creds.Credential, operationID)
}
