# Hito Z — B7+B8 bandwidth decision for Software 1.0

Status: **B7 CLOSED by explicit 1.0 policy decision. B8 CLOSED by explicit no-claim decision.**

This document records product/release policy only. It does not change the transport
implementation and it does not invent a universal customer bandwidth budget.

## B7 — production bandwidth limits

The Edge already implements three independent Cloud transport controls:

- `GEOCAM_CLOUD_MAX_BYTES_PER_SEC`: positive values enable byte-rate limiting; `0` means unlimited.
- `GEOCAM_CLOUD_BURST_BYTES`: positive values set an explicit byte-token burst. `0` means **automatic burst sizing when the byte limiter is active**; it is not a second "unlimited" switch.
- `GEOCAM_CLOUD_MAX_FPS`: positive values enable upload FPS limiting; `0` means unlimited.

The limiter is enforced before upload and the configured/effective transport state is
observable through the existing Cloud status fields.

### Software 1.0 decision

Software 1.0 does **not** ship an invented universal Mbps, bytes/sec or FPS ceiling.
Network capacity, camera count, image complexity, WAN cost and customer policy are
deployment-specific inputs that this repository does not possess.

Therefore the 1.0 contract is:

1. bandwidth limiting capability is implemented and configurable;
2. bytes/sec and FPS remain unlimited when explicitly configured as `0`;
3. when a deployment requires an uplink cap, the operator/deployment profile must set it;
4. no specific production number is implied by the software default.

This explicit decision closes B7 without pretending that one bandwidth value is safe for
every residential/corporate/site topology.

## B8 — Hybrid bandwidth saving

The repository has a controlled synthetic-selectivity benchmark. It deliberately routes
only a fixed subset of frames and verifies the resulting byte accounting. That experiment
does **not** execute the integrated Hybrid motion/ROI candidate decision and does not use a
physical camera.

There is therefore no field or integrated-Hybrid measurement from which to publish a
general bandwidth-saving percentage.

### Software 1.0 decision

Hybrid local gating is a functional capability, but Software 1.0 makes **no quantitative
bandwidth-saving claim**.

A percentage claim may be added only after a reproducible measurement exercises the real
Hybrid candidate path on a stated workload (and, for a field/product claim, representative
camera/network conditions). Any future result must describe the workload and scope; it
must not be generalized beyond the evidence.

The synthetic benchmark remains useful as a transport/accounting sensitivity test, not as
a commercial saving claim.

## What remains unvalidated

- integrated Hybrid bandwidth reduction on representative scenes;
- real-camera bandwidth saving;
- production WAN cost reduction;
- any universal Mbps/FPS recommendation.

Those are measurement/deployment questions, not hidden Software 1.0 blockers.
