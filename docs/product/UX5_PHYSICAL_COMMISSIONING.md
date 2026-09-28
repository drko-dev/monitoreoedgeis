# UX-5 — Physical Camera Commissioning (end-to-end validation)

Status: **PASS** (physical validation performed; see below). Not deployed,
not merged.

## What this milestone validates

That a real IP camera can go through the entire path with no simulation at
any step:

```
Edge Installer (discovery/ONVIF/RTSP) -> SaaS camera onboarding ->
camera<->Edge binding -> credential assignment -> SaaS->Edge authoritative
sync -> cameracreds.Store.Apply (via the daemon's own Syncer, never the
installer) -> daemon reconciler -> CameraTarget -> real RTSP stream consumed
```

UX-5 is validation and defect-fixing, not redesign. It does not reduce or
reopen UX-0..UX-4 scope.

## Hardware and environment

- Camera: TP-Link Tapo TC70, discovered live on the LAN (IP not pinned in
  code or docs — resolved fresh each run via real WS-Discovery/ONVIF, since
  DHCP can change it).
- Edge host: local macOS dev machine, isolated data dir
  (`GEOCAM_DATA_DIR=/tmp/ux5-edge-data`, cleaned up after the session),
  isolated health port (`GEOCAM_HEALTH_ADDR=127.0.0.1:8099`) so it never
  interfered with another already-running Edge process on this host.
- SaaS: local, non-productive native dev instance
  (`http://127.0.0.1:5055`, `scripts/dev-native-start.sh` in `monitoreoia`),
  never production, never the VPS.

## Edge provisioning for this milestone

UX-2's self-service `POST /api/v1/edge/claim` flow exists only on the
unmerged `feature/ux2-edge-claim` SaaS branch, not on `main`. Rather than
cherry-picking unreviewed cross-agent code into this branch, the test Edge
device was provisioned through the real, already-merged **admin** API
(`POST /api/v1/edge/devices`), and the returned `api_key` was persisted
through the real `internal/credentials.Save` / `internal/identity.Load`
code — the same functions `ClaimDevice` itself calls. Authentication was
independently confirmed for real (`GET /api/v1/edge/me` -> 200, correct
`device_kind=edge`, correct organization) before touching the camera flow.

```
EDGE_PROVISIONING_FOR_UX5   = ADMIN_API_REAL
UX2_SELF_SERVICE_CLAIM      = NOT_AVAILABLE_ON_MAIN (exists only on feature/ux2-edge-claim, unmerged)
UX2_SELF_SERVICE_CLAIM_VALIDATED = NO
EDGE_CREDENTIAL_AUTH        = PASS
```

This is a real integration gap for **UX-2's own** self-service enrollment on
`main`, tracked separately — it does not affect the UX-4/UX-5 camera
onboarding contract, which only depends on the Edge already holding a valid
device credential, however it was obtained.

## Defect found and fixed during physical commissioning

**ONVIF media profile parser: audio codec overwrote video codec.**

`internal/discovery/onvif/{soap,wssecurity}.go`'s `GetProfiles`/
`GetProfilesAuth` parsed `<Encoding>`/`<Width>`/`<Height>`/
`<FrameRateLimit>` anywhere under `<Profiles>`, without scoping to
`VideoEncoderConfiguration`. The Tapo TC70's profile also carries an
`AudioEncoderConfiguration` with its own `<Encoding>G711</Encoding>`, which
appears later in document order and silently clobbered the video codec —
width/height/fps stayed correct because audio has none of those fields.

Reproduced against the physical camera: `TestCameraCredentials` reported
`codec: "G711"` for what was actually a `1920x1080` `H264` video profile.

Fixed by tracking whether the parser is currently inside
`VideoEncoderConfiguration` and gating those four fields on that state, in
both the unauthenticated and WS-Security authenticated parsers. Two
regression tests added (`TestGetProfiles_VideoCodecNotOverwrittenByAudioEncoding`,
`TestGetProfilesAuth_VideoCodecNotOverwrittenByAudioEncoding`), each
reproducing the real Video-before-Audio profile shape. Re-ran against the
physical camera after the fix: `codec: "H264"`, confirmed.

Commit: `fix(onvif): stop audio encoder codec from overwriting video codec`.

## Physical results, phase by phase

| Phase | Result |
|---|---|
| A — Preflight | PASS (host facts only, no secrets recorded) |
| B — Discovery (real WS-Discovery/ONVIF) | PASS — TC70 found live on the LAN, single-source, `onvif_available=true` |
| C — ONVIF (real GetCapabilitiesAuth/GetProfilesAuth/GetStreamUriAuth) | PASS — after the codec fix: `H264 1920x1080@15` |
| D — RTSP (real `rtsptest.TestDescribe`) | PASS |
| E — SaaS onboarding (real `POST /api/v1/edge/camera-onboarding`) | PASS — real operation created, camera/binding/credential/assignment resolved |
| F — Idempotency (same `idempotency_key`, real replay) | PASS — identical `operation_id`/`camera_id` on replay; divergent payload with the same key correctly rejected `409` |
| G — Edge convergence (real daemon, own Syncer, own `Store.Apply`) | PASS — `cameracreds: credential cache updated added=1`, then `camera target reconciler: applying targets device_count=1 target_count=1` |
| H — Camera runtime operational | PASS — `camera stream connected and playing` against the real camera; survived one real transient RTSP EOF/reconnect on its own |
| I — Failure/retry | Wrong password -> real `ONVIF_AUTH_FAILED`, RTSP never attempted. Idempotent replay -> PASS. Rollback of a stale (already-rotated) operation via the real `DELETE /api/v1/edge/camera-onboarding/{operation_id}` -> confirmed it did not touch the active operation or interrupt the live stream. |

At no point did the installer call `cameracreds.Store.Apply` directly — the
credential reached the local store exclusively through the daemon's own
already-running Syncer, exactly as designed in UX-4.

## Known gaps (unchanged scope, explicitly not addressed here)

1. `UX2_SELF_SERVICE_CLAIM` is unavailable on `main` (see above) — tracked
   against UX-2, not UX-4/UX-5.
2. A separate, unrelated subsystem (`internal/discovery/module.go`'s gateway
   discovery pull loop) returned `403` against this admin-provisioned test
   device during the session. It does not touch camera onboarding, binding,
   credential assignment, or the reconciler/CameraTarget path validated
   above, all of which passed. Not investigated further here — out of this
   milestone's scope (camera onboarding, not gateway discovery).
3. Profile selection remains automatic (first ONVIF profile resolved) — no
   physical blocker required a manual selector; unchanged from UX-4.
4. `RestartDaemon` was not needed anywhere in this flow: the running
   daemon's own reconciler picked up the new credential and built the
   `CameraTarget` without any restart. It stays out of the happy path, as
   designed in UX-4.

## Explicitly out of scope here (per the milestone's own restrictions)

DVR/NVR onboarding (still `NOT_VALIDATED`/disabled), merging, deploying,
touching production or the VPS, PR #219, issue #18, other agents'
worktrees/branches.
