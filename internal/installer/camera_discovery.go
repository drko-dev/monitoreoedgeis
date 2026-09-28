package installer

import (
	"context"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// OnboardingCandidate is the safe, opaque-keyed projection of a
// discovery.DiscoveredDevice the frontend can see: never a raw internal
// struct, never a password, never something React could use to fabricate
// its own candidate identity. CandidateKey is discovery.DiscoveredDevice's
// own StableIdentity -- React only ever echoes it back, never constructs it.
type OnboardingCandidate struct {
	CandidateKey   string `json:"candidate_key"`
	Host           string `json:"host"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	Model          string `json:"model,omitempty"`
	ONVIFAvailable bool   `json:"onvif_available"`
	AuthRequired   bool   `json:"auth_required"`
	// MultiSource is true for a DVR/NVR/multi-sensor device (ChannelCount() >
	// 1). UX-4 supports single-source cameras only: the frontend must treat
	// a MultiSource candidate as disabled/not selectable, and
	// TestCameraCredentials/PlanCameraOnboarding both refuse it too, so a
	// stale frontend can never bypass this.
	MultiSource bool `json:"multi_source"`
}

func newOnboardingCandidate(d discovery.DiscoveredDevice) OnboardingCandidate {
	return OnboardingCandidate{
		CandidateKey:   d.StableIdentity,
		Host:           d.IP,
		Manufacturer:   d.Manufacturer,
		Model:          d.Model,
		ONVIFAvailable: d.XAddr != "",
		AuthRequired:   d.AuthRequired,
		MultiSource:    d.ChannelCount() > 1,
	}
}

// DiscoverCamerasResult is the outcome of one discovery scan.
type DiscoverCamerasResult struct {
	Candidates []OnboardingCandidate `json:"candidates"`
	DurationMS int64                 `json:"duration_ms"`
}

// DiscoverCameras runs one bounded WS-Discovery + unauthenticated ONVIF
// inspection scan (discovery.Engine, the same engine internal/agent runs in
// the daemon) and caches its result for the duration of the onboarding
// wizard. Running this from the Wails installer does not conflict with a
// live daemon: WS-Discovery is passive multicast listening, and this engine
// is its own independent instance, never the daemon's.
func (s *Service) DiscoverCameras(ctx context.Context) (*DiscoverCamerasResult, error) {
	engine := discovery.NewEngine(nil, nil, nil, nil, 0, nil)
	scan, err := engine.RunScan(ctx)
	if err != nil {
		return nil, &SafeError{
			Code:        "DISCOVERY_FAILED",
			SafeMessage: "Camera discovery scan failed.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	s.discoveryMu.Lock()
	s.discoveredDevices = make(map[string]discovery.DiscoveredDevice, len(scan.DevicesFound))
	for _, d := range scan.DevicesFound {
		s.discoveredDevices[d.StableIdentity] = d
	}
	s.discoveryMu.Unlock()

	result := &DiscoverCamerasResult{
		Candidates: make([]OnboardingCandidate, 0, len(scan.DevicesFound)),
		DurationMS: scan.Duration.Milliseconds(),
	}
	for _, d := range scan.DevicesFound {
		result.Candidates = append(result.Candidates, newOnboardingCandidate(d))
	}
	return result, nil
}

// GetDiscoveredCamera returns one previously discovered candidate by its
// opaque CandidateKey. It never re-scans: a candidate that has aged out (the
// operator waited too long, or ran a new scan) is reported as not found
// rather than silently returning stale data.
func (s *Service) GetDiscoveredCamera(ctx context.Context, candidateKey string) (*OnboardingCandidate, error) {
	s.discoveryMu.Lock()
	d, ok := s.discoveredDevices[candidateKey]
	s.discoveryMu.Unlock()
	if !ok {
		return nil, &SafeError{
			Code:        "CANDIDATE_NOT_FOUND",
			SafeMessage: "This camera is no longer known. Run discovery again.",
			Recoverable: true,
		}
	}
	candidate := newOnboardingCandidate(d)
	return &candidate, nil
}

// TestCameraCredentialsRequest carries the operator-entered secret only for
// the duration of this one validation call -- it is never persisted by this
// installer (see docs/product/UX4_CAMERA_IP_ONBOARDING.md, "Credential
// handling").
type TestCameraCredentialsRequest struct {
	CandidateKey string `json:"candidate_key"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// StreamProfile is the safe, non-secret shape of a resolved ONVIF media
// profile: never the stream URI (which could embed the password on some
// cameras), only what the review screen needs to show.
type StreamProfile struct {
	Codec  string  `json:"codec,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	FPS    float64 `json:"fps,omitempty"`
}

// CameraValidationResult is always returned, never an error, for a
// validation that simply failed: ONVIFStatus/RTSPStatus carry the specific,
// UI-safe reason code (never a raw Go/XML error) per
// docs/product/UX4_CAMERA_IP_ONBOARDING.md's error taxonomy.
type CameraValidationResult struct {
	CandidateKey string         `json:"candidate_key"`
	MultiSource  bool           `json:"multi_source"`
	ONVIFStatus  string         `json:"onvif_status"`
	ONVIFReason  string         `json:"onvif_reason,omitempty"`
	RTSPStatus   string         `json:"rtsp_status"`
	RTSPReason   string         `json:"rtsp_reason,omitempty"`
	Profile      *StreamProfile `json:"profile,omitempty"`
	// StreamURI is intentionally absent from this exported type: it can
	// legally embed the password (rtsp://user:pass@host/...), and this
	// struct crosses into the frontend. ApplyCameraOnboarding re-resolves it
	// itself from the same profile token, out of the UI's reach.
	Passed bool `json:"passed"`
}

const onvifRequestTimeout = 5 * time.Second

// TestCameraCredentials validates a candidate end to end: authenticated
// ONVIF (device capabilities -> media profiles -> stream URI) followed by a
// real RTSP DESCRIBE against the resolved URI (internal/rtsptest, the same
// package internal/cameratest already uses). A DVR/NVR candidate
// (MultiSource) is refused before any network call -- UX-4 does not support
// multi-channel devices.
func (s *Service) TestCameraCredentials(ctx context.Context, req TestCameraCredentialsRequest) (*CameraValidationResult, error) {
	s.discoveryMu.Lock()
	device, ok := s.discoveredDevices[req.CandidateKey]
	s.discoveryMu.Unlock()
	if !ok {
		return nil, &SafeError{
			Code:        "CANDIDATE_NOT_FOUND",
			SafeMessage: "This camera is no longer known. Run discovery again.",
			Recoverable: true,
		}
	}

	result := &CameraValidationResult{CandidateKey: req.CandidateKey, MultiSource: device.ChannelCount() > 1}
	if result.MultiSource {
		result.ONVIFStatus = "ONVIF_UNSUPPORTED_LAYOUT"
		result.ONVIFReason = "DVR/NVR and other multi-channel devices are not supported yet."
		return result, nil
	}
	if device.XAddr == "" {
		result.ONVIFStatus = "ONVIF_UNREACHABLE"
		result.ONVIFReason = "No ONVIF service address was found for this device."
		return result, nil
	}

	validateCtx, cancel := context.WithTimeout(ctx, onvifRequestTimeout)
	defer cancel()

	client := onvif.NewClient(onvifRequestTimeout, nil)
	client.SetXAddrValidator(discovery.ValidateXAddr)

	mediaXAddr, err := client.GetCapabilitiesAuth(validateCtx, device.XAddr, req.Username, req.Password)
	if err != nil {
		result.ONVIFStatus, result.ONVIFReason = classifyONVIFError(validateCtx, err)
		return result, nil
	}
	if mediaXAddr == "" {
		mediaXAddr = device.XAddr
	}

	profiles, err := client.GetProfilesAuth(validateCtx, mediaXAddr, req.Username, req.Password)
	if err != nil {
		result.ONVIFStatus, result.ONVIFReason = classifyONVIFError(validateCtx, err)
		return result, nil
	}
	if len(profiles) == 0 {
		result.ONVIFStatus = "ONVIF_NO_PROFILES"
		result.ONVIFReason = "The camera exposed no usable media profile."
		return result, nil
	}
	profile := profiles[0]

	streamURI, err := client.GetStreamUriAuth(validateCtx, mediaXAddr, profile.Token, req.Username, req.Password)
	if err != nil {
		result.ONVIFStatus, result.ONVIFReason = classifyONVIFError(validateCtx, err)
		return result, nil
	}
	result.ONVIFStatus = "ok"
	result.Profile = &StreamProfile{Codec: profile.Codec, Width: profile.Width, Height: profile.Height, FPS: profile.FPS}

	addr, path, err := rtsptest.ParseTarget(streamURI)
	if err != nil {
		result.RTSPStatus = "RTSP_UNREACHABLE"
		result.RTSPReason = "The resolved stream URI could not be parsed."
		return result, nil
	}
	probe := rtsptest.TestDescribe(ctx, addr, path, req.Username, req.Password, onvifRequestTimeout)
	result.RTSPStatus, result.RTSPReason = translateRTSPResult(probe)
	result.Passed = result.ONVIFStatus == "ok" && result.RTSPStatus == "ok"
	return result, nil
}

// classifyONVIFError maps a raw ONVIF client error to one of the stable,
// UI-safe codes from docs/product/UX4_CAMERA_IP_ONBOARDING.md. The ONVIF
// client package does not export distinct sentinel errors per failure class
// today, so this is a best-effort classification (timeout is checked
// precisely via the context; every other failure -- wrong credentials,
// malformed SOAP, connection refused -- is reported as ONVIF_AUTH_FAILED,
// the far most common real cause once XAddr itself is known reachable).
// Known, documented simplification -- see "Known gaps".
func classifyONVIFError(ctx context.Context, err error) (status, reason string) {
	if ctx.Err() != nil {
		return "ONVIF_TIMEOUT", "The camera did not respond in time."
	}
	return "ONVIF_AUTH_FAILED", "The camera rejected the supplied credentials or returned an invalid response."
}

// translateRTSPResult maps rtsptest.Result to the stable RTSP_* codes.
func translateRTSPResult(probe rtsptest.Result) (status, reason string) {
	switch probe.State {
	case rtsptest.StateValid:
		return "ok", ""
	case rtsptest.StateInvalid:
		return "RTSP_AUTH_FAILED", "The camera rejected the supplied RTSP credentials."
	case rtsptest.StateUnreachable:
		return "RTSP_UNREACHABLE", "Could not connect to the camera's RTSP port."
	default:
		if probe.Err != nil {
			return "RTSP_TIMEOUT", "The camera did not respond to the RTSP request in time."
		}
		return "RTSP_UNREACHABLE", "Could not validate the RTSP stream."
	}
}
