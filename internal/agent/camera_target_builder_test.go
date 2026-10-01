package agent

import (
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
)

func singleSourceDevice(stableIdentity string, profiles ...discovery.MediaProfile) discovery.DiscoveredDevice {
	return discovery.DiscoveredDevice{
		StableIdentity: stableIdentity,
		VideoSources: []discovery.VideoSource{
			{SourceToken: "source_0", Profiles: profiles},
		},
	}
}

func TestBuildCameraTargets_SingleSource(t *testing.T) {
	devices := []discovery.DiscoveredDevice{
		singleSourceDevice("epr:cam-1", discovery.MediaProfile{
			Token:     "profile_sub",
			StreamURI: "rtsp://10.0.0.5:554/stream2",
			Role:      discovery.StreamRoleSubStream,
			Codec:     "H264",
			Width:     640,
			Height:    360,
			FPS:       10,
		}),
	}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	got := targets[0]
	if got.CandidateKey != "epr:cam-1" {
		t.Errorf("CandidateKey = %q, want epr:cam-1", got.CandidateKey)
	}
	if got.Addr != "10.0.0.5:554" || got.RTSPPath != "/stream2" {
		t.Errorf("Addr/RTSPPath = %q/%q, want 10.0.0.5:554//stream2", got.Addr, got.RTSPPath)
	}
	if got.StreamRole != "sub" || got.Codec != "H264" || got.Width != 640 || got.Height != 360 || got.FPS != 10 {
		t.Errorf("metadata mismatch: %+v", got)
	}
}

func TestBuildCameraTargets_MainSubSelection(t *testing.T) {
	main := discovery.MediaProfile{Token: "a_main", StreamURI: "rtsp://10.0.0.5:554/stream1", Role: discovery.StreamRoleMainStream}
	sub := discovery.MediaProfile{Token: "b_sub", StreamURI: "rtsp://10.0.0.5:554/stream2", Role: discovery.StreamRoleSubStream}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", main, sub)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].RTSPPath != "/stream2" {
		t.Fatalf("expected the sub profile selected, got %+v", targets)
	}

	targets, skips = buildCameraTargets(devices, nil, "main")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].RTSPPath != "/stream1" {
		t.Fatalf("expected the main profile selected, got %+v", targets)
	}
}

func TestSelectProfilePrefersONVIFURIOverConstructedFallback(t *testing.T) {
	profiles := []discovery.MediaProfile{
		{Token: "hikvision:stream:102", StreamURI: "rtsp://10.0.0.5/ISAPI/Streaming/channels/102", StreamURIOrigin: "hikvision_constructed", Role: discovery.StreamRoleSubStream},
		{Token: "zz-onvif", StreamURI: "rtsp://10.0.0.5/custom/onvif", StreamURIOrigin: "onvif", Role: discovery.StreamRoleSubStream},
	}
	got, ok := selectProfile(profiles, "sub")
	if !ok || got.Token != "zz-onvif" {
		t.Fatalf("selected profile = %+v, ok=%v; want ONVIF profile", got, ok)
	}
}

func TestBuildCameraTargets_SingleProfileFallback(t *testing.T) {
	// Only one usable profile and its Role does not match desiredRole:
	// fall back to it explicitly rather than skipping.
	only := discovery.MediaProfile{Token: "only", StreamURI: "rtsp://10.0.0.5:554/stream1", Role: discovery.StreamRoleUnknown}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", only)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].RTSPPath != "/stream1" {
		t.Fatalf("expected fallback to the single usable profile, got %+v", targets)
	}
}

func TestBuildCameraTargets_AmbiguousProfilesSkipped(t *testing.T) {
	// Two usable profiles, neither matches desiredRole: no unambiguous way
	// to choose, so skip rather than guess.
	a := discovery.MediaProfile{Token: "a", StreamURI: "rtsp://10.0.0.5:554/a", Role: discovery.StreamRoleUnknown}
	b := discovery.MediaProfile{Token: "b", StreamURI: "rtsp://10.0.0.5:554/b", Role: discovery.StreamRoleUnknown}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", a, b)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(targets) != 0 {
		t.Fatalf("expected no targets, got %+v", targets)
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoUsableProfile {
		t.Fatalf("expected one SkipNoUsableProfile, got %+v", skips)
	}
}

func TestBuildCameraTargets_NoUsableProfileSkipped(t *testing.T) {
	empty := discovery.MediaProfile{Token: "empty", StreamURI: "", Role: discovery.StreamRoleSubStream}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", empty)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(targets) != 0 {
		t.Fatalf("expected no targets, got %+v", targets)
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoUsableProfile {
		t.Fatalf("expected SkipNoUsableProfile, got %+v", skips)
	}
}

func TestBuildCameraTargets_QueryStringPreserved(t *testing.T) {
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsp://10.0.0.20:554/stream?channel=1&subtype=0", Role: discovery.StreamRoleSubStream}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", p)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].RTSPPath != "/stream?channel=1&subtype=0" {
		t.Fatalf("expected query string preserved in RTSPPath, got %+v", targets)
	}
}

func TestBuildCameraTargets_InvalidStreamURISkipped(t *testing.T) {
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsps://10.0.0.5:554/stream1", Role: discovery.StreamRoleSubStream}
	devices := []discovery.DiscoveredDevice{singleSourceDevice("epr:cam-1", p)}

	targets, skips := buildCameraTargets(devices, nil, "sub")
	if len(targets) != 0 {
		t.Fatalf("expected no targets for rtsps://, got %+v", targets)
	}
	if len(skips) != 1 || skips[0].Reason != SkipInvalidStreamURI {
		t.Fatalf("expected SkipInvalidStreamURI, got %+v", skips)
	}
}

// TestBuildCameraTargets_MultichannelProducesOneTargetPerChannel is the
// UX-6 replacement for the old Hito Z G1 "multichannel is always skipped"
// behavior: a device with N VideoSources now produces N independent
// targets, each with its own composite CandidateKey
// (discovery.ChannelCandidateKey), never collapsed into the device's
// identity alone.
func TestBuildCameraTargets_MultichannelProducesOneTargetPerChannel(t *testing.T) {
	dev := discovery.DiscoveredDevice{
		StableIdentity: "epr:nvr-1",
		VideoSources: []discovery.VideoSource{
			{SourceToken: "ch1", Profiles: []discovery.MediaProfile{{Token: "a", StreamURI: "rtsp://10.0.0.5:554/1"}}},
			{SourceToken: "ch2", Profiles: []discovery.MediaProfile{{Token: "b", StreamURI: "rtsp://10.0.0.5:554/2"}}},
		},
	}
	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (one per channel), got %+v", targets)
	}
	wantCh1 := discovery.ChannelCandidateKey("epr:nvr-1", "ch1")
	wantCh2 := discovery.ChannelCandidateKey("epr:nvr-1", "ch2")
	// targets are sorted by CandidateKey.
	if targets[0].CandidateKey != wantCh1 || targets[1].CandidateKey != wantCh2 {
		t.Fatalf("expected composite candidate keys %q and %q, got %+v", wantCh1, wantCh2, targets)
	}
	if targets[0].RTSPPath == targets[1].RTSPPath {
		t.Fatalf("expected each channel to resolve its own stream, got identical paths: %+v", targets)
	}
}

func TestBuildCameraTargets_MultichannelUsesChannelCredentials(t *testing.T) {
	deviceKey := "epr:nvr-channel-credentials"
	dev := discovery.DiscoveredDevice{
		StableIdentity: deviceKey, AuthRequired: true,
		VideoSources: []discovery.VideoSource{
			{SourceToken: "ch1", Profiles: []discovery.MediaProfile{{Token: "p1", StreamURI: "rtsp://10.0.0.5/1"}}},
			{SourceToken: "ch2", Profiles: []discovery.MediaProfile{{Token: "p2", StreamURI: "rtsp://10.0.0.5/2"}}},
		},
	}
	resolve := func(key string) (string, string, bool) {
		switch key {
		case discovery.ChannelCandidateKey(deviceKey, "ch1"):
			return "operator-1", "secret-1", true
		case discovery.ChannelCandidateKey(deviceKey, "ch2"):
			return "operator-2", "secret-2", true
		default:
			return "", "", false
		}
	}
	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, resolve, "sub")
	if len(skips) != 0 || len(targets) != 2 {
		t.Fatalf("targets=%+v skips=%+v, want two independently authorized channels", targets, skips)
	}
	if targets[0].Username != "operator-1" || targets[0].Password != "secret-1" || targets[1].Username != "operator-2" || targets[1].Password != "secret-2" {
		t.Fatalf("channel credentials were not kept separate: %+v", targets)
	}
}

// TestBuildCameraTargets_MultichannelOneChannelFailureDoesNotCollapseOthers
// guards the UX-6 requirement that one channel's failure never removes,
// silently degrades, or overwrites another channel's target on the same
// device.
func TestBuildCameraTargets_MultichannelOneChannelFailureDoesNotCollapseOthers(t *testing.T) {
	dev := discovery.DiscoveredDevice{
		StableIdentity: "epr:nvr-2",
		VideoSources: []discovery.VideoSource{
			{SourceToken: "ch1", Profiles: []discovery.MediaProfile{{Token: "a", StreamURI: "rtsp://10.0.0.5:554/1"}}},
			{SourceToken: "ch2", Profiles: nil}, // no usable profile
		},
	}
	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(targets) != 1 || targets[0].CandidateKey != discovery.ChannelCandidateKey("epr:nvr-2", "ch1") {
		t.Fatalf("expected exactly ch1's target to survive, got %+v", targets)
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoUsableProfile || skips[0].CandidateKey != discovery.ChannelCandidateKey("epr:nvr-2", "ch2") {
		t.Fatalf("expected ch2 alone to be skipped with SkipNoUsableProfile, got %+v", skips)
	}
}

func TestBuildCameraTargets_DisabledChannelSkipped(t *testing.T) {
	dev := discovery.DiscoveredDevice{
		StableIdentity: "epr:nvr-disabled",
		VideoSources: []discovery.VideoSource{
			{SourceToken: "ch1", Availability: discovery.ChannelAvailabilityEnabled, Profiles: []discovery.MediaProfile{{Token: "p1", StreamURI: "rtsp://10.0.0.5:554/1"}}},
			{SourceToken: "ch4", Availability: discovery.ChannelAvailabilityDisabled, Profiles: []discovery.MediaProfile{{Token: "p4", StreamURI: "rtsp://10.0.0.5:554/4"}}},
		},
	}

	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(targets) != 1 || targets[0].CandidateKey != discovery.ChannelCandidateKey("epr:nvr-disabled", "ch1") {
		t.Fatalf("expected only enabled channel target, got %+v", targets)
	}
	if len(skips) != 1 || skips[0] != (TargetSkip{
		CandidateKey: discovery.ChannelCandidateKey("epr:nvr-disabled", "ch4"),
		Reason:       SkipChannelDisabled,
	}) {
		t.Fatalf("expected disabled channel skip, got %+v", skips)
	}
}

func TestBuildCameraTargets_ZeroVideoSourcesSkipped(t *testing.T) {
	zero := discovery.DiscoveredDevice{StableIdentity: "epr:zero-1"}
	_, skips := buildCameraTargets([]discovery.DiscoveredDevice{zero}, nil, "sub")
	if len(skips) != 1 || skips[0].Reason != SkipNoVideoSource {
		t.Fatalf("expected SkipNoVideoSource for zero video sources, got %+v", skips)
	}
}

func TestBuildCameraTargets_CredentialResolution(t *testing.T) {
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsp://10.0.0.5:554/stream1", Role: discovery.StreamRoleSubStream}
	dev := singleSourceDevice("epr:cam-1", p)
	dev.AuthRequired = true

	resolve := func(candidateKey string) (string, string, bool) {
		if candidateKey == "epr:cam-1" {
			return "admin", "s3cret", true
		}
		return "", "", false
	}

	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, resolve, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].Username != "admin" || targets[0].Password != "s3cret" {
		t.Fatalf("expected resolved credential on target, got %+v", targets)
	}
}

func TestBuildCameraTargets_AuthRequiredNoCredentialSkipped(t *testing.T) {
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsp://10.0.0.5:554/stream1", Role: discovery.StreamRoleSubStream}
	dev := singleSourceDevice("epr:cam-1", p)
	dev.AuthRequired = true

	noResolve := func(string) (string, string, bool) { return "", "", false }

	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, noResolve, "sub")
	if len(targets) != 0 {
		t.Fatalf("expected no target when auth is required and no credential resolves, got %+v", targets)
	}
	if len(skips) != 1 || skips[0].Reason != SkipAuthRequiredNoCredential {
		t.Fatalf("expected SkipAuthRequiredNoCredential, got %+v", skips)
	}

	// nil resolver behaves the same as one that never resolves.
	targets, skips = buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(targets) != 0 || len(skips) != 1 || skips[0].Reason != SkipAuthRequiredNoCredential {
		t.Fatalf("expected the same skip with a nil resolver, got targets=%+v skips=%+v", targets, skips)
	}
}

func TestBuildCameraTargets_AuthRequiredZeroSourcesNoCredentialSkipped(t *testing.T) {
	// A device that rejected anonymous access and has no VideoSources yet
	// (because authenticated enrichment could not run without credentials)
	// must be reported as SkipAuthRequiredNoCredential, NOT as
	// SkipMultichannelNotSupported.
	dev := discovery.DiscoveredDevice{
		StableIdentity: "epr:uuid:3fa1fe68-b915-4053-a3e1-5ca6e67f02cd",
		AuthRequired:   true,
		VideoSources:   nil,
	}

	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(targets) != 0 {
		t.Fatalf("expected no targets, got %+v", targets)
	}
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(skips))
	}
	if skips[0].Reason != SkipAuthRequiredNoCredential {
		t.Fatalf("skip reason = %q, want %q", skips[0].Reason, SkipAuthRequiredNoCredential)
	}
}

func TestBuildCameraTargets_NoAuthRequiredWorksWithoutCredential(t *testing.T) {
	// A device that never required auth still gets a target even if no
	// credential resolves for it — absence of a credential is not itself a
	// reason to skip when the camera never needed one.
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsp://10.0.0.5:554/stream1", Role: discovery.StreamRoleSubStream}
	dev := singleSourceDevice("epr:cam-1", p)

	targets, skips := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(targets) != 1 || targets[0].Username != "" || targets[0].Password != "" {
		t.Fatalf("expected a credential-less target, got %+v", targets)
	}
}

func TestBuildCameraTargets_NeverHardcodesDefaults(t *testing.T) {
	p := discovery.MediaProfile{Token: "p", StreamURI: "rtsp://10.0.0.5:554/stream1"}
	dev := singleSourceDevice("epr:cam-1", p)

	targets, _ := buildCameraTargets([]discovery.DiscoveredDevice{dev}, nil, "sub")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %+v", targets)
	}
	got := targets[0]
	if got.Username == "admin" || got.Password != "" {
		t.Errorf("target must never carry a guessed default credential, got %+v", got)
	}
	if got.Addr == "10.0.0.5:554" && got.RTSPPath == "/stream1" {
		// This is the profile's own real values, not a hardcoded default —
		// re-derive independently to make sure it came from ParseTarget and
		// not a literal "/stream1"/"554" baked into the builder.
		p2 := discovery.MediaProfile{Token: "p2", StreamURI: "rtsp://192.168.1.9:8554/onvif2"}
		dev2 := singleSourceDevice("epr:cam-2", p2)
		targets2, _ := buildCameraTargets([]discovery.DiscoveredDevice{dev2}, nil, "sub")
		if len(targets2) != 1 || targets2[0].Addr != "192.168.1.9:8554" || targets2[0].RTSPPath != "/onvif2" {
			t.Fatalf("expected addr/path derived from the profile's own StreamURI, got %+v", targets2)
		}
	}
}

func TestBuildCameraTargets_DeterministicOrdering(t *testing.T) {
	pA := discovery.MediaProfile{Token: "a", StreamURI: "rtsp://10.0.0.1:554/a"}
	pB := discovery.MediaProfile{Token: "b", StreamURI: "rtsp://10.0.0.2:554/b"}
	pC := discovery.MediaProfile{Token: "c", StreamURI: "rtsp://10.0.0.3:554/c"}

	devices := []discovery.DiscoveredDevice{
		singleSourceDevice("epr:c", pC),
		singleSourceDevice("epr:a", pA),
		singleSourceDevice("epr:b", pB),
	}

	for i := 0; i < 10; i++ {
		targets, _ := buildCameraTargets(devices, nil, "sub")
		if len(targets) != 3 {
			t.Fatalf("expected 3 targets, got %d", len(targets))
		}
		if targets[0].CandidateKey != "epr:a" || targets[1].CandidateKey != "epr:b" || targets[2].CandidateKey != "epr:c" {
			t.Fatalf("expected deterministic CandidateKey ordering, got %+v", targets)
		}
	}
}
