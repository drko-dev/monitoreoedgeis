# GEO CAM Edge — 5–10 Camera Pilot Plan (Hito Z3)

## Purpose

This document is an **executable plan** for a real 5–10 camera pilot. It has
**not been executed**. Every field below is `NOT_VALIDATED` until a real run
produces the evidence record defined at the end of this document. This plan
does not duplicate existing benchmark infrastructure: it reuses the
camera-scale/CPU/RAM/network harness already built for Hito X
(`internal/performance`, `internal/soak`) and the failure-lifecycle
simulators already built for Hito W/Y (`internal/rtsp` simulators,
`internal/edgebacklog` fault injection, restart/power-loss/config-corruption
tests). No new benchmark or simulator is introduced here.

## Pre-flight checklist

- [ ] Confirm target `main` commit and build the exact release artifact to
      be piloted (same build/sign/package path as OTA, `docs/security/update-trust.md`).
- [ ] Confirm processing mode for the pilot (`cloud` / `hybrid` / `edge`) and
      that any `edge`-mode hardware requirement (Vision Worker, CPU/GPU) is
      actually present on the pilot host — this repo does not certify that.
- [ ] Confirm network reachability: pilot host → SaaS over HTTPS, pilot host
      → each camera (RTSP TCP 554, ONVIF HTTP/SOAP), and whether WS-Discovery
      multicast is required (same-subnet cameras) or targets are pre-provisioned
      (routed/cross-subnet cameras — see the R5/R7 gap in `MVP_PROFILES.md`).
- [ ] Confirm enrollment token / credential provisioning path for the pilot
      Edge instance (zero-touch seed file or manual `geocam-edge enroll`).
- [ ] Confirm rollback path is available before the pilot starts (previous
      known-good release still installed, per the existing OTA rollback
      mechanism — never a manual hotfix).

## Hardware inventory (fill in with the real pilot hardware)

| Field | Value |
| --- | --- |
| Edge hardware model | *(fill in — do not assume)* |
| CPU | |
| RAM | |
| Storage | |
| OS / kernel | |
| Architecture | `amd64` / `arm64` |
| Network interface(s) | |

## Architecture for this pilot

- Number of cameras: 5–10 (fill in exact count).
- Camera model(s), codec(s), resolution(s), FPS per camera.
- Processing mode.
- Topology: cameras same subnet as Edge, or routed — if routed, confirm
  targets are pre-provisioned (per the documented discovery gap).

## What to observe during the pilot

Reusing existing instrumentation — no new measurement code required:

| Signal | Existing source |
| --- | --- |
| CPU / RAM / goroutines / FDs | `internal/performance` (Hito X instrumentation, MEASURED/DERIVED/UNAVAILABLE semantics) |
| Disk usage | `internal/platform` (statfs on `GEOCAM_DATA_DIR`) |
| Network bytes | `internal/performance` network instrumentation (application payload, no invented HTTP/TLS overhead) |
| Decode FPS | `internal/processing` `/status` `video_pipeline` block |
| Inference FPS | Vision Worker metrics, only if `edge` mode is piloted |
| Queue/backlog | `internal/edgebacklog` `Status()` (pending/quarantined/persist errors) |
| Reconnects / camera errors | `internal/rtsp` `/status` per-camera state (`connecting`/`online`/`degraded`/`auth_failed`/`offline`) |
| Camera health | Same RTSP `/status` surface |
| SaaS health | Heartbeat module `/status` (`last_success_at`, `consecutive_failures`, sanitized error class) |
| OTA | OTA module state, `/readyz` gate |
| Restart / outage / recovery | Same restart/power-loss/config-corruption/offline scenarios already exercised by Hito Y (Y1–Y10) — run them against the real pilot hardware, not a simulator, to close the "physical hardware" gap Y explicitly left open |

## Evidence format

Every pilot run must be recorded using this exact reproducible format. A
field that was not actually run or measured must say `NOT_VALIDATED` — never
a placeholder number.

```text
date
commit
edge hardware
OS
architecture
camera model
camera count
resolution
FPS
codec
processing mode
duration
CPU
RAM
network
decode
inference
drops
reconnects
errors
result
```

No pilot run has produced this record yet. Until one does, every row of a
would-be evidence table is `NOT_VALIDATED`.

## Classification

| Item | Status |
| --- | --- |
| Pre-flight checklist | DOCUMENTED |
| Evidence format | DOCUMENTED |
| Instrumentation to fill the evidence format | IMPLEMENTED / TESTED (reused from Hito X/Y, no duplication) |
| Actual 5–10 camera pilot execution | **NOT_VALIDATED — actual pilot executed: NO** |
| Physical hardware CPU/RAM/network numbers for this pilot | NOT_VALIDATED |
| Real restart/power-loss/outage/recovery on pilot hardware | NOT_VALIDATED (Hito Y validated these against simulators/deterministic fault injection only) |
