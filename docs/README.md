# GEO CAM Edge — Documentation Index

**Source of truth order, if a document and the code ever disagree: the code
on `main` wins.** Report the discrepancy instead of silently trusting the
document.

## Architecture

- [`architecture/EDGE_ARCHITECTURE.md`](architecture/EDGE_ARCHITECTURE.md) —
  **start here.** Canonical, current system overview.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — historical engineering decision log
  for the Go core through Hito I (kept for detail, not the entry point).
- [`product/COMMERCIAL_MODES.md`](product/COMMERCIAL_MODES.md) —
  Gateway/Hybrid/Full Edge capability matrix.
- [`product/G1_CAMERA_TARGET_WIRING.md`](product/G1_CAMERA_TARGET_WIRING.md) —
  discovery → credentials → `CameraTarget` wiring, in depth.
- [`product/B3_RETENTION_DESIGN.md`](product/B3_RETENTION_DESIGN.md) —
  retention design and invariants.
- [`REMOTE_CONFIG.md`](REMOTE_CONFIG.md), [`CONTROL_CHANNEL.md`](CONTROL_CHANNEL.md).

## Install

- [`runbooks/EDGE_INSTALL_FROM_SCRATCH.md`](runbooks/EDGE_INSTALL_FROM_SCRATCH.md) —
  clean Linux host → operational Edge.
- [`deployment/`](deployment/) — hardware targets, corporate networking/
  enterprise deployment models.

## Camera

- [`runbooks/CAMERA_FROM_SCRATCH_EDGE.md`](runbooks/CAMERA_FROM_SCRATCH_EDGE.md) —
  connect a new camera end to end, with troubleshooting.

## Full Edge

- [`runbooks/FULL_EDGE_FROM_SCRATCH.md`](runbooks/FULL_EDGE_FROM_SCRATCH.md) —
  provisioning the local Vision Worker, models, `GEOCAM_EDGE_YOLO_DEVICE`.

## Operations

- [`operations/RETENTION.md`](operations/RETENTION.md) — operational
  reference (see `product/B3_RETENTION_DESIGN.md` for full design).
- [`operations/OFFLINE_AND_RECOVERY.md`](operations/OFFLINE_AND_RECOVERY.md) —
  operational reference (see `resilience/RESILIENCE_MATRIX.md` for the full
  failure-mode matrix).
- [`observability/n-resources-queues.md`](observability/n-resources-queues.md).

## Troubleshooting

- See `runbooks/CAMERA_FROM_SCRATCH_EDGE.md` §H for the camera/RTSP/ONVIF
  troubleshooting table.

## Release

- [`runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md`](runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md) —
  full lifecycle: cut a release, update a device, roll back.
- [`RELEASING.md`](RELEASING.md) — release-cutting detail (GitHub Actions side).
- [`security/ota.md`](security/ota.md), [`security/update-trust.md`](security/update-trust.md).

## Physical Validation

- [`product/PHYSICAL_VALIDATION_REGISTER.md`](product/PHYSICAL_VALIDATION_REGISTER.md) —
  what is actually validated on real hardware vs. code-complete/locally
  tested only.

## Historical

- [`PROJECT_STATUS.md`](PROJECT_STATUS.md) — milestone tracker (Hito A–Z);
  Snapshot section reflects current `main`, per-milestone sections are
  historical record and are not rewritten to match later capabilities.
- [`ROADMAP.md`](ROADMAP.md), [`hito-z-bandwidth-decision.md`](hito-z-bandwidth-decision.md),
  [`product/MVP_PROFILES.md`](product/MVP_PROFILES.md),
  [`product/PILOT_5_10_CAMERAS.md`](product/PILOT_5_10_CAMERAS.md),
  [`product/RELEASE_1_0_READINESS.md`](product/RELEASE_1_0_READINESS.md).

## Security

- [`security/`](security/) — threat model, least-privilege audit, device
  lifecycle, edge security baseline, OTA trust, update trust.

## Other

- [`product/HARDWARE_CERTIFICATION.md`](product/HARDWARE_CERTIFICATION.md),
  [`product/SUPPORT_MODEL.md`](product/SUPPORT_MODEL.md),
  [`performance/`](performance/), [`testing/`](testing/),
  [`fulledge/k5-k8.md`](fulledge/k5-k8.md),
  [`remoteconfig/o-runtime-apply.md`](remoteconfig/o-runtime-apply.md).

---

The main `README.md` at the repo root is the general entry point and links
here; it does not duplicate this index.
