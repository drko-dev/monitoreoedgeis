# UX-6 — DVR/NVR Multichannel Support

**Status:** `UX6_SOFTWARE_STATUS = PARTIAL` (audited, designed, not implemented)
**Status:** `UX6_PHYSICAL_DVR_NVR = NOT_VALIDATED`
**Status:** `DVR_NVR_UI_ENABLED = NO`

This is a new milestone, not a UX0–UX5 gap. It was audited during Final UX
Closure. The honest result: the foundational data model already respects
`1 IP != 1 camera`, but the onboarding/reconciler/SaaS layers built on top
of it do not yet extend that model to independent per-channel identity —
they fail-closed on any multi-source device instead. Completing that is a
real, multi-file feature (comparable in size to UX4 itself, per channel)
that this closure pass did not implement, to avoid claiming a completion
that wasn't actually built and tested.

## What already exists (audited, not reimplemented)

`internal/discovery/types.go` already models the correct architecture:

```go
// VideoSource represents a physical or logical sensor / channel on a device.
type VideoSource struct {
    SourceToken  string
    Label        string
    Profiles     []MediaProfile
    Capabilities []string
}

// DiscoveredDevice ...
// CRITICAL ARCHITECTURAL RULE: 1 IP != 1 camera. A DiscoveredDevice may host
// multiple VideoSources (e.g. an 8-channel NVR has 8 VideoSources).
```

`DiscoveredDevice.ChannelCount()` already returns `len(VideoSources)` (or 1
as a single-source fallback), and each `VideoSource` already carries its own
stable `SourceToken` and its own `[]MediaProfile`. This is real, existing
groundwork for per-channel identity — it was not invented for this audit.

## What does not exist yet (the real gap)

Everything downstream of discovery collapses back to device-level identity
and explicitly refuses multi-source:

- `internal/installer/camera_discovery.go`: `OnboardingCandidate.MultiSource
  = d.ChannelCount() > 1`, and `TestCameraCredentials` refuses a
  multi-source candidate **before any network call** — this is intentional,
  documented fail-closed behavior (`docs/product/UX4_CAMERA_IP_ONBOARDING.md`),
  not a bug.
- `internal/installer/camera_onboarding.go`'s onboarding request/plan/apply
  types carry one `CandidateKey` per call — there is no channel dimension.
- The SaaS onboarding contract (`monitoreoia`'s
  `POST /api/v1/edge/camera-onboarding`) binds one `edge_device` to one
  camera via `edge_device_cameras` — there is no
  `edge_device_channels`/`dvr_channel_id` concept in the schema.
- `internal/agent/camera_target_reconciler.go` /
  `camera_target_builder.go` build one `CameraTarget` per credential — no
  independent runtime target keyed by `(device, channel)`.

## Proposed design (documented, not built)

1. **Edge candidate model:** extend `OnboardingCandidate` with an optional
   `Channels []ChannelCandidate` (token, label, profile summary) when
   `ChannelCount() > 1`, instead of refusing outright. The wizard would let
   the operator pick one or more channels to onboard, each becoming its own
   onboarding request keyed by `(candidate_key, source_token)`.
2. **SaaS contract:** add a `channel_token` column to
   `edge_device_cameras`/the onboarding operation, nullable for
   single-source (backward compatible), non-null and unique per
   `(device_id, channel_token)` for DVR/NVR. `camera_credential_assignments`
   already keys off a resolved camera row, so no change needed there beyond
   ensuring one camera row per channel.
3. **Reconciler:** `CameraTarget` keyed by `(device_id, channel_token)`
   instead of `device_id` alone; independent supervisor/pipeline state per
   channel (already how multiple *distinct* single-source cameras work
   today — this generalizes the existing per-device-credential loop rather
   than introducing a new mechanism).
4. **Cloud/Hybrid/Full Edge:** no new behavior needed per mode — each
   channel is just another `CameraTarget` with its own processing-mode
   inheritance from the Edge's existing config, exactly as single-source
   cameras already work.

This design deliberately reuses existing primitives (`VideoSource`,
`CameraTarget`, `camera_credential_assignments`) rather than introducing a
parallel system, mirroring the pattern UX-2 Claim Closure used for
enrollment codes.

## Why this stays `NOT_VALIDATED` / disabled

- No DVR/NVR hardware is available to this session (the only physical
  camera on hand, UX-5's TP-Link Tapo TC70, is single-source).
- Implementing points 1–4 above touches the SaaS schema, the onboarding
  contract, the reconciler, and both UIs — a real multi-file, multi-repo
  change that needs its own design review and test suite, not a
  same-session addition bolted onto an audit pass.
- Per this milestone's own instructions: fail-closed today is correct;
  `DVR_NVR_ENABLED` in the GUI must stay `NO` until physical PASS, and no
  physical PASS is possible without hardware.

## Tests

No new DVR/NVR fixtures were added in this pass, for the same reason: a
fixture suite for a not-yet-built contract would test against invented
assumptions rather than a real API. The existing regression coverage that
*proves* the fail-closed behavior is real and already passes:
`TestTestCameraCredentialsRejectsMultiSourceBeforeAnyNetworkCall` in
`internal/installer/camera_onboarding_test.go`.
