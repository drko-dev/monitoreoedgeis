# UX-4 — Camera IP Onboarding

**Status:** Implemented / Tested Local — **UX4_STATUS = COMPLETE** (physically confirmed end-to-end against a real camera in UX-5; SaaS remains sole credential authority, Installer never calls `cameracreds.Store.Apply` directly, DVR/NVR stays out of scope)
**Branch:** `feature/ux4-camera-ip-onboarding`
**Base:** `feature/ux3-processing-mode` @ `261189e82a5f48b9749ad2e92b609b10ddfc88b1`
**Cross-repo counterpart:** `drko-dev/monitoreoia`,
[`docs/saas/20-edge-camera-onboarding.md`](https://github.com/drko-dev/monitoreoia)
(branch `feature/ux4-camera-onboarding-api`)

## Overview

UX-4 lets an operator add ONE single-source IP camera from the Wails
installer: discover it locally, enter credentials, validate ONVIF + RTSP
against the real device, preview what will be created, and have the SaaS
authoritatively record the camera/binding/credential — the same durable
record an admin's console flow already produces, just triggered by an
authenticated Edge instead of a human in a browser.

DVR/NVR and other multi-channel devices remain explicitly unsupported:
`DVR_NVR_STATUS = NOT_VALIDATED`, and a multi-channel candidate is refused
before any credential is even tested.

## Two preflight fixes (prerequisites, done first)

### Config source of truth

`installer.Service.checkConfigPresent()` checked `DataDir/config.env`, a
path nothing else in this codebase ever wrote to — the real config always
lived at `config.PersistentConfigPath()` (used by `config.Load()` and
UX-3's `ApplyProcessingMode`). Fixed: the default (empty `ConfigFilePath`,
the only value the real app ever passes) now resolves to that same
canonical path. `CONFIG_PATH_SEAM_RESOLVED = YES`, pinned by
`TestConfigPresenceUsesCanonicalPersistentPathByDefault`.

### Daemon status / restart model

Audited `internal/health` (read-only loopback: `/healthz`, `/readyz`,
`/operationalz`, `/status` — no admin/config-write endpoint),
`internal/remoteconfig`'s `RuntimeAdapter` (SaaS-driven hot-swap only, not
reachable from a local process), and `internal/service` (the real,
existing, narrow OS service-manager wrapper: `launchctl`
bootout/bootstrap/kickstart on darwin, plain `systemctl` on linux).

Found a second real bug while auditing this: `checkServiceInstalled()`'s
darwin branch checked label `io.sidom.geocam-edge` at both a LaunchAgents
and a LaunchDaemons path; `internal/service.Command` actually always
manages label `io.geocam.edge` as a **user-scope** LaunchAgent
(`service_darwin.go`'s `macOSLabel`/`macOSServicePaths`, always `gui/<uid>`,
never `/Library/LaunchDaemons`). The old check could never detect a real
install. Fixed, and extended into a narrow facade
(`internal/installer/daemon_control.go`):

- `DetectDaemon(ctx) bool` — cheap `/healthz` liveness probe.
- `GetDaemonStatus(ctx) (*DaemonStatus, error)` — running, service-installed,
  scope (`user`/`system`/none), and whether a restart could actually be
  attempted without elevation this process lacks.
- `RestartDaemon(ctx) (*RestartDaemonResult, error)` — only ever calls the
  real `internal/service.Command("restart", ...)` when `GetDaemonStatus`
  says it needs no elevation (a user-scope LaunchAgent) or this process
  already runs at the privilege a system-scope entry (Linux `systemctl`)
  would require. Otherwise: `ACTION_REQUIRED`, never a silent failed attempt
  or a false success. `RELOAD_AVAILABLE = NO` (no safe channel exists
  anywhere in this codebase); `RESTART_AVAILABLE` is conditional exactly as
  above.

No generic `ExecuteCommand`/shell is exposed. `RestartDaemon` is not wired
into the camera onboarding Apply flow in this milestone (nothing in UX-4
requires an immediate restart — see "Sync convergence" below); it exists as
the resolved preflight for UX-5.

## Camera type supported

Single-source IP camera only. `DVR_NVR_STATUS = NOT_VALIDATED`; a
`discovery.DiscoveredDevice` with `ChannelCount() > 1` is flagged
`multi_source: true` by `DiscoverCameras`/`GetDiscoveredCamera` and refused
by both `TestCameraCredentials` (before any network call to the camera
itself) and `PlanCameraOnboarding` — a stale or hand-crafted frontend
request can never bypass this, the backend refuses it independently at both
layers.

## Discovery and candidate identity

`internal/installer/camera_discovery.go`'s `DiscoverCameras` runs one
bounded `discovery.Engine.RunScan` — the exact same engine
`internal/agent` runs in the daemon, constructed as an independent instance
here (WS-Discovery is passive multicast listening; two independent
listeners don't conflict, and this never touches the daemon's own instance
lock). `CandidateKey` is always `discovery.DiscoveredDevice.StableIdentity`
verbatim (EPR UUID > hardware serial > normalized endpoint, in that
priority — see `internal/discovery/types.go`): the frontend receives it as
an opaque string and only ever echoes it back; it never constructs one
itself. The scan result is cached in-memory
(`Service.discoveredDevices`, mutex-guarded) for the wizard's lifetime —
ephemeral by design, cleared on the next scan or app restart, never
persisted.

## Credential handling

The operator's password lives in React state only for the duration of the
Test/Apply call (`CameraOnboardingRequest.password`, `TestCameraCredentialsRequest.password`)
and is never written to `localStorage`, `sessionStorage`, IndexedDB, a URL,
or a log. `ApplyCameraOnboarding` sends it once, over HTTPS, to the SaaS's
`POST /api/v1/edge/camera-onboarding` (transport-level TLS is whatever
`internal/transport.New` already enforces — no new crypto here); this
installer never writes it to any local file, and specifically **never**
calls `cameracreds.Store.Apply` (see "Why not a local credential store"
below).

## ONVIF and RTSP validation

`TestCameraCredentials` (`internal/installer/camera_discovery.go`) does the
full chain directly against the camera, with the operator-supplied
credentials, using packages already tested elsewhere in this repo —
`internal/discovery/onvif` and `internal/rtsptest` are not duplicated, only
orchestrated:

1. `onvif.Client.GetCapabilitiesAuth` → media service XAddr.
2. `GetProfilesAuth` → media profiles (codec/resolution/fps already
   resolved here — never asked of the operator).
3. `GetStreamUriAuth` → the stream URI for the first profile.
4. `rtsptest.ParseTarget` + `rtsptest.TestDescribe` → a real RTSP DESCRIBE
   against that URI with the same credentials.

Stable error codes (`ONVIF_UNREACHABLE`, `ONVIF_AUTH_FAILED`,
`ONVIF_NO_PROFILES`, `ONVIF_UNSUPPORTED_LAYOUT`, `ONVIF_TIMEOUT`,
`ONVIF_INVALID_RESPONSE`, `RTSP_UNREACHABLE`, `RTSP_AUTH_FAILED`,
`RTSP_TIMEOUT`, `RTSP_NO_VIDEO`, `RTSP_UNSUPPORTED_CODEC`) are what cross
into the frontend — never a raw Go/XML error. **Known, documented
simplification:** the ONVIF client package does not export distinct
sentinel errors per failure class today, so `classifyONVIFError` can
precisely detect only a timeout (via the request context); every other
ONVIF failure (wrong credentials, malformed SOAP, connection refused) is
reported as `ONVIF_AUTH_FAILED`, the far most common real cause once the
device's XAddr is already known reachable from discovery.

`CameraValidationResult` never carries the resolved stream URI to the
frontend (it can legally embed the password on some cameras) — only the
safe `StreamProfile` (codec/width/height/fps).

## CameraTarget

UX-4 does not construct a `internal/rtsp`/`CameraTarget` runtime object
directly, and does not call `rtsp.Manager.SetTargets`. Per
`docs/product/G1_CAMERA_TARGET_WIRING.md`, that object is built by
`internal/agent`'s own event-driven reconciler, from the daemon's own
`cameracreds.Provider` + discovery inventory, entirely inside the running
daemon process — a separate OS process the Wails installer cannot reach
into. Once the SaaS onboarding call succeeds and the daemon's own
`cameracreds.Syncer` (already running, on its normal interval) picks up the
new credential, the reconciler builds the `CameraTarget` and calls
`SetTargets` **on its own**, exactly as it already does for every
Syncer-observed credential change today. UX-4's job stops at making that
credential exist authoritatively; UX-4 does not need to prove detections
work (that is commissioning, UX-5).

## Why not a local credential store (the actual blocker this milestone resolved)

Audited before writing a single line of onboarding code:
`internal/cameracreds.Store.Apply(incoming []Credential)` is the **only**
write path into the local encrypted cache, and it is explicitly documented
and implemented as a full authoritative snapshot: *"a cached entry whose ID
is absent from incoming is removed"*. `Credential.ID` is the SaaS's own
BIGSERIAL row id. Calling `Store.Apply` from this installer with a
locally-fabricated entry would do one of two bad things: omit the
already-cached entries (deleting every other camera on this Edge), or
include them with a fake ID the SaaS has never heard of — which the next
real `Syncer.Sync()` poll would then delete, since it is "absent" from the
SaaS's real snapshot. Neither is honest.

The resolution (implemented cross-repo, see the SaaS doc) is:
`POST /api/v1/edge/camera-onboarding`, device-authenticated exactly like
this Edge's existing credential sync call, lets the SaaS itself create the
authoritative camera/binding/credential/assignment — the same underlying
tables and functions the admin console already uses
(`create_camera`, `link_camera_to_device`, `create_camera_credential`,
`assign_camera_credential_candidate`), just reachable from an authenticated
device instead of a CSRF-protected browser session. `INSTALLER_CALLS_STORE_APPLY = NO`,
enforced by construction (`internal/cameracreds` is not even imported by
`camera_onboarding.go` for writing — only `LoadOrCreateMasterKey`/`OpenStore`/`Snapshot`,
a **read**, for the sync-convergence check below).

## `PlanCameraOnboarding` / `ApplyCameraOnboarding`

`PlanCameraOnboarding` re-runs `TestCameraCredentials` fresh every time and
never mutates anything — no SaaS call, no local file write, no daemon
interaction (`TestPlanCameraOnboardingNeverMutatesConfigOrCredentials`
pins the multi-source-blocker path; the ONVIF/RTSP calls it makes are
themselves read-only network probes of the camera, not of any local state).

`ApplyCameraOnboarding`:

1. Serializes concurrent calls (`Service.onboardMu`, same `TryLock` pattern
   as UX-3's `ApplyProcessingMode` — `TestApplyCameraOnboardingRejectsConcurrentApply`).
2. Re-validates the candidate (calls `PlanCameraOnboarding` again — "the
   candidate still exists / identity matches"). A blocked plan never
   reaches the SaaS at all (`TestApplyCameraOnboardingBlockedNeverCallsSaaS`
   asserts the fake SaaS server it points at is never hit).
3. Resolves this Edge's own enrollment credential
   (`credentials.Load(s.DataDir)`) and `GEOCAM_SAAS_URL`
   (`config.PersistentValue`) — the exact same two facts
   `internal/agent`'s `newCameraCredsModule` uses to build its own Syncer.
   Unenrolled or unconfigured → `ACTION_REQUIRED`, no network call.
4. Calls `transport.Client.OnboardCamera` (new:
   `internal/transport/cameraonboarding.go`, mirrors
   `FetchCameraCredentials`'s existing pattern exactly). A typed SaaS
   rejection (`*transport.OnboardingRejection`, 409/422/404) becomes
   `BLOCKED` with the SaaS's own detail message; `ErrUnauthorized` becomes
   `ACTION_REQUIRED` ("re-enrollment may be required"); any other transport
   failure is `ACTION_REQUIRED` — **no local change was made** either way.
5. On success, briefly polls (see "Sync convergence") and returns `SUCCESS`
   with `sync_observed` set accordingly. Never returns `SUCCESS` for a SaaS
   call that did not itself succeed.

There is no local-state rollback path in `ApplyCameraOnboarding` to write
(step 3-4 never mutate anything locally before the SaaS call, and the SaaS
call is the single point of truth) — the SaaS side owns its own
compensation (see its doc's "Orden de la transacción y compensación").
`CancelCameraOnboarding(operationID)` is exposed for the wizard's own
Cancel action, wrapping the SaaS's `DELETE` endpoint 1:1.

## Sync convergence ("wait for sync")

The daemon's `cameracreds.Syncer` already runs on its own interval
(`DefaultSyncInterval`, 5 minutes) — this installer cannot trigger an
immediate SaaS-side poll from it (that logic lives inside the running
daemon process). `pollLocalCredentialSync` instead does a **short, bounded
(3s), read-only** poll of this Edge's own local encrypted cache
(`cameracreds.OpenStore` + `Snapshot`) for the candidate to appear — cheap,
safe, and honest about what it actually checked: it does not write, does
not call the Syncer, and does not claim success if the poll times out. On
timeout, `ApplyCameraOnboarding` still returns `SUCCESS` (the SaaS
onboarding itself genuinely succeeded and is durable) with `sync_observed: false`
and a message saying the credential will sync on the daemon's own next
cycle — never `SYNC_FAILED` for what is simply "not yet".

## UI

Three screens (`ProcessingMode`'s existing card/badge/check-list CSS
vocabulary is reused, no new design system): **Add camera** (`CameraDiscovery.tsx`,
states `IDLE`/`SCANNING`/`FOUND`/`NONE_FOUND`/`ERROR`, a multi-source or
non-ONVIF candidate is shown but its Select button is disabled) →
**Camera credentials** (`CameraCredentialsForm.tsx`, combines credential
entry, the ONVIF/RTSP check list, and the review summary in one screen — a
deliberate scope reduction from the six separate screens originally
sketched, to fit this milestone's time budget; see "Known gaps") →
**Result** (`CameraOnboardingResultView.tsx`, `SUCCESS`/`ACTION_REQUIRED`/`BLOCKED`/`ROLLED_BACK`,
never claims "Detection operational" — that's UX-5 commissioning). Reopening
the app always resets to the dashboard; the wizard's step and in-progress
form state are plain React state, never persisted, matching UX-3's own
rehydration discipline for its wizard.

## Security

- Password never in `localStorage`/`sessionStorage`/IndexedDB/URL/logs (this
  installer's own logs — the SaaS side's non-leakage is covered by its own
  doc/tests).
- `ApplyCameraOnboarding`'s only outbound secret transmission is the one
  HTTPS call to the SaaS's onboarding endpoint, authenticated with this
  Edge's own enrollment credential — no admin credential is ever used or
  reachable from this code path.
- No generic `ExecuteCommand`/shell exposed by `daemon_control.go` (see
  above).
- Instance lock: `internal/installer` still never imports
  `internal/instance`; discovery and ONVIF/RTSP probing are ordinary
  outbound network calls, not service-manager operations, so they cannot
  collide with it either.
- Processing mode: `PlanCameraOnboarding` only **reads**
  `GetCurrentProcessingMode` (to show it in the plan/review) and never
  writes it — a camera onboarding can never accidentally change UX-3's
  configured mode.

## Tests

**Go** (`internal/installer/camera_onboarding_test.go`, 8 tests;
`internal/installer/daemon_control_test.go`, 8 tests from the preflight
fix; `internal/installer/service_test.go`, +1 test for the config seam):
multi-source rejected before any network call, ONVIF-unreachable-without-XAddr,
Plan never mutates, Apply blocked never touches a fake SaaS server, Apply
concurrency guard, unenrolled Edge rejected before any network call,
idempotency-key uniqueness, success-message text differs by sync-observed
state. Full repo re-run (`go build ./...`, `go vet ./...`, `go test ./...`)
confirms zero regressions across the whole codebase, not just the new
files.

**Known, documented test gap:** no fake-ONVIF-SOAP-server integration test
exercises `TestCameraCredentials`'s full ONVIF chain end to end inside this
package; `internal/discovery/onvif` and `internal/rtsptest` are each
already tested in their own packages, and this package's tests instead
focus on the orchestration/safety-gate logic around them (multi-source
rejection, no-mutation, concurrency, enrollment gating) using directly
seeded candidates. Building a full fake SOAP responder for
capabilities/profiles/stream-uri was judged not worth the time this
milestone had, given the underlying protocol code is already covered
elsewhere.

**Frontend:** `tsc --noEmit`, `vite build`, and the existing `node --test`
suite all pass; no new frontend logic tests were added for the camera
wizard (same constraint noted in UX-3's doc — this repo's JS test runner
cannot execute `.tsx`, and the wizard's logic here is mostly thin
presentational wiring over the Go-tested orchestration above).

## Build verification actually performed

`go build ./...`, `go vet ./...`, `go test ./...` (repo-wide, multiple
times) — all green. `tsc --noEmit`, `npm test`, `vite build` — green.
`git diff --check` — clean. `wails build -platform darwin/arm64` (via
`go run github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`, no `wails` binary
preinstalled) — actually run twice (once after the Go facade landed, once
after the full UI), regenerating `frontend/wailsjs/**` from the real
`App.go`/`installer` types both times; both succeeded and produced
`cmd/geocam-edge-ui/build/bin/geocam-edge-ui.app`.

## Known gaps

1. **UI scope reduction.** The six-screen sketch (separate Discovery,
   Credentials, Validation, Profile selection, Review, Result screens) was
   built as three (Discovery, a combined Credentials+Validation+Review, and
   Result) to fit this milestone's time budget. All the same information is
   shown; the screens are just combined.
2. **ONVIF error classification is coarse** (see "ONVIF and RTSP
   validation" above) — only timeout is detected precisely; every other
   ONVIF failure reports as `ONVIF_AUTH_FAILED`. Refining this needs
   sentinel errors added to `internal/discovery/onvif`, out of this
   milestone's scope.
3. **No fake-ONVIF-server integration test** for the full
   `TestCameraCredentials` chain (see "Tests").
4. **`RestartDaemon` is not wired into this flow.** Nothing in UX-4 needs an
   immediate restart (the daemon's own Syncer converges the credential on
   its own schedule); the facade exists and is tested standalone as a
   resolved UX-5 prerequisite.
5. **Camera removal ("Remove camera")** was not audited or implemented —
   deferred explicitly to UX-5/follow-up per the original brief.
6. **Slot allocation on the SaaS side is a best-effort scan**, not
   race-proof under concurrent onboarding of two cameras in the same
   organization — see the SaaS doc's own "Known gaps".

## UX-5 prerequisites

- Wire `RestartDaemon` into a real recovery flow once a milestone needs an
  Edge-triggered live reload/restart.
- Camera removal / re-commissioning flow.
- Full end-to-end field validation with a real onboarded camera (explicitly
  out of scope here per the original brief: no physical camera, no
  device19/camera9, deterministic tests only).
- Reconcile the SaaS's per-camera `processing_mode` column with this Edge's
  own UX-3 `GEOCAM_PROCESSING_MODE` (noted, not audited, in the SaaS doc).
