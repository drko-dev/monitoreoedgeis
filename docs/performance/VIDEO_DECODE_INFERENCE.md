# Hito X — video decode FPS and inference FPS

Scope: **reproducible measurement of VIDEO DECODE FPS and INFERENCE FPS only.**
This document does not close Hito X. It does not cover the 1/5/10/25/50-camera
benchmark, global CPU/RAM, total network, or the commercial hardware/camera
matrix — `docs/ROADMAP.md` still lists all of those as pending.

> [!IMPORTANT]
> **Hito X is NOT complete.** What exists here is one measurement axis, on one
> host, for one camera's pipeline. Every number below is an observation on the
> stated hardware with the stated source. Nothing here is a product commitment,
> a sizing guarantee, or a claim about the production appliance.

| Category | Status |
|---|---|
| **Decode FPS, production pipeline** | **MEASURED** on the benchmark host (synthetic source, loopback RTSP) |
| **Decode FPS, real camera / real network** | **NOT MEASURED** — no camera, no Wi-Fi/LTE in the loop |
| **Inference FPS, real YOLO models, CPU** | **MEASURED** on the benchmark host |
| **Inference FPS on the production appliance (Linux amd64/arm64)** | **NOT MEASURED** |
| **Inference FPS on CUDA** | **NOT MEASURED** — no CUDA device on any host used here |
| **Inference FPS with real camera content** | **NOT MEASURED** — the frames are decoded from a synthetic clip |
| **1/5/10/25/50 cameras, CPU/RAM, network totals** | **OUT OF SCOPE** for this axis |

## 1. How to run it

Both benchmarks are behind two gates, so `go test ./...` neither compiles nor
runs them:

```bash
# 1) Decode only. Needs ffmpeg + ffprobe on PATH (the harness generates and
#    verifies its own clip); neither is needed by `go test ./...`.
GEOCAM_PERF=1 GEOCAM_PERF_OUT=/tmp/geocam-perf \
  go test -tags localbench ./internal/perf/ -run TestXPerformanceDecode -v -count=1

# 2) Inference only. Needs a Python with ultralytics/PyTorch and the two real
#    weight files. The harness never downloads a model.
GEOCAM_PERF=1 GEOCAM_PERF_OUT=/tmp/geocam-perf \
  GEOCAM_PERF_PYTHON=/path/to/python3 \
  GEOCAM_PERF_MODELS_DIR=/path/to/models \
  GEOCAM_PERF_DEVICE=cpu \
  go test -tags localbench ./internal/perf/ -run TestXPerformanceInference -v -count=1
```

`go test -tags localbench ./...` alone still runs nothing: `GEOCAM_PERF=1` is
required as well. What CI does run is the untagged part of `internal/perf`,
which proves the harness compiles and its counters work — and produces no
performance number.

Useful overrides: `GEOCAM_PERF_CLIP` (`WxH@FPSxFRAMES`, default `640x360@15x300`),
`GEOCAM_PERF_CLIP_FILE` (measure a pre-existing clip instead), `GEOCAM_PERF_DECODE_RUNS`
(`realtime,realtime3x,max,direct,direct-realtime`), `GEOCAM_PERF_INFER_SAMPLES`,
`GEOCAM_PERF_INFER_WARMUP`, `GEOCAM_PERF_IMGSZ`, `GEOCAM_PERF_STABILITY_MS`,
`GEOCAM_PERF_VERBOSE=1` (real pipeline/worker logs on stderr),
`GEOCAM_PERF_TRACE=1` (a pipeline status sample every 250 ms — this is how the
decoder priming below was found rather than guessed).

## 2. What was measured, and on what

| Item | Value |
|---|---|
| **Host** | `darwin/arm64`, Apple M4, 10 CPUs, 16 GiB, macOS 27.0 (26A428) |
| **Go** | `go1.27.1` |
| **ffmpeg / ffprobe** | `ffmpeg version 8.1` (benchmark host build — **not** the LGPL-only build shipped in the appliance image) |
| **Python / torch / ultralytics** | 3.14.6 / 2.14.0 / 8.4.155 |
| **CUDA / MPS, as torch reports them** | `cuda unavailable`, `mps available` |
| **Models** | `yolo11s-pose.pt` (`1060bda4…c2ba`), `yolo11n.pt` (`0ebbc80d…4ee1`) — the repo's fixed K2 models, never substituted |
| **Inference input** | 640×360 frames, `imgsz=640`, conf 0.5/0.5, NMS IoU 0.45, JPEG quality 90 |

Machine-readable results are committed next to this document:
`docs/performance/results/hito-x-decode-m4-darwin-arm64.json` and
`docs/performance/results/hito-x-inference-m4-darwin-arm64.json`. They are the
evidence; this prose is a reading of them.

## 3. The video source

A synthetic clip, because no camera is available and CI must not depend on one:

| Property | Value |
|---|---|
| Container | raw H.264 Annex-B elementary stream (`-f h264`) |
| Codec / profile | `h264` / Main, `yuv420p` |
| Resolution | 640×360 (the repo's own `DefaultVideoOutputWidth/Height`) |
| Nominal source FPS | 15 |
| Coded frames | 300 (20 s) |
| GOP | 30, `no-scenecut`, `repeat-headers`, **`aud=1`** |
| SHA-256 | `beb3690febdd8865a637ae3a749d02260d6852cce6757fa43e115401b32e3b6e` |
| Reproducibility | generating the same spec twice on this host produced **byte-identical** output (`regenerated_identical: true`) |

The exact generator command is recorded in the JSON (`generation_command`) and is
reproduced by `ClipSpec.FFmpegArgs`. `aud=1` matters: it makes each picture carry
an access unit delimiter, which is what lets the harness split the bitstream into
frames correctly even though this encoder emits five slices per picture. The
harness **refuses to report** if its own split disagrees with `ffprobe
-count_frames`, so "decode FPS" can never be computed over the wrong
denominator.

A raw Annex-B stream carries no container timing, so `ffprobe`'s `r_frame_rate`
for it is a guess (it says `30/1`). The nominal rate used everywhere is the
generator's `-r 15`, and the JSON says so explicitly
(`nominal_source_fps_source`) while keeping the guess visible
(`ffprobe_guessed_fps`) so it cannot be mistaken for the nominal rate.

## 4. What is real, and what is faked

Real, unmodified production code:

- `internal/rtsptest.Simulator` — a real RTSP/RTP/AVP/TCP server (the fixture
  Hito W3 added), serving real RTP packets of a real H.264 bitstream;
- `internal/rtsp.Manager` — the real RTSP client and supervisor;
- `internal/processing` — the real depacketizer, the real
  `ffmpeg -f h264 -i pipe:0 -f rawvideo -pix_fmt yuv420p pipe:1` subprocess, the
  real sampler, the real router and its bounded queues;
- `internal/vision.Worker` spawning `deploy/vision-worker/worker.py`, and
  `internal/vision.Sink` encoding each frame exactly as production does;
- the real `yolo11s-pose.pt` / `yolo11n.pt` weights.

Faked only at two external boundaries:

- the **decoder subprocess**, in the untagged tests only, so the harness's own
  counting is testable without ffmpeg. Every such run is labelled
  `decoder_fake: true` and asserts nothing about ffmpeg;
- the **model**, in the untagged tests only, via a stand-in that speaks the wire
  protocol. Those runs are labelled `kind: "fake-worker"` and
  `real_inference_performance_validated: false`.

A fake validates the harness. It never produces a performance number.

## 5. Measurement model: priming, measured feed, tail drain

The measurement is split into three phases because this decode path behaves
differently in each, and blending them produces a number that describes none of
them.

This was not a design preference — it was found by tracing the pipeline
(`GEOCAM_PERF_TRACE=1`) after an earlier draft reported a suspiciously low
decode rate:

- fed a raw H.264 elementary stream, **ffmpeg emits nothing at all for roughly
  the first 3.3 s** of a 15 fps feed (about 50 frames of input). Priming it with
  a short pre-roll and then going quiet makes the first output arrive *later*,
  not sooner, so priming is a **continuous** pre-roll, never a pause;
- once primed, the decoder sustains the input rate while holding a **persistent
  lag of about a dozen frames** (`decoder_buffered_lag_frames`). Those frames
  are not lost, they are still inside the decoder;
- when the source stops, the remaining frames trickle out over seconds, so
  closing the decoder immediately discards them.

`PipelineStatus.decode_latency_ms` (≈867 ms in the traced run) is a faithful
reading of that persistent lag — it is *not* per-frame decode cost.

Consequently every rate is reported twice: over the **measured feed window**
(the decoder actively fed, priming excluded) and **end to end** (including the
tail drain), plus `steady_state_achieved`, which is the harness's own check that
`decode_fps / ingest_fps ≥ 0.85`. When that check fails, `decode_fps` describes a
warming decoder and `decode_fps_end_to_end` is the number to read.

For inference, warm-up is likewise separated: **3 warm-up inferences are
excluded** from every statistic and reported on their own
(`warmup_ms: [802.1, 65.9, 62.8]`). The first call pays one-time model/backend
initialisation — ~800 ms here, ~13× a steady-state inference. Two passes are
measured: the worker in isolation (`worker.Infer` on a pre-encoded JPEG, which is
where the worker's own `inference_ms` comes from) and the full production path
(`vision.Sink.Route`: yuv420p → image → JPEG → socket → worker → YOLO).

## 6. Results — video decode (640×360, H.264 Main, 15 fps source)

| Run | Pacing | Measured frames | Ingest FPS | **Decode FPS** | Dropped | Decoder lag | Tail flushed | End-to-end FPS | Steady state |
|---|---|---|---|---|---|---|---|---|---|
| **Production pipeline** | real time (15 fps) | 240 | 15.06 | **15.06** | 0 | 0 | 0 | 14.38 | **yes (1.000)** |
| **Production pipeline** | 3× real time (45 fps) | 200 | 45.16 | **45.16** | 0 | 0 | 0 | 38.59 | **yes (1.000)** |
| Production pipeline | flooded (burst) | 299 | 9537.95 | 542.29 ⚠ | 176 | 282 | 86 | 109.80 | no (0.057) |
| Decoder driven directly | flooded | 300 | 3008.08 | **2617.03** | 0 | 39 | 24 | 283.54 | yes (0.870) |

Reading it:

- **The production pipeline keeps up with a 15 fps camera exactly, with zero
  dropped frames** — ingest 15.06/s in, 15.06/s decoded out, and the same holds
  at **3× real time (45.16/s)** on this host. It is not the bottleneck at
  640×360.
- The **decoder itself** sustains **≈2600 decoded frames/s** on this content, so
  a single 15 fps camera uses well under 1 % of its decoding capacity. That
  headroom is what the flooded rows are competing for, not the camera's rate.
- The **flooded pipeline row is not a sustained ceiling** and is labelled as
  such. Over interleaved TCP the sender's rate is a property of the socket: the
  harness wrote 300 access units in 26 ms, the pipeline took them in at ~9500/s
  and answered exactly as designed — shedding frames at its bounded queues
  (`frames_dropped: 176`) instead of blocking packet ingest. Its measured window
  is 31 ms long and the decoder was still priming, so `decode_fps` there is not
  a rate; `decode_fps_end_to_end: 109.80` is the only meaningful figure for that
  row. A *sustained* ceiling needs a paced sweep wider than the 3× probe; that
  is a documented follow-up, not a number invented here.

Per-frame decode latency (`DecodedAt − SourceReceivedAt`), available only in the
direct mode because the pipeline exposes a last-frame snapshot rather than a
distribution:

| Direct mode, flooded | value |
|---|---|
| samples | 299 |
| p50 | 8.19 ms |
| p95 | 49.80 ms |
| p99 | 910.98 ms |
| max | 911.19 ms |

The p99 is the tail drain — frames still inside the decoder when the feed
stopped — not a slow decode. This is the same ~0.9 s persistent lag measured
above, seen from the other side.

Decoder priming, measured: **60 source frames / 3934 ms** at 15 fps, with
**time to first decoded frame 3345 ms**; at 3× pacing, 100 frames / 2201 ms and
1123 ms respectively. Direct mode (no RTP) primes in well under a millisecond
because its priming is input-driven and the input is not paced.

## 7. Results — inference (real YOLO, CPU)

| Metric | Value |
|---|---|
| `real_inference_performance_validated` | **true** |
| Worker state | `ready` |
| `device_requested` / `device_effective` | `cpu` / `cpu` |
| Models loaded | `yolo11s-pose.pt`, `yolo11n.pt` (checksums in the JSON) |
| Warm-up (excluded) | 3 inferences: 802.1 ms, 65.9 ms, 62.8 ms |
| Measured samples | 50 |
| **Worker inference** (`worker.Infer`, pre-encoded JPEG) | p50 **58.32 ms**, p95 64.38 ms, p99 67.87 ms → **16.67 inferences/s** |
| **Production edge path** (`vision.Sink.Route`, incl. JPEG encode) | p50 **61.42 ms**, p95 66.19 ms, p99 98.69 ms → **15.97 frames/s** |
| JPEG encode + IPC, p50 estimate | 3.10 ms |
| Inference errors | 0 |

Reading it: on this host, one CPU-only worker runs the two models over one
640×360 frame in ~61 ms end to end, i.e. **~16 frames/s**. The production
sampling default is **5 fps per camera** (`GEOCAM_VIDEO_TARGET_FPS`), so one
camera uses roughly a third of a single worker's throughput here; the k8s-style
`Router` (one worker goroutine per sink, bounded queue, drop-on-full) is what
absorbs the difference, and `frames_dropped` accounts for it.

Two caveats that matter:

- **`detections_total: 0`.** The synthetic `testsrc2` pattern contains no person
  or vehicle above the 0.5 confidence threshold. The model forward pass is the
  same work regardless — YOLO's cost here is bound by input shape, not by how
  many boxes come out — but the detection post-processing path was not exercised
  with real content. Real-footage inference remains to be measured.
- **CPU only.** CUDA is unavailable on this host, so **no GPU number was
  measured**, and a CPU number must never be extrapolated to a GPU (or the
  reverse). `torch_mps_reported: available` is recorded for completeness; the
  worker was deliberately run with `cpu`, and no MPS/TensorRT/RKNN/Hailo backend
  was added or claimed.

## 8. Cloud / Hybrid / Edge

Kept apart, because they are different responsibilities:

- **Cloud** — `GEOCAM_PROCESSING_MODE=cloud` uploads frames to the SaaS and runs
  **no local inference at all** (`internal/cloudsink` is registered instead of
  the vision sink). No inference number above applies to it. Decode still applies:
  the pipeline decodes, samples and JPEG-encodes before uploading.
- **Hybrid** — `ModeHybrid` adds the local motion pre-filter
  (`internal/hybrid`, block-luma diff) and adaptive idle FPS. That is **gating,
  not inference**: it is not YOLO, it produces no detections, and no inference
  FPS is claimed for it. Decode applies unchanged; the filter runs on the
  already-resized Y plane inside the same `readLoop`.
- **Full Edge** — `ModeEdge` is the only mode where `internal/vision.Sink` is
  registered and YOLO actually runs locally. Every inference number in section 7
  came through that real path (`vision.Worker` → `worker.py` → `Ultralytics`),
  with the health handshake verified before any sample was taken.

## 9. `device_requested` vs `device_effective` — a defect found and fixed

Hito X's device audit reproduced a real defect rather than a missing capability:

```
$ python3 -c "from backend import YOLOBackend; ... dev='auto'"
auto -> load ready= True device= auto
    infer FAILED: ValueError Invalid CUDA 'device=auto' requested.
cuda -> load ready= True device= cuda
    infer FAILED: ValueError Invalid CUDA 'device=0' requested.
```

`auto` is a value `internal/fulledge.ParseDeviceMode` accepts and
`internal/agent/fulledge_module.go` falls back to; `cuda` is documented as
supported with a *controlled fallback to CPU* in `internal/fulledge/hardware.go`.
In reality the worker passed the raw string to Ultralytics, so:

- the health handshake reported `ready = true` and `/readyz` passed, while
  **100 % of inferences failed at runtime**;
- `WorkerStatus.device` **echoed the request**, so `/status` could not distinguish
  a resolved device from an unresolved one — on a CUDA box
  `fulledge.HardwareStatus.current_device` could read `cpu` while the worker was
  handed `auto`;
- `hardware.go`'s controlled CPU fallback never reached the worker's `--device`,
  so it was cosmetic.

Minimal fix in this branch, plus regression tests on both sides:

- `deploy/vision-worker/backend.py` gains `resolve_device()`, which mirrors
  `fulledge.HardwareManager.resolve()`'s exact cpu/cuda/auto contract and adds
  no new backend: `auto`/empty → `cuda` if torch reports CUDA, else `cpu`;
  `cuda` without CUDA → `cpu` **with a logged warning**; anything else (an
  explicit `mps`, a CUDA index such as `0`) is passed through untouched. The
  resolved value is what `infer()` uses, and the handshake reports it as
  `device`.
- `HealthResult`/`wireResponse`/`WorkerStatus` now carry **`device_requested`
  alongside `device`**, so an echo can never be presented as a verified
  effective device. `internal/perf` labels the pair explicitly
  (`device_effective_source`).
- Regression tests: `deploy/vision-worker/tests/test_worker.py`
  (`ResolveDeviceTests`, CUDA availability patched so the contract is stated
  without a GPU) and `internal/vision` (`TestWorker_DeviceFields`,
  `TestWorker_EchoedDevice`).

Verified after the fix on the real backend:
`auto -> ready=True requested=auto effective=cpu`, inference succeeds;
`cuda -> ready=True requested=cuda effective=cpu warning="cuda requested but
this host's PyTorch reports no CUDA device; falling back to cpu"`, inference
succeeds.

## 10. Instrumentation gaps found (not fixed here)

These are why some metrics above are single points or absent. They are recorded
rather than papered over:

- **No per-frame decode latency distribution in the pipeline.**
  `PipelineStatus.DecodeLatencyMs` is the *last* frame's value, so a p95/p99 can
  only be produced by bypassing the pipeline (direct mode) or by adding a
  histogram to `internal/processing`.
- **No microsecond-or-slower breakdown of a pipeline restart.** The decoder
  restart path resets `lastFrameAt`/`decoderStartedAt` but exposes no count of
  restarts or of frames lost to one.
- **No sustained-capacity sweep.** The harness can pace at N× real time but the
  benchmark only runs 1× and 3×; the point at which drops begin is not located.
- **`processing.Config.Enabled` is never read inside `internal/processing`**, and
  `rtsp.ErrClosed`/`ErrNoVideoTrack` are declared but never returned — the same
  kinds of dead knob/sentinel Hito W recorded for `internal/rtsp`.
- **`internal/rtsptest` is a simulator, not a camera**: it speaks RTSP/RTP over
  loopback TCP and has no RTCP, no jitter and no loss, so it cannot measure
  network effects — which is exactly why section 3's source is described as
  synthetic rather than "a camera".

## 11. Files

| Path | What it is |
|---|---|
| `internal/perf/` | the harness: clip generation/probing, Annex-B splitting, RFC 6184 packetization, host/device probing, the decode and inference drivers, and the JSON report |
| `internal/perf/localbench_test.go` | the opt-in benchmark entry points (`localbench` + `GEOCAM_PERF=1`) |
| `internal/perf/*_test.go` (untagged) | CI-visible proof that the harness compiles and its counters, packetizer and percentiles work — no ffmpeg, no network, no models |
| `docs/performance/results/*.json` | the machine-readable evidence for this document |
| `deploy/vision-worker/backend.py`, `worker.py` | `resolve_device()` and the `device_requested`/`device` handshake |
| `internal/vision/{protocol,worker}.go` | the Go side of that handshake, surfaced in `WorkerStatus` |

The packetizer is verified against the **production** depacketizer
(`internal/processing.H264Depacketizer`) byte for byte, so the synthetic RTP
stream can never drift from the wire format the Edge actually consumes.
