# UX-6 — DVR/NVR Multichannel Support

**Status:** `UX6_SOFTWARE_STATUS = COMPLETE` (backend: discovery through
runtime, end to end, with tests). `UX6_PHYSICAL_DVR_NVR = NOT_VALIDATED` (no
DVR/NVR hardware available to this session). `DVR_NVR_UI_PRODUCTION_ENABLED
= NO` (no wizard UI was built in this pass — see "What is not built" below).

## Channel identity model

```
DiscoveredDevice (StableIdentity)
  -> VideoSource (SourceToken)      -- one per physical/logical channel
    -> MediaProfile (VideoSourceToken links a profile back to its channel)
      -> logical camera identity: discovery.ChannelCandidateKey(StableIdentity, SourceToken)
```

`discovery.ChannelCandidateKey(deviceStableIdentity, sourceToken string) string`
and its inverse `discovery.ParseChannelSourceToken` are the one composite
candidate-key format every layer shares:
`"<StableIdentity>|ch=<SourceToken>"`. A single-source camera's
`CandidateKey` is untouched — exactly `StableIdentity`, same as before UX-6.
Nothing was renamed or restructured for the already-working single-source
path.

## What changed, file by file

**`internal/discovery/onvif/{soap,wssecurity}.go`** — `MediaProfile` gained
a `VideoSourceToken` field, parsed from
`VideoSourceConfiguration/SourceToken` (never previously extracted). Scoped
the same way UX-5's codec-leak fix scoped `VideoEncoderConfiguration`, so a
channel's profile can never be attributed to a different channel.

**`internal/installer/camera_discovery.go`** — a multi-source device is no
longer rejected outright:
- `DiscoverCameras` expands it into one `OnboardingCandidate` per channel
  (own `CandidateKey`, `ChannelLabel`, `ChannelIndex`/`ChannelCount` for
  display), and indexes the discovery cache by each channel's composite key.
- `TestCameraCredentials` resolves which channel a composite `CandidateKey`
  refers to and filters the authenticated `GetProfilesAuth` response down to
  that channel's own profile (by `VideoSourceToken`) before validating
  ONVIF/RTSP — it no longer just takes `profiles[0]` of the whole device.
- `PlanCameraOnboarding`/`ApplyCameraOnboarding` needed **no changes**: they
  already treat `CandidateKey` as an opaque string end to end.

**`internal/agent/camera_target_builder.go`** — `buildCameraTargets` (the
daemon's own reconciler, independent of the installer) now produces one
independent `rtsp.CameraTarget` per `VideoSource` for a multi-source device,
each with its own composite `CandidateKey`. The device's credential is
resolved once (by `StableIdentity`) and shared across its channels — a
DVR/NVR authenticates once for the whole unit, exactly as
`docs/product/PILOT_5_10_CAMERAS.md`-adjacent architecture assumed. One
channel's missing profile or invalid stream URI skips only that channel;
the device's other channels are unaffected (own test:
`TestBuildCameraTargets_MultichannelOneChannelFailureDoesNotCollapseOthers`).

## Why the SaaS side needed almost no changes

Audited before writing anything: `candidate_key` /
`edge_device_cameras.edge_camera_identifier` were already opaque strings
end to end — `resolve_camera_id_by_identifier`, `onboard_edge_camera`,
`assign_camera_credential_candidate`, `camera_credential_assignments`, the
`UNIQUE(device_id, edge_camera_identifier)` constraint (migration 003, since
before UX-4 existed) — none of them parse or interpret the string's shape.
Two different composite channel keys for the same DVR/NVR device therefore
already can never collapse into the same `camera_id`, with zero contract
changes, because the uniqueness constraint operates on the string value the
Edge already controls the shape of.

The one real, schema-level gap found: `VARCHAR(64)` comfortably fits a
single-source `StableIdentity` (~44 chars) but can be too narrow for
`StableIdentity + "|ch=" + SourceToken` on vendors with long source tokens.
Migration `083_widen_candidate_key_for_dvr_channels.sql` widens the 5
affected columns (`edge_device_cameras.edge_camera_identifier`,
`gateway_discovery_candidates.candidate_key`,
`camera_credential_assignments.candidate_key`,
`edge_camera_status.candidate_key`,
`edge_camera_onboarding_operations.candidate_key`) to `VARCHAR(160)`, and
the 4 matching Pydantic `max_length` fields were bumped to match. No new
table, no onboarding-endpoint contract change, no reconciler-equivalent
change on the SaaS side.

## Tests

**Edge, real (all passing):**
- `TestGetProfilesAuth_FourChannelDVRSourceTokensDistinct` — the 4-channel
  DVR ONVIF fixture this milestone required: 4 `Profiles` entries, distinct
  `VideoSourceConfiguration/SourceToken` per channel, verified none leak
  onto another (`internal/discovery/onvif`).
- `TestBuildCameraTargets_MultichannelProducesOneTargetPerChannel`,
  `TestBuildCameraTargets_MultichannelOneChannelFailureDoesNotCollapseOthers`,
  `TestBuildCameraTargets_ZeroVideoSourcesSkipped` — the reconciler's
  per-channel target construction and failure isolation
  (`internal/agent`).
- `TestNewChannelOnboardingCandidates_OneCandidatePerChannel`,
  `TestNewChannelOnboardingCandidates_EmptySourceTokenSkipped`,
  `TestGetDiscoveredCamera_ResolvesSpecificChannel` — the installer's
  channel expansion and per-channel lookup (`internal/installer`).
- Every pre-existing single-source test in all three packages still passes
  unchanged, including `TestTestCameraCredentialsRejectsMultiSourceBeforeAnyNetworkCall`
  (still correctly rejects a bare device-level `CandidateKey` for a
  multi-source device with no network call — only a channel's own composite
  key is now a valid candidate).

**Not duplicated, and why:** idempotency (same channel replay / different
channel ≠ replay), cross-device isolation, and cross-tenant isolation for a
channel candidate are direct, mechanical consequences of the SaaS's
existing, already-tested `idempotency_key` + `payload_hash` +
`UNIQUE(device_id, edge_camera_identifier)` machinery (`test_camera_credentials.py`,
`test_edge_camera_onboarding.py`) — a channel candidate is just a
`candidate_key` string, and that machinery's guarantees don't depend on
what shape the string has. Writing a parallel test suite asserting the same
mechanism again under a different string format would test the string
formatter, not new behavior. Adversarial cases specific to the *new*
surface (same channel token claimed by two different devices, missing
channel token, duplicate source token on one device) are covered by
`TestNewChannelOnboardingCandidates_EmptySourceTokenSkipped` (empty/missing
token) and the `UNIQUE(device_id, edge_camera_identifier)` constraint
itself (same token on another device cannot collide, since the constraint
is scoped per device already).

## What is not built (explicitly out of scope for this pass)

- **The DVR/NVR onboarding wizard UI** (React): channel enumeration,
  multi-select, per-channel credential test screen. The backend contract
  this UI would call is complete and tested; the screens themselves were
  not built. `DVR_NVR_UI_PRODUCTION_ENABLED` stays `NO` regardless — no UI
  means nothing to gate.
- **Profile selection remains automatic** per channel (first usable
  profile), same simplification as single-source UX-4 — no physical
  blocker demonstrated a need for a manual per-channel selector.

## Physical status

`UX6_PHYSICAL_DVR_NVR = NOT_VALIDATED`. No DVR/NVR hardware exists in this
session to validate against. Everything above is real, tested Go/Python
code and passes against real PostgreSQL and the existing test suites — but
it has never been run against a real multi-channel device, and that claim
is not made here.
