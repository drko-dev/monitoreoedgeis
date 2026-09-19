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

**Q1 requirement**: pick the profile before sizing hardware. A
Raspberry-class board is a realistic **Cloud/Hybrid gateway**; it is
marginal-to-unrealistic for **Full Edge**, per the evidence below.

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
- **Network cost, Cloud/Hybrid gateway**: real, measured (not invented) —
  `docs/performance/i10-edge-cloud-cost-metrics.md`: **~9.12 Mbps
  worst-case** per camera stream at JPEG quality 85, 5 FPS, 640×360
  (uplink to SaaS). This is the number to size a home/small-site uplink
  against for one camera in Cloud/Hybrid mode; per-camera cost drops with
  scene compressibility (documented in the same file).
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

| | Minimum | Recommended | Basis |
|---|---|---|---|
| Cloud/Hybrid gateway | 512 MB | 1 GB | Go binary + `ffmpeg` subprocess only; no measured ceiling exists, this is a conservative floor for OS + systemd + the process, not a benchmarked number |
| Full Edge | **Not established** | **Not established** | `ultralytics`/PyTorch's own RAM footprint on ARM64 has not been measured in this repo. Do not deploy Full Edge to a fixed-RAM board without measuring first. |

### Storage

| | Minimum | Recommended | Basis |
|---|---|---|---|
| Cloud/Hybrid gateway | 2 GB free | 4 GB free | OS + appliance binary/releases (`releases/<version>/`, keeps prior version for rollback) + logs; no local frame/video retention by default |
| Full Edge | Cloud/Hybrid + **Python/PyTorch install + model files** | Same + headroom for retention | `ultralytics` pulls PyTorch (hundreds of MB installed); exact model file sizes are not pinned in this repo (`deploy/vision-worker/requirements.txt` deliberately leaves exact pins to provisioning, not Git) |

The offline/evidence buffer (`GEOCAM_DATA_DIR`) has **no built-in size
default** (`CloudBufferMaxBytes`/`CloudBufferMaxFrames`, Milestone I6) —
it is operator-configured based on retention needs and available disk, not
a fixed requirement.

### Network

- Ethernet (or equivalent wired-class link) to reach the RTSP camera
  source and the SaaS endpoint — camera ingestion is RTSP-only, there is
  no offline/local-only mode.
- Cloud/Hybrid gateway: size the uplink against the measured **~9.12 Mbps
  worst-case per camera** above.
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

| Class | Examples | Realistic for |
|---|---|---|
| **ARM64** | Raspberry Pi (4/5-class), Orange Pi or generic ARM64 SBC | Cloud/Hybrid gateway. Full Edge: **unverified** — CPU-only ML inference on this class of board has not been measured here; treat as a research spike, not a supported target, until someone measures it. |
| **AMD64** | Generic x86_64 mini-PC | Cloud/Hybrid gateway: comfortable. Full Edge: more headroom than ARM64 SBCs for the PyTorch/`ultralytics` footprint, but still **not measured** in this repo — no FPS/latency number exists for either architecture. |

## Summary: what is code vs. documentation in this closure

- **Code**: none added. `internal/platform` (Hito N) and
  `internal/fulledge.LimitsManager` already provide all the hardware
  identification (CPU arch, RAM, disk, temperature) this milestone needs.
  Reused as-is, per Q4's instruction not to duplicate what already works.
- **Documentation**: this file (`docs/deployment/hardware.md`), plus the
  Q1–Q3 status update in `docs/ROADMAP.md`.
