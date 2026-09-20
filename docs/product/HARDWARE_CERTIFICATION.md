# GEO CAM Edge — Hardware Certification (Hito Z7)

## Scope of this document

Z7 does not certify any hardware, because **this repository cannot certify
hardware by itself**. What it can do — and what this document does — is define
what "certified" will mean, what the code can already measure without new work,
the exact test protocol to run on a physical unit, and the record format that
makes a run auditable. The candidate hardware classes themselves are already
defined in [docs/deployment/hardware.md](../deployment/hardware.md) (Hito Q) and
are **not** restated or upgraded here.

This document is a companion to `docs/product/PILOT_5_10_CAMERAS.md` (Z3, the
5–10 camera pilot plan) and to the commercial capability matrix in
`docs/product/COMMERCIAL_MODES.md` (Z8–Z10). It reuses the pilot's evidence
format rather than defining a second one.

**Governing rule, restated because every table below depends on it: no board,
vendor, model, RAM figure, storage figure, FPS number or bandwidth figure is
certified, estimated or extrapolated by this document.** Where a number does not
exist, the cell says `NOT ESTABLISHED` — never a plausible-looking placeholder.

## 1. What "certified" means here, and what it does not

A **certified unit** is one specific physical device — one manufacturer, one
model, one SoC, one RAM/storage configuration, one OS image, one firmware/driver
set — that has been put through the protocol in §5 and whose record in §6
exists in this repository.

Certification is therefore **per unit, not per class**. From a certified unit you
may conclude nothing about:

- other boards in the same architecture class (a certified ARM64 board does not
  certify "ARM64", nor another board from the same vendor);
- the same board with a different RAM, storage, camera count, resolution, FPS
  target or processing mode;
- the same board after an OS or driver change that the record does not name.

The record's configuration fields exist precisely so that this boundary is
mechanically checkable rather than a matter of interpretation.

**The certificate describes the configuration that was tested.** Change the
camera count or the mode, and the previous record no longer applies — a new run
is required.

## 2. Why a shipped code path cannot substitute for a physical run

Verified facts, each of which blocks a purely software certification:

- **CI never executes the real decode/inference harness.** The hardware
  benchmarks live behind the `localbench` build tag
  (`internal/perf/localbench_test.go`) and an explicit `GEOCAM_PERF=1` gate. CI
  runs `go test ./internal/perf -run '^TestScaleFull$'` and
  `./internal/soak -run '^TestSoak$'` *without* that tag, i.e. the synthetic
  scale/soak paths only.
- **CI's ARM64 is emulated.** The multi-arch image jobs build under QEMU, not on
  a physical ARM64 board (`.github/workflows/ci.yml`). Emulated build success
  says nothing about CPU cost or thermal behaviour.
- **The Python Vision Worker is not exercised by CI at all.** There is no Python
  job in `.github/workflows/ci.yml`, and the repository's Python test suite
  (`deploy/vision-worker/tests/`) is never invoked. The real worker under the Go
  agent is reached only through the double-gated `localbench` path.
- **The appliance package does not ship the Vision Worker**, so a Full Edge
  certification run cannot even start from the released artefact — it needs
  hand-provisioned Python, `ultralytics` and weights.
- **The only committed result artifacts were produced on the development
  machine.** Both files under `docs/performance/results/` record
  `"goos": "darwin"`, `"goarch": "arm64"`, `"cpu_model": "Apple M4"`,
  `"os_version": "macOS 27.0 26A428"`, with `"torch_cuda_reported":
  "unavailable"`. They are a **macOS/dev-host** measurement, not a Linux
  appliance measurement, and they are not a target-hardware record.
- **Camera ingestion is RTSP-only; there is no `v4l2`/`/dev/video*` path.**
  USB capture is outside the scope of certification entirely.

## 3. What the code can already measure (no new work)

This is the part Z7 genuinely benefits from: the instrumentation exists.

**Host identification and live resources.** `internal/platform` detects
hostname, OS, kernel, `GOARCH`, CPU count and total RAM, and samples CPU%,
memory used/available, disk used/available per mount, and the hottest readable
thermal zone from `/sys/class/thermal` (nil when the host exposes none; on
darwin CPU% is unavailable because it would need cgo). These are exposed on
`GET /status` under `resources`:

| Field | Notes |
| --- | --- |
| `resources.cpu.percent` | may be absent where the platform cannot measure it |
| `resources.memory.total_bytes` / `used_bytes` / `available_bytes` / `used_percent` | |
| `resources.disk.data_dir.*` | the `$GEOCAM_DATA_DIR` mount specifically |
| `resources.thermal.temperature_c` | absent when no thermal zone is readable |

**Reproducible benchmark harness with a self-describing schema.** The decode and
inference benchmarks write a JSON record stamped `"schema_version":
"geocam-perf/hito-x/1"` that already contains the host block
(`goos`, `goarch`, `cpu_model`, `memory_bytes`, `os_version`, `num_cpu`,
`go_version`, `ffmpeg_version`, `python_version`, `torch_version`,
`ultralytics_version`, `torch_cuda_reported`, `torch_mps_reported`), the exact
input clip with its SHA-256 and generation command, and the harness invocation
itself. A certification run therefore produces machine-comparable evidence for
free.

Verified invocation (env gates and test names confirmed in
`internal/perf/*.go`):

```sh
GEOCAM_PERF=1 \
GEOCAM_PERF_OUT=/var/tmp/cert \
GEOCAM_PERF_CLIP=/var/tmp/cert/clip.h264 \
go test -tags localbench ./internal/perf \
  -run 'TestXPerformance(Decode|Inference)' -count=1 -v

# Full Edge (real worker) additionally needs:
#   GEOCAM_PERF_PYTHON=<venv>/bin/python
#   GEOCAM_PERF_WORKER_SCRIPT=deploy/vision-worker/worker.py
#   GEOCAM_PERF_MODELS_DIR=<dir with the two .pt weights>
#   GEOCAM_PERF_DEVICE=cpu|cuda|auto
#   GEOCAM_PERF_INFER_SAMPLES / GEOCAM_PERF_INFER_WARMUP
```

Other harness gates that exist and their purpose (not a complete list — see
`internal/perf`): `GEOCAM_PERF_CAMERAS`, `GEOCAM_PERF_DURATION`,
`GEOCAM_PERF_SAMPLE_FPS`, `GEOCAM_PERF_IMGSZ`, `GEOCAM_PERF_FFMPEG`,
`GEOCAM_PERF_FFPROBE`, `GEOCAM_PERF_DECODE_ONLY`, `GEOCAM_PERF_INFER_ONLY`,
`GEOCAM_PERF_FAKE_WORKER`, `GEOCAM_PERF_TRACE`, `GEOCAM_PERF_VERBOSE`. The soak
harness is separately gated by `GEOCAM_SOAK=1` with `GEOCAM_SOAK_DURATION`.

**Operational signals to observe during a run.** `geocam-edge check` for the
one-line mode/status, `GET /status` for `queues` (per-router depth/capacity/drops,
`vision.drops`, `edge_backlog.drops`), `cloud.frames_upload_failed`, per-camera
state under `cameras`, and `vision.worker.device` / `device_requested` for the
worker-confirmed inference device.

**A certificate-shaped record already rejects invented numbers.** The codebase
already carries the evidence schema this document's record format is modelled
on: `internal/performance/matrix.go` defines exactly three row classifications —
`MEASURED`, `DERIVED`, `NOT_VALIDATED` — where a `MEASURED` row **requires**
date, commit SHA, hardware id, OS, architecture and the command, plus a
positive duration, and a `NOT_VALIDATED` row is **rejected if it carries any
numeric metric at all**. That is the mechanism that makes "no invented numbers"
enforceable rather than aspirational, and a certification record should be
expressible as rows of it.

**Thermal is read but nothing acts on it.** `internal/platform` reports the
hottest `/sys/class/thermal` zone, and it is exposed on `/status` and in
heartbeat metrics — but **no code consumes it to gate, slow or stop decoding or
inference**. The resource guards in `internal/fulledge/limits.go` cover disk
(`CanWriteEvidence`) and memory only; `LimitsConfig` has no temperature field.
So throttling on a Raspberry-class board will not be mitigated by software, and
characterising it is a manual measurement task in §5 step 3, not a logged event.

**A capacity limit that shapes the whole matrix.** At most
`GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES` pipelines run at once (default **4**,
range 1–16); the manager simply does **not admit** cameras beyond that. A 5–10
camera pilot therefore requires raising this knob above its default, and the
recorded configuration must say so — otherwise a run silently measures 4
cameras while appearing to measure 10. There is **no maximum camera count**
enforced anywhere else in the code; the ceiling is this concurrency knob plus
the device's real CPU/thermal budget. No number for that exists in this
repository.

**What is measurable without new work.** Host identity (hostname, distro, kernel,
`GOOS`/`GOARCH`, CPU count, total RAM), whole-host CPU% (Linux, after one priming
sample; unavailable on darwin because it would need cgo), RAM and disk
total/used/available, thermal zones where present, per-camera pipeline counters
and FPS (`input_fps`, `decoded_fps`, `output_fps`, `frames_dropped`, RTP packets,
decode latency), queue depth/capacity/drops per component under `queues`,
Cloud-transport byte accounting, application-payload network counters by
category, and GPU *presence* (not performance). One caveat to record rather than
misread: `/status`'s disk `available_bytes` is a non-pointer field with
`omitempty`, so a genuine 0 is indistinguishable from "not measured" — use the
known-flag semantics on the Go side, not the JSON, when a run must distinguish
them.

**Configured bounds that frame the matrix** (defaults, from
`internal/config/config.go` — these bound *what you may configure*, not what the
hardware supports): target FPS 0.1–30 (default 5); output up to 1920×1080
(default 640×360); ring buffer 1–300 (default 30); pipeline queue depth 4–512
(default 64); decode queue depth 1–16 (default 4); max concurrent ffmpeg
pipelines 1–16 (default 4); decode timeout 1s–60s (default 10s); inference
concurrency 1–16 (default 1); YOLO `imgsz` 128–1920 (default 640); worker start
timeout 1s–5m (default 30s); infer timeout 100ms–60s (default 5s); minimum free
disk 100 MiB default; max memory percent 0 = disabled.

## 4. Exclusion: GPU / CUDA is not certifiable today

Be precise, because two documents in this repository describe this differently:

- There is **no bespoke GPU/NPU inference backend**. No CUDA, TensorRT, RKNN or
  Hailo integration exists in `deploy/vision-worker/backend.py`.
- `GEOCAM_EDGE_YOLO_DEVICE` accepts `cpu`, `cuda`, `auto`, and the CUDA path is
  **delegated to Ultralytics/PyTorch** by `backend.py`'s `resolve_device()`,
  which reads `torch.cuda.is_available()`. So the parameter is real and is
  forwarded, but no code in this repository implements or validates the
  accelerator path itself.
- `docs/deployment/hardware.md` states that nothing in the repo implements a
  GPU/NPU backend and that a board should be assumed CPU-only. That remains the
  correct posture for sizing; the nuance above is only that `cuda` is not
  rejected — it is *requested* and silently reduced to CPU when unavailable,
  with an operator-visible warning.

Consequently **no CUDA configuration may appear as certified** in §6. A CUDA row
can only be added after a physical run on a host whose driver stack is named in
the record and whose `vision.worker.device` reports `cuda` — which has never
happened here.

## 5. Certification protocol

Run in order. A run that is aborted at step *n* certifies nothing; record where
it stopped. Every step records against the current commit and the unit's
identity.

**Step 0 — Provenance.** Record commit SHA, `geocam-edge version` (version,
commit, build date), the exact release artefact filename and its SHA-256, and
where the Ed25519 public key came from.

**Step 1 — Unit identity.** Manufacturer, model, SoC, CPU count, RAM, storage
device and size, OS image + version + kernel, and — for Full Edge — the Python
version, `torch` version, `ultralytics` version, and driver version. Capture the
`/status` `resources` block as the machine's own view of itself.

**Step 2 — Provisioning.** Which variables were set (processing profile, camera
count, target FPS, resolution, stream role), and for Full Edge where the Python
runtime and the model weights came from. Weights are **external and verified**:
record each file's name and SHA-256. Nothing may be auto-downloaded during the
run.

**Step 3 — Steady state per camera count.** For each count in the matrix
(1, then the pilot's 5–10, then any higher count claimed), run for a fixed
duration and record: decode FPS, inference FPS (Full Edge), CPU% and RAM
(peak and mean), thermal readings and whether throttling is observed, network
egress, and every drop counter (`queues.router[].drops`, `ring_buffer_dropped`,
`oversized_aus_dropped`, `vision.drops`, `edge_backlog.drops`,
`cloud.frames_upload_failed`). **A count is certified only up to the highest one
actually sustained without unbounded queue growth or dropped frames beyond the
documented policy.**

**Step 4 — Restart and power.** Clean `systemctl restart`, then an unclean power
cut. Verify identity and credential survive, the backlog is intact, no
duplicate supervisors or vision workers appear, and `cameras` recovers.

**Step 5 — OTA and rollback.** Apply a signed release over the previous version,
confirm `/readyz`, then force a failure and confirm automatic rollback. Record
wall-clock time to ready — this is the step that exposes a slow model load
against the 30-second readiness budget, which is a known risk for Full Edge.

**Step 6 — Outage and recovery.** Take the SaaS unreachable, observe the
documented behaviour (frames dropped by policy with buffering unconfigured;
events retained in the backlog), then restore it and confirm recovery without a
restart.

**Step 7 — Real systemd supervision.** Confirm watchdog liveness and
`StartLimitBurst` behaviour on a real PID 1 — these are `NOT_VALIDATED`
everywhere in this repository today (Hito Y recorded that no test ran on a host
where systemd is PID 1).

**Step 8 — Retention and disk.** Drive the evidence store to its configured
limits and confirm deterministic, non-destructive behaviour at capacity. **This
step cannot currently be completed**: there is no retention or eviction policy
for `events/` or `evidence/`, and the free-disk gate covers JPEG captures but
not MP4 clips. Record the outcome, not a pass.

## 6. Certification record format

One record per certified unit, committed under `docs/deployment/certifications/`
as `<vendor>-<model>-<date>.md`, plus the harness JSON it references. Every field
that was not actually measured says `NOT_VALIDATED` — never a placeholder.

```text
CERTIFICATION RECORD
commit
release artifact + sha256
certified on (date)

UNIT
manufacturer / model / soc
cpu count / ram / storage
os image / kernel / arch
python / torch / ultralytics        (Full Edge only)
driver version                      (GPU runs only)

CONFIGURATION
processing profile
camera count certified
resolution / target fps / stream role
device (cpu|cuda|auto) and worker-confirmed device
model files + sha256
configured bounds that differ from defaults

MEASUREMENTS
duration
decode fps
inference fps                      (Full Edge only)
cpu percent (mean / peak)
ram bytes (mean / peak)
thermal (mean / peak) / throttled
network egress
drop counters (all)
reconnects
uptime

SCENARIOS
restart        (clean)
power loss     (unclean)
ota + rollback
outage + recovery
systemd watchdog
retention at capacity

RESULT
certified | NOT_VALIDATED
what this record does NOT certify
```

## 7. Existing numbers, and how each must be read

| Number | Source | Real or synthetic | Usable for certification? |
| --- | --- | --- | --- |
| ~9.12 Mbps, one camera, JPEG q85, 5 FPS, 640×360 | `docs/performance/i10-edge-cloud-cost-metrics.md` | **Synthetic local benchmark** (`TestLocalBandwidthBenchmark`, pseudo-random frames, no network, no SaaS) | No — sizing hint only |
| "66.00%" Hybrid bandwidth reduction | `docs/performance/hybrid-j10-j11.md` | **Synthetic** — an injected `i%3==0` selector; never constructs a pipeline or a `MotionDetector` | No — do not quote |
| 1/5/10/25/50-camera scale runs | Hito X, `internal/perf` | **Synthetic loopback** | No — no commercial capacity is certified from them |
| decode + inference timings | `docs/performance/results/hito-x-*-m4-darwin-arm64.json` | **Real run, wrong platform** — Apple M4, macOS, `torch_cuda_reported: unavailable` | No — dev host, not a Linux target |
| RAM / storage minimums | `docs/deployment/hardware.md` | **NOT ESTABLISHED** | No — explicitly not a supported minimum |

## 8. Verbatim `NOT ESTABLISHED` markers (do not soften)

- `docs/deployment/hardware.md` — RAM: "**NOT ESTABLISHED — pending real
  measurement.**"; "512 MB / 1 GB is a **provisional provisioning assumption,
  not validated**"; Full Edge RAM: "No provisional number is offered for this
  profile."
- Storage: "**NOT ESTABLISHED — pending real measurement.**"; "2 GB / 4 GB is a
  **provisional provisioning assumption, not validated**, not a supported
  minimum."
- `docs/deployment/hardware.md` — inference: "**not measured** … Nothing here
  should be read as a throughput/FPS claim."
- Full Edge network: "no number is reported here because none was measured. Do
  not extrapolate one."
- ARM64: "Candidate target for Cloud/Hybrid gateway; **NOT HARDWARE
  VALIDATED**. Full Edge: **NOT PERFORMANCE VALIDATED** … treat as a research
  spike, not a supported target, until someone measures it."
- AMD64: "Candidate target … **NOT HARDWARE VALIDATED**. Full Edge: **NOT
  PERFORMANCE VALIDATED** … no comparative claim vs. ARM64 is made."

## 9. Classification

| Item | Status |
| --- | --- |
| Definition of "certified unit" and its non-extrapolation boundary | DOCUMENTED (this document) |
| Certification protocol (steps 0–8) | DOCUMENTED |
| Certification record format | DOCUMENTED |
| Instrumentation to fill the record (host metrics, `/status` resources, perf JSON schema) | IMPLEMENTED / TESTED (Hito N, Hito X — reused, no duplication) |
| Benchmark harness invocation and env gates | IMPLEMENTED / TESTED, gated behind `localbench` + `GEOCAM_PERF=1` |
| Candidate architecture classes | DOCUMENTED / **NOT HARDWARE VALIDATED** (Hito Q — unchanged) |
| Any specific board certified | **NOT_VALIDATED — physical certification run executed: NO** |
| RAM / storage minimums | **NOT ESTABLISHED** |
| Real decode/inference FPS on a Linux target | NOT_VALIDATED |
| CPU-only Full Edge on ARM64 | NOT_VALIDATED (research spike) |
| CUDA / GPU inference | NOT_VALIDATED — never executed; no bespoke backend exists |
| Camera-count capacity | NOT_VALIDATED — no maximum is enforced in code, so any figure must come from a run |
| Real systemd watchdog / `StartLimitBurst` on a PID-1 systemd host | NOT_VALIDATED (Hito Y) |
| Retention behaviour at capacity | NOT_VALIDATED — no retention policy exists for events/evidence |
| QEMU ARM64 build success as ARM64 evidence | INVALID as evidence — emulation, not physical |

## 10. How to close Z7

Z7 closes when **at least one** record exists in §6 for a named unit and
configuration, produced by a physical run of §5 on that unit — including a
`result: certified` and an explicit statement of what it does not certify. Z7
does **not** close by merging this document. Nothing else in the repository can
substitute for that run, and no number above may be filled in by inference.
