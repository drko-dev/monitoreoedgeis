# UX-8 — Installer E2E / Field Acceptance

`UX8_AUTOMATED_MATRIX` = see per-case table below
`UX8_MACOS_PHYSICAL` = PARTIAL (single-camera commissioning path only — see UX-5)
`UX8_LINUX_PHYSICAL` = NOT_VALIDATED
`UX8_WINDOWS_PHYSICAL` = NOT_VALIDATED
`UX8_ARM64_LINUX_PHYSICAL` = NOT_VALIDATED
`UX8_MULTICAMERA_Z3` = BLOCKED_PHYSICAL_RESOURCES (1 real camera available, minimum 5 required — see below)

This document formalizes UX-8 as a milestone. It does not invalidate any
prior evidence: UX-5's real hardware run (macOS arm64 host, TP-Link Tapo
TC70) feeds the matrix rows it actually covers, and nothing else is
inferred from it.

## Evidence classification

Each cell is one of:

- **AUTOMATED** — covered by `go test`/frontend tests, runs in CI, no
  human or hardware involved.
- **LOCAL_REAL** — exercised by hand against a real, running local
  daemon/SaaS on this dev machine, but not a field deployment.
- **PHYSICAL** — exercised against real hardware/network outside the
  dev machine's own loopback (a real camera, a real second host, etc.).
- **NOT_VALIDATED** — not exercised in this pass. Never inferred from a
  related case.

## Matrix

| Case | AUTOMATED | LOCAL_REAL | PHYSICAL | Notes |
|---|---|---|---|---|
| Clean install | — | — | NOT_VALIDATED | No clean-machine/VM install was performed in this session. |
| Enrollment (self-service claim) | PASS (`internal/installer/enrollment_test.go`) | PASS (UX-2 Claim Closure: real HTTP, real Postgres, real `ClaimDevice`, `GET /edge/me` 200) | NOT_VALIDATED | Self-service claim is functional but SaaS-side is not on `main`. |
| Discovery | PASS (`internal/discovery/...` unit + integration tests) | — | PASS (UX-5: real WS-Discovery found the TC70 on the LAN) | |
| Wrong-credential recovery | PASS (`TestTestCameraCredentialsUnreachableWithoutXAddr` and related) | — | PASS (UX-5: wrong password against the real TC70 → `ONVIF_AUTH_FAILED`, RTSP never attempted) | |
| Camera offline/online recovery | — | — | PASS (UX-5: one real RTSP `EOF`/reconnect observed and recovered on its own during the sustained run) | Not a deliberate unplug/replug test — an organic transient. |
| Cloud commissioning | PASS (`processing_mode_test.go`) | PASS | PASS (UX-5 ran with `processing_mode=cloud`) | |
| Hybrid commissioning | PASS (`processing_mode_test.go`) | — | NOT_VALIDATED | No physical run in Hybrid mode. |
| Full Edge commissioning | PASS (`processing_mode_test.go`, hardware-capability checks) | — | NOT_VALIDATED | No physical run with a real local vision worker/model. |
| Reboot persistence | — | — | NOT_VALIDATED | Would require restarting the physical host mid-test. |
| Upgrade | — | — | NOT_VALIDATED | No OTA upgrade was exercised against a running physical Edge in this pass. |
| Rollback | PASS (`ApplyProcessingMode` rollback-on-failure tests; UX-4/UX-5 onboarding rollback tests) | PASS (UX-5: real `DELETE /api/v1/edge/camera-onboarding/{operation_id}` against a stale operation, confirmed the active stream/binding were untouched) | PASS (same UX-5 evidence, against real hardware) | |
| Uninstall/reinstall | — | — | NOT_VALIDATED | Not exercised. |
| No secret leakage | PASS (static checks: `Credentials.Credential` field comment, no password in any exported/logged struct) | PASS (UX-2/UX-4/UX-5 sessions: passwords/keys/tokens never appeared in any command, log, or committed artifact — verified by construction, not just by absence of a grep hit) | PASS (same discipline followed against real hardware in UX-5) | |

## UX8/Z3 — 5–10 camera physical pilot

`Z3_STATUS = BLOCKED_PHYSICAL_RESOURCES` (carried over unchanged from the
dedicated Z3 milestone run). Real discovery on this LAN found exactly **1**
ONVIF camera (the TC70); the pilot's own minimum is 5. This blocks
`UX8_MULTICAMERA_Z3` specifically — it does not block
`UX8_AUTOMATED_MATRIX` or the single-camera rows above, which are backed by
real evidence independent of camera count.

## UX-7 build status feeding this matrix

`UX7_WINDOWS_AMD64_BUILD = PASS`, `UX7_LINUX_AMD64_BUILD = PASS`,
`UX7_LINUX_ARM64_BUILD = PASS`, `UX7_MACOS_ARM64_BUILD = PASS` — all four
real, on GitHub Actions native runners (`docs/product/UX7_CROSS_PLATFORM_RELEASE.md`).
This proves the installer **builds** for all four platforms. It does not
by itself prove a physical/field run on Windows, Linux x86_64, or Linux
ARM64 hardware — building and field-running are different rows in this
matrix, not the same evidence.

## UX-6 DVR/NVR status feeding this matrix

`UX6_SOFTWARE_STATUS = COMPLETE`: discovery, ONVIF profile resolution,
installer onboarding, the daemon's own reconciler/runtime, and the
onboarding wizard UI (channel discovery, selection, credential test,
onboarding, result) all support independent per-channel identity now
(`docs/product/UX6_DVR_NVR_MULTICHANNEL.md`). `UX6_PHYSICAL_DVR_NVR =
NOT_VALIDATED` — no DVR/NVR hardware exists in this session.
`DVR_NVR_UI_PRODUCTION_ENABLED = NO` — the wizard is real and testable in
development, but this is a product/commercial-support statement, not a
capability gap: no physical DVR/NVR has ever driven it.

## What UX-8 does not claim

- No Windows, Linux x86_64, or Linux ARM64 **physical field run** of the
  Wails installer exists yet — UX-7 proved the build, not a field run on
  that hardware, which neither this session nor a GitHub Actions runner
  can provide (a CI runner is not a representative field/customer host).
- No multi-camera capacity, throughput, or stability claim is made (Z3
  blocked).
- No DVR/NVR **physical** field case exists — the software contract and
  onboarding wizard UI are both complete and tested, but no real
  multi-channel device was ever connected to a real Edge in this session.
