package agent

import (
	"sort"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// TargetSkipReason is a safe, non-sensitive diagnostic explaining why a
// discovered device did not produce a rtsp.CameraTarget. It is safe to log
// or expose in a status surface: it never carries a candidate key's
// identifying details beyond what CandidateKey itself already is (the same
// StableIdentity already used throughout discovery/inventory), and never a
// URI, username, or password.
type TargetSkipReason string

const (
	// SkipMultichannelNotSupported means the device has zero or more than
	// one VideoSource. Hito Z G1 is single-source only; DVR/NVR support
	// remains NOT_VALIDATED (see docs/product/G1_CAMERA_TARGET_WIRING.md).
	SkipMultichannelNotSupported TargetSkipReason = "multichannel_not_supported"
	// SkipNoUsableProfile means the device's single VideoSource has no
	// profile with a StreamURI, or none matches the desired role and more
	// than one candidate remains without an unambiguous fallback.
	SkipNoUsableProfile TargetSkipReason = "no_usable_profile"
	// SkipInvalidStreamURI means the selected profile's StreamURI failed
	// rtsp.ParseTarget (unsupported scheme, missing host, ...).
	SkipInvalidStreamURI TargetSkipReason = "invalid_stream_uri"
	// SkipAuthRequiredNoCredential means the device requires authentication
	// and no credential resolved for its candidate key. This is never
	// filled in with a guessed default.
	SkipAuthRequiredNoCredential TargetSkipReason = "auth_required_no_credential"
)

// TargetSkip records one discovered device that did not produce a
// CameraTarget, for safe (non-sensitive) diagnostics/observability only.
type TargetSkip struct {
	CandidateKey string
	Reason       TargetSkipReason
}

// buildCameraTargets is Hito Z G1-B's pure target builder: a deterministic,
// side-effect-free function from an Inventory snapshot plus a credential
// resolver to the RTSP targets rtsp.Manager.SetTargets can safely be given.
//
// CandidateKey is always exactly DiscoveredDevice.StableIdentity — never an
// IP address, never a synthesized identity. Output is sorted by
// CandidateKey so callers (and tests) see deterministic ordering regardless
// of the Inventory's internal map iteration order.
//
// See internal/agent/camera_target_reconciler.go for the thin layer that
// calls this against live Inventory/Provider state, and
// docs/product/G1_CAMERA_TARGET_WIRING.md for the product contract.
func buildCameraTargets(
	devices []discovery.DiscoveredDevice,
	resolve discovery.CredentialResolver,
	desiredRole string,
) ([]rtsp.CameraTarget, []TargetSkip) {
	var targets []rtsp.CameraTarget
	var skips []TargetSkip

	for _, dev := range devices {
		candidateKey := dev.StableIdentity
		if candidateKey == "" {
			// An inventory entry can only lack StableIdentity if something
			// upstream is badly broken; silently skipping it (no
			// diagnostic keyed on an empty string) is safer than
			// fabricating an identity here.
			continue
		}

		var username, password string
		var credOk bool
		if resolve != nil {
			username, password, credOk = resolve(candidateKey)
		}

		if dev.AuthRequired && (!credOk || username == "") {
			// The device rejected anonymous access and no credential
			// resolved: never emit a target that is known in advance to
			// fail authentication, and never guess a default
			// username/password. This must be evaluated before VideoSources
			// checks so an un-enriched auth-required device with 0 sources
			// is correctly reported as auth_required_no_credential rather
			// than multichannel_not_supported. Other cameras still reconcile.
			skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipAuthRequiredNoCredential})
			continue
		}

		// Hito Z G1 is single-source only: DVR/NVR/multichannel is
		// NOT_VALIDATED and deliberately out of scope. Never collapse two
		// channels under one CandidateKey.
		if len(dev.VideoSources) != 1 {
			skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipMultichannelNotSupported})
			continue
		}

		profile, ok := selectProfile(dev.VideoSources[0].Profiles, desiredRole)
		if !ok {
			skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipNoUsableProfile})
			continue
		}

		addr, path, err := rtsp.ParseTarget(profile.StreamURI)
		if err != nil {
			skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipInvalidStreamURI})
			continue
		}

		target := rtsp.CameraTarget{
			CandidateKey: candidateKey,
			Addr:         addr,
			RTSPPath:     path,
			StreamRole:   string(profile.Role),
			Codec:        profile.Codec,
			Width:        profile.Width,
			Height:       profile.Height,
			FPS:          profile.FPS,
			Username:     username,
			Password:     password,
		}

		targets = append(targets, target)
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].CandidateKey < targets[j].CandidateKey })
	sort.Slice(skips, func(i, j int) bool { return skips[i].CandidateKey < skips[j].CandidateKey })

	return targets, skips
}

// selectProfile deterministically picks one usable media profile (a profile
// with a non-empty StreamURI) from a single VideoSource's profile list:
//
//  1. Profiles are first restricted to usable ones and sorted by Token, so
//     selection never depends on slice/map iteration order.
//  2. A profile whose Role matches desiredRole wins outright.
//  3. Otherwise, if exactly one usable profile exists at all, it is used as
//     an explicit fallback (a single-profile device rarely tags Role
//     correctly).
//  4. Otherwise — multiple usable profiles, none matching desiredRole — the
//     choice would be arbitrary, so selectProfile refuses rather than
//     guessing.
func selectProfile(profiles []discovery.MediaProfile, desiredRole string) (discovery.MediaProfile, bool) {
	usable := make([]discovery.MediaProfile, 0, len(profiles))
	for _, p := range profiles {
		if p.StreamURI != "" {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		return discovery.MediaProfile{}, false
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].Token < usable[j].Token })

	for _, p := range usable {
		if string(p.Role) == desiredRole {
			return p, true
		}
	}
	if len(usable) == 1 {
		return usable[0], true
	}
	return discovery.MediaProfile{}, false
}
