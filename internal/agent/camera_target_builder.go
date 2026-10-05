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
	// SkipNoVideoSource means the device reported zero VideoSources at
	// all -- never fabricated as a fallback single channel.
	SkipNoVideoSource TargetSkipReason = "no_video_source"
	// SkipNoUsableProfile means a VideoSource has no profile with a
	// StreamURI, or none matches the desired role and more than one
	// candidate remains without an unambiguous fallback.
	SkipNoUsableProfile TargetSkipReason = "no_usable_profile"
	// SkipChannelDisabled means a recorder explicitly reports the channel as
	// disabled. Unknown availability remains eligible because ONVIF does not
	// define a universal enabled-state signal.
	SkipChannelDisabled TargetSkipReason = "channel_disabled"
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

// buildCameraTargets is Hito Z G1-B's pure target builder, extended by
// UX-6 for multi-source (DVR/NVR) devices: a deterministic, side-effect-free
// function from an Inventory snapshot plus a credential resolver to the RTSP
// targets rtsp.Manager.SetTargets can safely be given.
//
// CandidateKey is exactly DiscoveredDevice.StableIdentity for a
// single-VideoSource device (unchanged since Hito Z G1 -- this is the
// majority case and its CandidateKey format never changes). For a device
// with more than one VideoSource, each channel gets its own CandidateKey
// via discovery.ChannelCandidateKey(StableIdentity, SourceToken) and its own
// independent rtsp.CameraTarget -- never an IP address, never collapsed
// into the device's identity alone. Credentials resolve with specific-over-
// inherited precedence: an exact match for the channel's composite
// CandidateKey wins, followed by inheritance from the physical device's
// StableIdentity if no channel-specific credential was issued. Single-source
// cameras resolve directly by their physical key.
// Output is sorted by CandidateKey so callers (and tests) see deterministic
// ordering regardless of the Inventory's internal map iteration order.
//
// See internal/agent/camera_target_reconciler.go for the thin layer that
// calls this against live Inventory/Provider state, and
// docs/product/G1_CAMERA_TARGET_WIRING.md /
// docs/product/UX6_DVR_NVR_MULTICHANNEL.md for the product contract.
func buildCameraTargets(
	devices []discovery.DiscoveredDevice,
	resolve discovery.CredentialResolver,
	desiredRole string,
) ([]rtsp.CameraTarget, []TargetSkip) {
	var targets []rtsp.CameraTarget
	var skips []TargetSkip

	for _, dev := range devices {
		deviceKey := dev.StableIdentity
		if deviceKey == "" {
			// An inventory entry can only lack StableIdentity if something
			// upstream is badly broken; silently skipping it (no
			// diagnostic keyed on an empty string) is safer than
			// fabricating an identity here.
			continue
		}

		if len(dev.VideoSources) == 0 {
			reason := SkipNoVideoSource
			if dev.AuthRequired {
				var username string
				var ok bool
				if resolve != nil {
					username, _, ok = resolve(deviceKey)
				}
				if !ok || username == "" {
					reason = SkipAuthRequiredNoCredential
				}
			}
			skips = append(skips, TargetSkip{CandidateKey: deviceKey, Reason: reason})
			continue
		}

		multiSource := len(dev.VideoSources) > 1
		for _, vs := range dev.VideoSources {
			candidateKey := deviceKey
			if multiSource {
				if vs.SourceToken == "" {
					// Never fabricate a per-channel identity out of an
					// empty token -- skip only this channel, the device's
					// other channels are unaffected.
					skips = append(skips, TargetSkip{CandidateKey: deviceKey, Reason: SkipNoVideoSource})
					continue
				}
				candidateKey = discovery.ChannelCandidateKey(deviceKey, vs.SourceToken)
			}
			if vs.Availability == discovery.ChannelAvailabilityDisabled {
				skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipChannelDisabled})
				continue
			}
			var username, password string
			var credOk bool
			if resolve != nil {
				username, password, credOk = resolve(candidateKey)
			}
			if dev.AuthRequired && (!credOk || username == "") {
				skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipAuthRequiredNoCredential})
				continue
			}

			profile, ok := selectProfile(vs.Profiles, desiredRole)
			if !ok {
				skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipNoUsableProfile})
				continue
			}

			addr, path, err := rtsp.ParseTarget(profile.StreamURI)
			if err != nil {
				skips = append(skips, TargetSkip{CandidateKey: candidateKey, Reason: SkipInvalidStreamURI})
				continue
			}

			targets = append(targets, rtsp.CameraTarget{
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
			})
		}
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
		if p.StreamURI != "" && p.Availability != discovery.ChannelAvailabilityDisabled {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		return discovery.MediaProfile{}, false
	}
	sort.Slice(usable, func(i, j int) bool {
		iONVIF := usable[i].StreamURIOrigin == "onvif"
		jONVIF := usable[j].StreamURIOrigin == "onvif"
		if iONVIF != jONVIF {
			return iONVIF
		}
		return usable[i].Token < usable[j].Token
	})

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
