# UX-6 — DVR/NVR Multichannel Support

**Status:** `UX6_SOFTWARE_STATUS = COMPLETE` (backend: discovery through
runtime, end to end, with tests; onboarding wizard UI: channel discovery,
selection, credential test, and onboarding, end to end, with tests).
`UX6_PHYSICAL_DVR_NVR = VALIDATED_GATEWAY_PATH` (physical Hikvision NVR
validation completed 2026-10-05: seven RTSP channels online; three selected
H.265 Cloud pipelines decoded and inferred end to end).
`DVR_NVR_UI_PRODUCTION_ENABLED` remains a product/commercial decision rather
than a code gate; this physical run validates the gateway runtime path, not
full appliance/hardware certification. See `G1_CAMERA_TARGET_WIRING.md` §13.

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

## The onboarding wizard UI (`cmd/geocam-edge-ui/frontend`)

The React onboarding wizard consumes the backend contract above with no
changes to the SaaS/backend surface:

- **Discovery** (`CameraDiscovery.tsx`): `DiscoverCameras` already returns
  one `OnboardingCandidate` per channel for a multi-source device (never a
  bare device-level candidate) — the existing flat candidate list therefore
  already shows each DVR/NVR channel as its own row; no regrouping was
  needed. Each row's badge shows `candidateChannelLabel()` (e.g.
  `Channel 2 of 4 (CH2)`) instead of the old blanket "DVR/NVR not
  supported".
- **Selection** (`utils/cameraOnboardingDisplay.ts`): `candidateSelectable()`
  no longer rejects every `multi_source` candidate. It rejects a candidate
  only when ONVIF is unreachable, or when it is `multi_source` with no
  resolved `channel_index` (a bare device-level candidate — never actually
  emitted by `DiscoverCameras`, but the UI stays fail-closed against it
  defensively). An expanded channel candidate (`multi_source: true` +
  `channel_index` set) is selectable exactly like a single-source one.
- **Credential test** (`CameraCredentialsForm.tsx`): unchanged flow, reused
  as-is. The channel's own composite `candidate_key`
  (`candidateKeyForRequest()`) is the only key ever sent — never
  reconstructed or swapped for a sibling channel's — and the header now
  shows the channel label so the operator knows exactly which stream is
  being tested. The backend already filters `GetProfilesAuth` results down
  to that channel's `VideoSourceToken` (see above), so the test result
  (ONVIF/RTSP/profile) can never belong to another channel.
- **Onboarding** (`App.tsx` → `PlanCameraOnboarding`/`ApplyCameraOnboarding`):
  reused exactly as UX-4 built it, keyed by the channel's `candidate_key`.
  No `channel_id` field was added outside `candidate_key` — the backend
  doesn't need one.
- **Result** (`CameraOnboardingResult.tsx`): shows a `cameraLabel`
  (`candidateDisplayName()`, e.g. `NVR-8CH · Channel 2 of 4 (CH2)`) sourced
  from the selected candidate in memory, never re-derived from the apply
  result — so two channels of the same device can never be confused in the
  success screen.
- **Multiple channels**: the operator onboards one channel, then clicks
  "Add another camera" (pre-existing `handleAddAnotherCamera`, unchanged),
  which re-scans and shows the remaining channels as independent
  candidates. No batch/multi-select onboarding was added — not required to
  close UX-6.

Frontend tests (`src/types/cameraOnboarding.test.ts`, pure `node:test`, no
jsdom — this repo's established pattern): single-source selectable,
single-source no-ONVIF disabled, bare multi-source disabled, expanded
channel candidate selectable, channel candidate missing identity disabled,
distinct channel labels/display names for two channels of the same device
(never collapse), and `candidateKeyForRequest` echoing each channel's exact
key unchanged.

**Profile selection remains automatic** per channel (first usable profile),
same simplification as single-source UX-4 — no physical blocker
demonstrated a need for a manual per-channel selector.

## Physical status

`UX6_PHYSICAL_DVR_NVR = NOT_VALIDATED`. No DVR/NVR hardware exists in this
session to validate against. Everything above is real, tested Go/Python
code and passes against real PostgreSQL and the existing test suites — but
it has never been run against a real multi-channel device, and that claim
is not made here.
