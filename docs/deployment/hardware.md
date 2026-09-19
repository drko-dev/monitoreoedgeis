# GEO CAM Edge — Residential Appliance Hardware (Hito Q, Q1–Q3)

Scope: minimum/recommended hardware for running the Linux appliance
(`docs/deployment/appliance.md`) on residential/small-site hardware. This is
**not** a commercial hardware catalog and does not certify any specific
board/vendor. Everything below is derived from the code paths that actually
run on the appliance (`internal/platform`, `internal/config/mode.go`,
`internal/fulledge`, `deploy/vision-worker/`, `deploy/appliance/scripts/`)
and from real, already-measured numbers in `docs/performance/`. No
benchmark or number below was invented — where no real measurement exists,
this doc says so instead of guessing.

## Two very different profiles

The appliance runs one Go binary in one of three modes
(`internal/config/mode.go`: `cloud`, `hybrid`, `edge`). Hardware
requirements differ sharply between them:

| | **Cloud / Hybrid gateway** (`cloud`, `hybrid`) | **Full Edge** (`edge`) |
|---|---|---|
| What it does | Pulls RTSP, transcodes/samples via `ffmpeg` subprocess, uploads JPEG frames to SaaS (hybrid also runs a local motion pre-filter, no ML) | All of the above, plus a local Python subprocess (`deploy/vision-worker/`) running YOLO (`ultralytics`) inference on every sampled frame |
| Runtime deps | Go binary (static, no cgo) + `ffmpeg` static binary | Go binary + `ffmpeg` + **Python 3 + `ultralytics`/PyTorch** (`deploy/vision-worker/requirements.txt`) |
| CPU cost | Low — `ffmpeg` decode/sample only | High — CPU-bound ML inference (`--device cpu` by default in `worker.py`; no GPU/NPU backend implemented) |
| Disk cost | Small (Go binary, few MB; no models) | Larger — PyTorch + `ultralytics` package install (hundreds of MB) plus the two YOLO model files themselves |

**Q1 requirement**: pick the profile before sizing hardware. No board class
below is hardware-validated — see the matrix and the explicit "not
established" markers throughout this document.

## What is verified by code vs. real measurement

- **Supported architectures**: `linux/amd64`, `linux/arm64` only
  (`internal/platform.SupportedArchitectures`, `Makefile`'s `build-linux`,
  `deploy/appliance/scripts/build-ffmpeg-static.sh <amd64|arm64>`). Any
  other `GOARCH` is reported as unsupported by `platform.ArchSupported`,
  not rejected at build time.
- **Host telemetry already implemented and reusable** (Hito N,
  `internal/platform`): hostname, OS, kernel, `GOARCH`, CPU count, total
  RAM (`/proc/meminfo`), CPU%, RAM used/available, disk used/available
  per mount, and thermal-zone temperature where exposed by the kernel
  (relevant for Raspberry-class boards, which throttle under sustained
  CPU load). This is exposed on `/health`
  (`internal/health.Reporter.SetPlatformSample`/`formatResources`) and
  used internally by `internal/fulledge.LimitsManager` to gate inference
  concurrency and evidence writes on real disk/RAM pressure
  (`PlatformDiskChecker`, `PlatformMemoryChecker`). **No new detection
  code was needed or added** — this satisfies Q4's "basic identification"
  requirement already.
- **Network cost, Cloud/Hybrid gateway**: `docs/performance/i10-edge-cloud-cost-metrics.md`
  reports **~9.12 Mbps** for one camera at JPEG quality 85, 5 FPS, 640×360.
  This is a **synthetic local benchmark**
  (`TestLocalBandwidthBenchmark`, `internal/cameratest/`), against
  pseudo-random noise frames, one camera, no real network path and no real
  SaaS endpoint involved — an upper-bound-like figure for that specific
  synthetic input, not a real-network/real-SaaS measurement. It does not
  extrapolate to multiple cameras (no multi-camera benchmark exists) and
  should be read as a rough sizing hint for a single-camera uplink, not a
  validated bandwidth requirement.
- **Network cost, Full Edge**: no real measurement exists. Uploads are
  detections/metadata, not continuous frames, so the qualitative
  direction is "much lower than Cloud/Hybrid" — but no number is reported
  here because none was measured. Do not extrapolate one.
- **Inference cost/FPS on ARM64 or amd64**: **not measured**. The vision
  worker (`deploy/vision-worker/backend.py`) documents in its own comments
  that this repo's CI/dev sandbox has no `torch` install and the real
  ARM64/amd64 inference path has never run end to end on real hardware.
  Nothing here should be read as a throughput/FPS claim.
- **Camera ingestion is RTSP-only**: `internal/rtsp/*` implements an RTSP
  client; there is no `v4l2`/`/dev/video*` capture path in the codebase.
  USB is therefore not part of the video-ingest requirement at all.

## Requirements

### Architecture

- `linux/amd64` or `linux/arm64`. Both are first-class (same build recipe,
  same `ffmpeg` static-build script, same appliance scripts — see Hito P).

### RAM

**NOT ESTABLISHED — pending real measurement.** No benchmark or profiling
run in this repo measures actual RAM usage of either profile on real
hardware.

| | Status | Basis |
|---|---|---|
| Cloud/Hybrid gateway | NOT ESTABLISHED | Go binary + `ffmpeg` subprocess only. 512 MB / 1 GB is a **provisional provisioning assumption, not validated** — a plausible floor for OS + systemd + the process, not a measured requirement. Do not present it as a supported minimum. |
| Full Edge | NOT ESTABLISHED | `ultralytics`/PyTorch's own RAM footprint on ARM64/amd64 has not been measured in this repo at all. No provisional number is offered for this profile. |

### Storage

**NOT ESTABLISHED — pending real measurement.** Same caveat as RAM: no
disk-usage benchmark exists in this repo for either profile.

| | Status | Basis |
|---|---|---|
| Cloud/Hybrid gateway | NOT ESTABLISHED | OS + appliance binary/releases (`releases/<version>/`, keeps prior version for rollback) + logs; no local frame/video retention by default. 2 GB / 4 GB is a **provisional provisioning assumption, not validated**, not a supported minimum. |
| Full Edge | NOT ESTABLISHED | Cloud/Hybrid footprint plus Python/PyTorch install + model files. `ultralytics` pulls PyTorch (hundreds of MB installed); exact model file sizes are not pinned in this repo (`deploy/vision-worker/requirements.txt` deliberately leaves exact pins to provisioning, not Git) — no total was measured, so none is provided even provisionally. |

The offline/evidence buffer (`GEOCAM_DATA_DIR`) has **no built-in size
default** (`CloudBufferMaxBytes`/`CloudBufferMaxFrames`, Milestone I6) —
it is operator-configured based on retention needs and available disk, not
a fixed requirement.

### Network

- Ethernet (or equivalent wired-class link) to reach the RTSP camera
  source and the SaaS endpoint — camera ingestion is RTSP-only, there is
  no offline/local-only mode.
- Cloud/Hybrid gateway: the synthetic local benchmark noted earlier in this
  document (~9.12 Mbps, one camera) is a rough sizing hint, not a
  validated bandwidth requirement.
- Wi-Fi is not excluded by the code (it is a plain TCP/HTTP client) but
  was not part of any test in this repo — treat as untested, not
  unsupported.

### GPU / NPU

**Optional, not guaranteed.** `deploy/vision-worker/worker.py` defaults to
`--device cpu` and nothing in this repo implements a GPU/NPU inference
backend (no CUDA/TensorRT/RKNN/Hailo integration exists in
`deploy/vision-worker/backend.py`). Any board sized for Full Edge should be
assumed CPU-only unless/until such a backend is added.

### USB

Not required for video ingestion (RTSP-only, see above). May be used for
generic peripherals/storage at the operator's discretion; nothing in this
repo depends on it.

## Target hardware matrix

Not a commercial catalog — architecture classes only, no brand/model
certified without real evidence (none exists in this repo).

| Class | Examples | Status |
|---|---|---|
| **ARM64** | Raspberry Pi (4/5-class), Orange Pi or generic ARM64 SBC | Candidate target for Cloud/Hybrid gateway; **NOT HARDWARE VALIDATED**. Full Edge: **NOT PERFORMANCE VALIDATED** — CPU-only ML inference on this class of board has not been measured here at all; treat as a research spike, not a supported target, until someone measures it. |
| **AMD64** | Generic x86_64 mini-PC | Candidate target for Cloud/Hybrid gateway and Full Edge; **NOT HARDWARE VALIDATED**. Full Edge: **NOT PERFORMANCE VALIDATED** on this architecture either — no FPS/latency/RAM number exists for it, so no comparative claim vs. ARM64 is made. |

## Summary: what is code vs. documentation in this closure

- **Code**: none added. `internal/platform` (Hito N) and
  `internal/fulledge.LimitsManager` already provide all the hardware
  identification (CPU arch, RAM, disk, temperature) this milestone needs.
  Reused as-is, per Q4's instruction not to duplicate what already works.
- **Documentation**: this file (`docs/deployment/hardware.md`), plus the
  Q1–Q3 status update in `docs/ROADMAP.md`.
