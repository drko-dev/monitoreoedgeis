# GEO CAM Edge — Commercial Modes (Hito Z: Z8 / Z9 / Z10)

> **G1 status (Hito Z G1-B):** production camera-target provisioning —
> discovery + cameracreds + authenticated ONVIF → `CameraTarget` →
> `rtsp.Manager.SetTargets` — is **IMPLEMENTED / TESTED LOCAL**. Every
> section below has been updated to reflect this; none of them describe G1
> as an open code gap anymore. What remains open, everywhere below, is the
> same as ever: **REAL CAMERA / REAL SaaS E2E: NOT_VALIDATED** (every test
> runs against fakes/simulators), **DVR/NVR: NOT_VALIDATED** (single-source
> only), **HARDWARE CERTIFICATION (Z7): NO**, **COMMERCIAL READY: NO**. Full
> wiring/test detail: [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md) §11.

This document defines what the **Gateway**, **Hybrid** and **Full Edge**
products are, using only capabilities that exist in this repository, and
states for each one exactly how far it has been verified. It is the
commercial/product companion to
[docs/ARCHITECTURE.md](../ARCHITECTURE.md) (which describes the technical
architecture and is not duplicated here).

**Every cell in the matrix below is derived from code on this branch, not from
intent.** Where the code and the product idea disagree, this document says so
and the disagreement is listed as a gap.

Two rules govern everything here:

- **No invented hardware.** No board, SoC or vendor is certified. The
  supported target architectures are `linux/amd64` and `linux/arm64`
  (`internal/platform`), and any specific board is a *documented candidate*
  only — see [docs/deployment/hardware.md](../deployment/hardware.md) (Hito Q,
  Z7 is still open). The Z7 hardware-certification block is the only thing
  that can change that.
- **No invented pricing, and no invented savings.** Where a number would be
  required, this document says the number does not exist.

## 1. There is no fourth mode

The agent has exactly three processing modes
(`internal/config/mode.go`): `cloud`, `hybrid`, `edge`. This document adds no
fourth one, and no second agent.

"Gateway" is **not** a mode. It is the commercial name for a
`(processing mode, media path)` combination that the existing configuration
already expresses. What makes the profiles distinguishable is a second,
pre-existing knob: `GEOCAM_VIDEO_PIPELINE_ENABLED` (default **false**), which
decides whether the local media path is built at all. `internal/agent/agent.go`
constructs the ffmpeg decode stages, the `processing.Manager`, the Cloud sink
and the local-inference Sink only inside that gate.

Because of that, `GEOCAM_PROCESSING_MODE` alone is a *request*, not a
description. A configuration can ask for `hybrid` and build nothing. To make
the running product unambiguous, the agent now derives and reports an
**effective profile** — `internal/config/profile.go`,
`config.ProfileFor(mode, videoPipelineEnabled)`:

| Effective profile   | Configuration                              | What the Edge actually does |
| ------------------- | ------------------------------------------ | --------------------------- |
| `gateway-no-media`  | any mode, `VIDEO_PIPELINE_ENABLED=false`   | No media path at all: no decode, no sampling, no gating, no frame upload, no local inference |
| `gateway`           | `cloud` + pipeline enabled                 | Light local media path (decode/resize/sample) → frames uploaded → **Cloud runs YOLO** |
| `hybrid`            | `hybrid` + pipeline enabled                | Local motion gating → only candidates uploaded → **Cloud runs YOLO** |
| `full-edge`         | `edge` + pipeline enabled                  | **Local YOLO** in an out-of-process Python Vision Worker; no frames uploaded for Cloud inference |

`/status` reports it as `profile` next to the unchanged `processing_mode`, and
the startup log prints both. When the two disagree — `hybrid` or `edge`
requested while the pipeline is disabled, so the mode's defining local stage
cannot run — the agent logs a warning naming the real profile. The
disagreement is reported, never rejected: `GEOCAM_PROCESSING_MODE` and
`GEOCAM_VIDEO_PIPELINE_ENABLED` have always been independent knobs, and staging
a Full Edge appliance before its Vision Worker is provisioned is legitimate.

> **The shipped default is `gateway-no-media`, not `gateway`.** A default
> appliance install (`cloud`, pipeline unset) runs ONVIF discovery and
> inventory, health, heartbeat, control and OTA, and constructs the RTSP
> connectivity subsystem — but it uploads **no frames at all**, so Cloud YOLO
> never sees an image regardless of camera wiring. Choosing the `gateway`
> product requires setting `GEOCAM_VIDEO_PIPELINE_ENABLED=true`. Production
> camera-target provisioning (discovery + cameracreds + authenticated ONVIF
> → `CameraTarget` → `rtsp.Manager.SetTargets`) is now **IMPLEMENTED /
> TESTED LOCAL** (Hito Z G1-B) — see
> [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md) §11. What remains
> open for this profile is hardware certification (Z7), not a code gap. See
> the profile examples in `deploy/appliance/config/`.

## 2. Commercial capability matrix

| Capability           | Gateway                              | Hybrid                                        | Full Edge                                     |
| -------------------- | ------------------------------------ | --------------------------------------------- | --------------------------------------------- |
| ONVIF discovery      | Yes — same module in all three       | Yes                                           | Yes                                           |
| RTSP                 | Subsystem constructed; per-camera operation wired (Hito Z G1-B), validated only against fakes/simulators | Same, plus the decode that consumes it | Same |
| Local decode         | **Yes** — ffmpeg decode/resize/sample | Yes — ffmpeg subprocess per camera           | Yes — ffmpeg subprocess per camera            |
| Local gating         | No                                   | **Yes** — block-luma motion diff, no model    | **No** — sampled frames go straight to local inference |
| Local YOLO           | **No**                               | **No**                                        | **Yes** — Python Vision Worker, out of process |
| Cloud YOLO           | Yes — sole inference engine          | Yes — sole inference engine                   | No — local inference replaces frame upload     |
| Offline queue        | Frames only, **off by default**       | Frames only, **off by default**                | Events/evidence: **on by default** (backlog)  |
| Local evidence       | No                                   | No                                            | Yes — `events/`, `evidence/captures/`, `evidence/clips/` |
| OTA                  | Yes                                  | Yes                                           | Yes                                           |
| GPU optional         | N/A (no local inference)             | N/A (no local inference)                      | Parameterized: `cpu` / `cuda` / `auto` — **no GPU is certified or validated** |
| Internet requirement | Required for any Cloud inference or control; local health/status work offline | Same | Same for sync/control/OTA; **inference is fully local** |

**The `Local gating` row is not a quality ranking.** Gateway and Full Edge do
not run the motion evaluator *at all*, and that is by design, not by omission:
its enablement is tied to the mode (`internal/agent/agent.go` sets
`Hybrid.Enabled = cfg.ProcessingMode == config.ModeHybrid`). Full Edge replaces
the gating step with real inference instead of adding to it.

**`Local decode` is not optional in the Gateway profile.** In the taxonomy this
document introduces, `gateway` *is* `cloud` with the pipeline enabled — so
decode/resize/sample always run. The profile that has no decode is
`gateway-no-media`, which is **not** the Gateway and **not** a fourth
processing mode; it is an effective profile derived from the pipeline flag.

Reading of each row, with its evidence:

- **ONVIF discovery** — `internal/discovery` runs in every profile and needs
  nothing from the media path (it imports neither `internal/rtsp` nor
  `internal/processing`). It only *reads* a stream URI as metadata; it never
  opens an RTSP connection. It is also the only one of these capabilities that
  is functional end to end today.
- **RTSP** — the **subsystem** is constructed whenever
  `GEOCAM_CONNECTIVITY_ENABLED=true` (default), independently of the mode and
  of the pipeline, and so is the decode that consumes its packets.
  **Operational per-camera RTSP connectivity is now wired in production**
  (Hito Z G1-B): discovery + cameracreds + authenticated ONVIF feed a
  deterministic target builder that calls `rtsp.Manager.SetTargets` for every
  single-source camera it can resolve credentials for — see
  [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md) §11 for the
  tested wiring and its test coverage. This has been verified against fakes
  and simulators only: **no real camera, no real appliance, no real SaaS
  end-to-end run has validated it.** DVR/NVR (multi-source) devices remain
  unsupported and NOT_VALIDATED — they are skipped, not collapsed.
- **Local decode** — the ffmpeg decode/resize/sample stages are built if and
  only if `GEOCAM_VIDEO_PIPELINE_ENABLED=true`; in every profile documented
  here that flag is on by definition.
- **Local gating** — `internal/processing.MotionDetector` is pure Go block
  luma diffing ("no OpenCV, no model"). It is constructed only when
  `cfg.Hybrid.Enabled`, which is true **only** for `hybrid` mode, so:
  `cloud` has no evaluator, `hybrid` has one, and `edge` has **none**. All the
  candidate/filtering logic in the pipeline is gated on `p.motion != nil`, so
  in Full Edge every sampled frame proceeds to local inference unfiltered.
  Hybrid cannot reach any model.
- **Local YOLO** — `newVisionSink` returns nil outside `edge` mode, and
  `newCloudSink` returns nil outside `cloud`/`hybrid`, so the Cloud frame sink
  and the local-inference sink are **mutually exclusive by construction** and
  no profile runs both. Switching mode at runtime replaces the whole sink set.
- **Offline queue** — two independent mechanisms, deliberately not merged:
  `internal/cloudsink.Buffer` for Cloud/Hybrid frames and
  `internal/edgebacklog.Backlog` for Full Edge events. The frame buffer has
  **no production default** (buffering activates only when both
  `GEOCAM_CLOUD_BUFFER_MAX_BYTES` and `GEOCAM_CLOUD_BUFFER_MAX_FRAMES` are set
  positive), so by default a Cloud/Hybrid frame that cannot be uploaded is
  lost. The event backlog defaults to 100 operations / 512 MiB and refuses new
  records rather than dropping them.
- **Local evidence** — only Full Edge persists events and evidence; Cloud and
  Hybrid keep nothing locally.
- **GPU optional** — see §5.

## 3. Product readiness per mode

Labels are used exactly as `AGENTS.md` defines them. **No mode is called
"commercial-ready", because all three are missing at least one criterion that
this repository cannot satisfy on its own.**

### Gateway — IMPLEMENTED, TESTED, NOT VALIDATED, BLOCKED (Z7 only)

| | |
| --- | --- |
| **IMPLEMENTED** | Yes. Profile is `cloud` + `GEOCAM_VIDEO_PIPELINE_ENABLED=true`. The default-profile derivation, the operator config example and the packaging documentation exist on this branch. As of Hito Z G1-B, production camera-target provisioning is also implemented: discovery + cameracreds + authenticated ONVIF → `CameraTarget` → `rtsp.Manager.SetTargets`, converging even when a credential arrives after the camera was already discovered — see [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md) §11. |
| **TESTED** | Yes. `internal/config/profile_test.go` pins the profile mapping and the "never claim a local stage you cannot run" invariant; `internal/health/profile_gate_test.go` pins the `/status` value; the existing suite covers discovery, heartbeat, control and OTA independently of the media path, plus the RTSP manager and the video pipeline as units. G1-B adds an end-to-end test (fake WS-Discovery + fake ONVIF WS-Security + a real `internal/rtsptest.Simulator`) proving discovery → builder → `SetTargets` → Supervisor → RTSP → `processing.Manager` for add, late-credential convergence, rotation, revoke, and TTL-based removal. |
| **VALIDATED LOCAL** | Partially. G1-B's own test above exercises the *production* `SetTargets` call site, not a direct test-only call — closing the gap the previous wording described — but still against **fakes and a simulator**, never a real camera, a real SaaS, or a real network path. `internal/cameratest`'s pre-existing integration tests (calling `SetTargets` directly, against synthetic local RTSP servers and a fake SaaS) independently validate that the pipeline works once handed a camera. |
| **NOT_VALIDATED** | Real ONVIF cameras; real per-camera RTSP and camera health; real bandwidth on a real uplink; the Cloud YOLO leg end to end; DVR/NVR (multi-source) devices, which G1 does not support and simply skips. |
| **BLOCKED** | **Hardware certification (Z7) only.** No board is certified, and `docs/deployment/hardware.md` states the RAM/storage minimums are **NOT ESTABLISHED** and the only bandwidth figure (~9.12 Mbps, one camera) is a **synthetic local benchmark**, not a production measurement. The camera-target provisioning code gap this row used to also list (G1) is closed — see above. |

### Hybrid — IMPLEMENTED, TESTED, NOT VALIDATED, BLOCKED

| | |
| --- | --- |
| **IMPLEMENTED** | Yes, and its contract is precise: full local H.264 decode and resize of every sampled frame, a local, model-free motion evaluator, and upload of **candidates only**. All inference stays in the Cloud, and the Cloud contract is unchanged — hybrid frames merely carry extra metadata headers (`X-Processing-Mode: hybrid`, `X-Candidate-Reason`, `X-Correlation-Id`). |
| **TESTED** | Yes. The motion evaluator, the candidate/counters path, the sampler regression guards and the mode mutual-exclusion tests all run in CI. |
| **VALIDATED LOCAL** | Partially, on synthetic input only. |
| **NOT_VALIDATED** | Real candidate accuracy on real scenes; real bandwidth reduction; multi-camera behaviour. |
| **BLOCKED** | **No measured bandwidth saving exists.** The only number in the repository — "66.00% bandwidth reduction" in [docs/performance/hybrid-j10-j11.md](../performance/hybrid-j10-j11.md) — is explicitly labelled **synthetic**: the harness injects a fixed `i%3==0` selector and calls `CloudSink.Route` directly. It never constructs a pipeline or a `MotionDetector`, and the document itself says it "does not measure the real Hybrid J1-J5 motion detector's candidate accuracy or its real-world bandwidth savings". That document's own acceptance criterion (positive reduction on non-continuous scenes) is therefore **unmeasured**. **Do not quote a percentage in any commercial material.** A second, independent constraint: Hybrid has **no byte-rate cap by default** (`GEOCAM_CLOUD_MAX_BYTES_PER_SEC`/`_BURST_BYTES`/`_MAX_FPS` all default to `0` = unlimited), so the only default bound on upload volume is `GEOCAM_VIDEO_TARGET_FPS` (5) times the candidate ratio. Those three knobs exist and are documented but their production values are an **open upstream decision** — this repository deliberately invents no number for them. |

### Full Edge — IMPLEMENTED, TESTED, NOT VALIDATED, BLOCKED

| | |
| --- | --- |
| **IMPLEMENTED** | Yes. Local inference is genuinely out of process: the Go agent `exec`s the Python worker and speaks newline-delimited JSON over a single Unix socket. **PyTorch is never inside the Go process** — `go.mod` has zero third-party dependencies, no `import "C"` exists anywhere, and the build is `CGO_ENABLED=0` on every target. The Go agent plus Python Vision Worker split is preserved. |
| **TESTED** | Yes for the Go side (worker lifecycle, handshake, restart/backoff, device fields, events, evidence, backlog, sync, quarantine, OTA) and for the Python `resolve_device` contract. The Python worker's own test suite exists but **CI never runs it** — there is no Python job in `.github/workflows/ci.yml`. |
| **VALIDATED LOCAL** | Only through a **fake** worker. The real `deploy/vision-worker/worker.py` under the Go agent is touched exclusively by `internal/perf`, behind both a `localbench` build tag and a `GEOCAM_PERF=1` environment gate. CI never runs it. |
| **NOT_VALIDATED** | Real model loading; real inference; real CUDA; the packaging path; OTA restart interaction with a live worker; retention of events and evidence. |
| **BLOCKED** | **The Python runtime is still provisioned by hand, but the packaging half is closed (B2).** `package.sh` now stages the worker's sources (`worker.py`, `backend.py`, `requirements.txt`) into every artifact under `vision-worker/`, `install.sh` places them in the versioned release directory and derives the worker *script* path into the systemd unit, and `scripts/check-vision-runtime.sh` verifies the interpreter and the real dependencies explicitly. What the package still does **not** and cannot ship is the runtime itself: ultralytics/PyTorch wheels are architecture-specific, so a portable virtualenv across amd64+arm64 cannot exist honestly — and no interpreter path is invented, so `GEOCAM_EDGE_YOLO_WORKER_CMD` remains operator-supplied. Weights stay external under `GEOCAM_DATA_DIR/models` and are never touched. Inference remains not provisioned until an operator installs Python and the dependencies on the appliance. |

## 4. Full Edge product requirements (no certified hardware)

`GEOCAM_EDGE_YOLO_DEVICE` accepts exactly `cpu`, `cuda`, `auto`
(`internal/fulledge.ParseDeviceMode`). What each one actually means:

- `cpu` — the default. The worker runs on CPU.
- `cuda` — **requested**, not guaranteed. On a host without CUDA both layers
  fall back to CPU *with an operator-visible warning*: the Go side
  detects `/dev/nvidia0`, `/dev/nvhost-ctrl` and `nvidia-smi`
  (`internal/fulledge/hardware.go`) and increments a fallback counter, and the
  Python worker reads `torch.cuda.is_available()` and reports the substitution
  through `--device`/`device_requested` in its health handshake. There is no
  hard error and no silent success.
- `auto` — resolves to CUDA when `torch.cuda.is_available()` reports true,
  otherwise CPU.

The device the *worker* confirms is what `/status` reports under
`vision.worker.device` and what each event records. Note gap **G6**: the
aggregate `full_edge.current_inference_device` is detected independently on the
Go side and can disagree with it.

**Hardware posture:**

- No GPU is certified, validated or benchmarked by this repository, and no
  claim of GPU inference is made anywhere in it. `cuda` is *parameterized*
  only.
- No deployment artefact grants access to `/dev/nvidia*`: there is no
  `DeviceAllow`, no `SupplementaryGroups`, no udev rule and no driver
  installation step. CUDA is reachable **only by omission** — the systemd unit
  deliberately does **not** set `PrivateDevices=true`.
- That omission is intentional and load-bearing. `PrivateDevices=` is
  deliberately absent (the unit file explains why) because Full Edge's CUDA
  path needs the host accelerator nodes `/dev/nvidia*`; a global
  `PrivateDevices=true` would mask them. A test asserts its absence
  (`deploy/appliance/appliance_test.go`). It must not be added — and any
  future hardening of the unit has to account for accelerator nodes.
- The `geocam-edge` service user is created by `install.sh` without any device
  group membership, so whether it can open the accelerator nodes depends
  entirely on the host driver's node modes. **Unverified.**
- Model weights are **not** bundled: `GEOCAM_EDGE_YOLO_MODELS_DIR` defaults to
  `$GEOCAM_DATA_DIR/models` (deliberately outside the versioned release tree,
  so an agent update never destroys installed weights), and the default
  filenames are `yolo11s-pose.pt` and `yolo11n.pt`. Missing weights produce the
  explicit `model_missing` state — nothing is auto-downloaded. There is no
  environment variable for the model *filenames*; they are reachable only
  through remote configuration.
- Real RAM, disk and CPU cost for Full Edge is **NOT ESTABLISHED**. See
  [docs/deployment/hardware.md](../deployment/hardware.md).

## 5. What each mode does and does not do locally

The three pipelines are **not** nested. Hybrid adds gating to Gateway's path;
Full Edge *diverges* from it — it keeps decode and sampling, drops gating
entirely, and replaces the Cloud upload with local inference.

**Gateway (`cloud` + pipeline)** — locally:

```
RTSP ingest → H.264 depacketize → ffmpeg decode → resize → fixed-FPS sample
            → JPEG encode → Cloud upload → Cloud YOLO
```

In the Cloud: everything else, including YOLO. It runs no local model and no
motion evaluator. It requires outbound connectivity to the SaaS for any
inference to happen at all.

**Hybrid** — locally:

```
RTSP ingest → H.264 depacketize → ffmpeg decode → resize → fixed-FPS sample
            → MotionDetector (block-luma diff, no model)
            → candidate selection
            → Cloud upload (candidates only) → Cloud YOLO
```

That is Gateway's path **plus** a block-average luma diff against the previous
frame with optional normalized ROIs, a candidate decision, and candidate
metadata stamping. Frames below the threshold are never dispatched. In the
Cloud: unchanged — the same JPEG quality, the same endpoint, the same limiter,
the same offline buffer, and YOLO. The adaptive idle sampler
(`GEOCAM_VIDEO_HYBRID_IDLE_FPS`) is **off by default** and, as of this branch,
can no longer leak into Cloud mode (see the defect list, D2).

**Full Edge** — locally:

```
RTSP ingest → H.264 depacketize → ffmpeg decode → resize → fixed-FPS sample
            → Vision Worker (local YOLO) → detections
            → local events/evidence
            → durable event/evidence backlog
            → SaaS sync
```

Note what is **absent**: there is no `MotionDetector` in this path. Full Edge
does not use Hybrid's gating, because `Hybrid.Enabled` is true only for
`hybrid` mode, and the pipeline builds the evaluator only when it is. Every
sampled frame therefore goes to local inference unfiltered. Gating is not a
prerequisite for inference, and Full Edge does not sample *less* than Gateway —
it just decides what is interesting with a model instead of a luma diff.

Each sampled frame is JPEG-encoded and sent over the Unix socket to the Python
worker; the detections become local events and evidence
(`events/<uuid>.json`, `evidence/captures/<uuid>.jpg`,
`evidence/clips/<event_uuid>.mp4`). Those are queued in the durable backlog and
synced to the SaaS in strict order. In the Cloud: nothing inference-related.

The Cloud frame sink and the local-inference sink are mutually exclusive by
construction — `newCloudSink` returns nil outside `cloud`/`hybrid` and
`newVisionSink` returns nil outside `edge` — so no profile uploads frames for
Cloud inference *and* runs local inference.

### Internet requirement, precisely

The local health surface (`/healthz`, `/readyz`, `/status`) and discovery do not
require the SaaS. Heartbeat, enrollment, the control channel, remote
configuration, discovery-run claims and OTA do. `/readyz` is local-only in
every mode. For RTSP and Full Edge local inference, see the qualification
immediately below — the capability is local, but the current wiring cannot
reach it.

On Full Edge offline behaviour, keep three different statements apart:

- **Architectural / local capability:** once a camera stream is actually
  provisioned into the RTSP/video pipeline, Full Edge inference and local
  event/evidence persistence do not require SaaS reachability — the backlog
  simply accumulates and drains when the SaaS returns.
- **Current product wiring:** camera targets are now provisioned in
  production (Hito Z G1-B). So the architectural property above is no
  longer gated on a missing wiring step — it still is not an end-to-end
  operational statement about a shipped product, since nothing here has run
  against real hardware.
- **Real validation:** none. No camera, no real model, no real tenant.

### VPN and subnet routing

There is **no** VPN, tunnel or subnet-routing implementation in this
repository, and none is required: the architecture treats the tunnel as
customer-side infrastructure. What the code does contribute is that it
*excludes* tunnel interfaces (`wg`, `tun`, `tap`, `tailscale`, `zt`, …) from
automatic discovery, so discovery does not try to scan the tunnel. Cross-subnet
target provisioning and automatic discovery across subnets are **not
implemented** — see `docs/ROADMAP.md` (R5) and
[docs/deployment/corporate-networking.md](../deployment/corporate-networking.md).

## 6. Defects found by this audit, and what was done

Each item is a concrete gap between what the repository says and what it does.
Fixes are limited to real correctness defects in profile and configuration
handling, plus precision in this document; no new subsystem was built.

The `G` items other than G1 are **not** fixed and are **not** in scope for
this PR (or for Hito Z G1-B). **G1 itself was closed by Hito Z G1-B** — see
its updated row below and [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md)
§11 — and is kept in this table, marked as such, so its history stays
visible. The Vision Worker packaging gap, hardware certification (Z7), CUDA
validation, a real pilot, storage retention and production bandwidth limits
all remain open.

| ID | Defect | Action |
| -- | ------ | ------ |
| **D1** | `/status` reported a `full_edge` block (zeros + hardware + limits) and a fabricated `queues.vision` entry in Cloud/Hybrid mode, contradicting the field's own documentation. Root cause: `newFullEdgeService` gates on `DataDir` only, not on mode. | **Documented here; comments corrected.** The service must pre-exist for a runtime transition to edge mode, so gating it on the startup mode would break remote-config mode changes. The status contract is what is wrong, and it is recorded as a known imprecision rather than silently re-gated. |
| **D2** | A Cloud-mode Edge silently inherited Hybrid's adaptive sampling: the pipeline built its sampler with `Hybrid.IdleFPS` unconditionally while `NoteMotion` is only ever called when a motion detector exists, so with `GEOCAM_VIDEO_HYBRID_IDLE_FPS` set, Cloud mode sat permanently idle and emitted at IdleFPS instead of TargetFPS. | **Fixed** (`internal/processing/pipeline.go`): the sampler is adaptive only when `Hybrid.Enabled`; regression-guarded through the real constructor (fails if reverted). |
| **D3** | `Sampler.SetTargetFPS` never recomputed the idle interval, so a remote-config TargetFPS reduction below the configured IdleFPS left the sampler emitting *faster* while idle than the new ceiling — breaking the documented "TargetFPS remains the ceiling" invariant. | **Fixed** (`internal/processing/sampler.go`): the interval is recomputed and adaptive sampling switches off when idle is no longer below the ceiling; regression-guarded. |
| **D4** | `hybrid`/`edge` with the video pipeline disabled were accepted silently, and `edge` additionally pinned `/readyz` to **503 forever** (the vision gate waited for a worker that can never be constructed) while `/status` said READY — which made the appliance's `update.sh` roll a healthy release back. | **Fixed** for the readiness trap (`internal/health/health.go`): with the pipeline disabled there is no worker to gate on. **Reported** for the mode mismatch: the agent now logs a warning naming the effective profile. Not rejected, because the two knobs have always been independent. |
| **D5** | Full Edge could not start on a fresh appliance: the default socket is `$GEOCAM_DATA_DIR/run/vision-worker.sock`, and **nothing** created `run/` — `install.sh` creates `DATA_DIR`, `DATA_DIR/ota` and `DATA_DIR/ota/pending`; the Python worker only `bind()`s the path; the Go side only removed a stale socket. First spawn failed with `ENOENT` and the worker looped in `error`. Hidden because every test pointed the socket at an existing temp directory. | **Fixed** (`internal/vision/worker.go`): the Go side provisions the socket's parent directory; regression-guarded end-to-end. |
| **D6** | This document itself misdescribed two profiles. It claimed Full Edge uses Hybrid's motion evaluator ("same evaluator, then real inference") and that Full Edge does "everything Hybrid does, except…". Both are false: `Hybrid.Enabled` is set only for `hybrid` mode, so an `edge` pipeline builds **no** `MotionDetector` and every sampled frame reaches local inference unfiltered. It also called Gateway's local decode "optional", when in this document's own taxonomy `gateway` *is* cloud with the pipeline enabled, so decode is never optional in it. | **Fixed in this document** (matrix rows, §2 rationale, §5 pipelines). No code change was needed or made: the code was already correct and unambiguous. Recorded because a capability matrix that overstates a profile is exactly the kind of claim this document exists to prevent. |
| **G1** | **CLOSED by Hito Z G1-B.** Discovery + cameracreds + authenticated ONVIF now feed a deterministic target builder that calls `rtsp.Manager.SetTargets` for every single-source camera it can resolve credentials for, converging even when a credential arrives after the camera was already discovered. See [G1_CAMERA_TARGET_WIRING.md](G1_CAMERA_TARGET_WIRING.md) §11 for the full wiring and its tests. | **IMPLEMENTED / TESTED LOCAL.** Validated only against fakes and a `rtsptest.Simulator` — no real camera, no real appliance. DVR/NVR (multi-source) devices remain unsupported and NOT_VALIDATED; they are skipped, never collapsed under one target. |
| **G2** | An operator-facing config file told operators **not** to use the two implemented modes: `deploy/appliance/config/geocam-edge.env.example` said "only 'cloud' has a working implementation today… do not select them in production", and `docs/deployment/appliance.md` and `README.md` said Hybrid/Edge "have no functional implementation yet". All three were false at this branch's base. | **Fixed.** The stale claims are corrected and the profiles are documented. |
| **G3** | The env example claimed to be "the full, commented list of every `GEOCAM_*` variable" but omitted **26** variables actually read by `config.Load()`: every `GEOCAM_EDGE_YOLO_*` and `GEOCAM_VIDEO_HYBRID_*` key, the local-event backlog bounds, the clip ceiling, the inference concurrency, the resource guards and `GEOCAM_HEARTBEAT_AUTH_FAILURE_INTERVAL`. | **Fixed.** All are now documented with their real defaults and ranges, and per-profile examples were added. |
| **G4** | The remote-config runtime applier does not exist when the video pipeline is disabled, so the module substitutes a no-op adapter that accepts everything, and the engine records and ACKs `applied` to the SaaS for tuning that changed nothing. | **Not fixed — reported.** Correcting it changes remote-config outcome semantics and the SaaS contract; it is recorded so it is not mistaken for working tuning. |
| **G5** | Documentation asserted the Vision Worker **does not exist** ("No Python, no YOLO, no PyTorch is present in this repository"), and that the modes implement "no functional difference". Both false at this base. | **Fixed.** |
| **G6** | `full_edge.current_inference_device` is detected Go-side while `vision.worker.device` and each event's device are worker-confirmed; the two can disagree on one `/status`. | **Reported.** The worker-confirmed value is the authoritative one to read. |
| **G7** | `internal/config` and its tests claimed the agent resolves `GEOCAM_EDGE_YOLO_DEVICE` through `HardwareManager` "before passing it to the worker". It does not — the worker receives the raw configured string and resolves it authoritatively in Python. | **Comment corrected** to describe the real two-layer contract (Go preselects for status; the worker decides). The raw string is passed through, and a *syntactically invalid* value reaches PyTorch — narrow, but real. |
| **G8** | No retention or eviction for `events/` or `evidence/`; the free-disk gate covers JPEG captures but **not** clips. Unbounded disk growth under sustained detections. | **Reported — out of this scope.** Storage retention is Hito V's slice in the SaaS repository plus the Edge side of it; inventing a policy here would invent commercial numbers. |
| **G9** | `internal/hybrid` (the J6 classifier adapter) is unreachable dead code — no package imports it and it is absent from the built binary's dependency graph. | **Reported.** Removing it is a separate cleanup; noting it prevents it being mistaken for a working model-based classifier. |
| **G10** | A unix socket path longer than 104 bytes (macOS/BSD) or 108 (Linux) fails. The default production path is well inside it, but an operator-set `GEOCAM_EDGE_YOLO_SOCKET_PATH` is unvalidated. | **Reported.** Surfaced while writing the D5 regression test, which had to shorten its own path. |

## 7. What needs real hardware, a real host, or an external system

None of the following can be verified from this repository, and none is
claimed:

- **Z7 hardware certification** — any board, and the unprivileged service
  user's ability to open `/dev/nvidia*` under a real driver install.
- **Real CUDA inference** — `torch.cuda.is_available()` and Ultralytics' CUDA
  path have never executed here; the repository's own tests patch CUDA
  detection.
- **Real Python worker driven by the Go agent** — only behind the double-gated
  `localbench`/`GEOCAM_PERF` path, which CI never runs. The Python test suite
  exists but CI has no Python job.
- **Real model loading, real inference accuracy, real timing** — including
  whether a cold torch + weights load fits `TimeoutStartSec=30`,
  `GEOCAM_EDGE_YOLO_START_TIMEOUT` (30s) and `wait-ready.sh`'s 30-second
  `/readyz` budget. If it does not, `update.sh` rolls back a healthy Full Edge
  release.
- **Real cameras** — ONVIF discovery against real devices, real RTSP, real
  camera-health transitions, real multi-camera load.
- **Real bandwidth** for Gateway and Hybrid on a real uplink, and therefore any
  statement about savings.
- **Real SaaS end to end** — the Cloud YOLO leg, event ingestion, control
  commands and OTA against a real tenant.
- **Real ENOSPC / power loss** on a real appliance — see
  [docs/resilience/RESILIENCE_MATRIX.md](../resilience/RESILIENCE_MATRIX.md).

## 8. Quick reference

```sh
# Gateway — media path ON; Cloud runs YOLO
GEOCAM_PROCESSING_MODE=cloud
GEOCAM_VIDEO_PIPELINE_ENABLED=true
GEOCAM_CONNECTIVITY_ENABLED=true
GEOCAM_DISCOVERY_ENABLED=true

# Hybrid — local gating, Cloud inference
GEOCAM_PROCESSING_MODE=hybrid
GEOCAM_VIDEO_PIPELINE_ENABLED=true
GEOCAM_VIDEO_HYBRID_MOTION_THRESHOLD=8      # defaults shown; all optional

# Full Edge — local YOLO, out-of-process
GEOCAM_PROCESSING_MODE=edge
GEOCAM_VIDEO_PIPELINE_ENABLED=true
GEOCAM_EDGE_YOLO_WORKER_CMD=/path/to/venv/bin/python   # no default: must be provisioned
GEOCAM_EDGE_YOLO_DEVICE=cpu                            # cpu | cuda | auto
```

Ready-to-copy files, each with the reasoning inline:

- `deploy/appliance/config/geocam-edge.env.gateway.example`
- `deploy/appliance/config/geocam-edge.env.hybrid.example`
- `deploy/appliance/config/geocam-edge.env.fulledge.example`

Check which profile an Edge is actually running:

```sh
geocam-edge check                 # status + mode
curl -s localhost:8091/status | jq '{processing_mode, profile, video_pipeline, cloud, vision, full_edge}'
```

`processing_mode` is what was requested. `profile` is what is running.
