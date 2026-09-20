# GEO CAM Edge — Physical Validation Register

**This is the single matrix of what still requires the physical world.** It exists
so that a physical gap is never read as pending development, and so that pending
development is never read as a physical gap.

| | |
| --- | --- |
| **Register date** | 2026-09-20 (register content) / verified against `main` again for Hito 2A |
| **`main`** | `6617322549e4d9ac815317a0724b92d3e4613045` |
| **SOFTWARE 1.0** | **READY** — see `docs/product/RELEASE_1_0_READINESS.md` |
| **FIELD / HARDWARE VALIDATION** | **PENDING** — every row below is `NOT_VALIDATED` |

## Governing rule

No board, vendor, model, RAM figure, storage figure, FPS number, bandwidth figure,
accuracy figure, camera-count capacity or cost is **certified, estimated,
extrapolated or invented** by this register. Where a number does not exist, the
cell says `NOT ESTABLISHED` or the row says `NOT_VALIDATED`. A physically
unexecuted item is recorded as `NOT_VALIDATED`, which is a statement about
evidence, never a `FAILED` result.

## How to read the columns

| Column | Meaning |
| --- | --- |
| **SW implemented** | The code/packaging exists in `main` today. This is a statement about the repository, not about any device. |
| **Local / simulated tested** | Exercised by the test suite, CI, simulators or deterministic fault injection. `YES` here never implies physical behaviour. |
| **Physical validated** | Executed on real hardware / real cameras / real network. |
| **Evidence required to close** | The artifact that must exist in this repository before the row can change. |
| **Owner / environment** | Who and what is needed. **None of these is registered in this repository today.** |

`PARTIAL` marks a row where only one of several sub-claims is covered. It is not
a softer `YES`.

---

## 1. Real camera / field

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **ONVIF against a real device** | YES — `internal/discovery/onvif`, `internal/cameracreds`, `internal/cameratest` | YES — `onvif/wssecurity_test.go`, `cameratest/credentials_failure_test.go`; loopback `httptest` only, and `UNREACHABLE` is distinguished from `INVALID` | **NO — NOT_VALIDATED** | Vendor / model / firmware named, plus discovery and credential-provisioning output from the real device | Field operator; real camera or DVR/NVR on the target subnet |
| **RTSP against a real stream** | YES — `internal/rtsp` | YES — `internal/rtsp/failure_lifecycle_test.go` against test-local RTSP servers | **NO — NOT_VALIDATED** | Per-camera state plus `input_fps` / `decoded_fps` / `output_fps` recorded from real streams | Field operator; real camera, real codec |
| **Reconnection under real faults** | YES — supervisor with bounded exponential backoff; states `connecting` / `online` / `degraded` / `auth_failed` / `offline` | YES — TCP refused, RTSP EOF/peer close, packet silence and auth rejection are simulated; Y5 recovery via `Manager.SetTargets` | **NO — NOT_VALIDATED** | Reconnect and timeout counters across a real link flap and a real camera power cycle | Field operator; real camera + real network path |
| **Real scenes** | YES — decode and detection pipeline exists; **no accuracy claim is made anywhere in this repository** | **NO** — no scene validation exists; the simulators carry no real imagery | **NO — NOT_VALIDATED** | Pilot evidence record over real scenes (motion, lighting, occlusion). Any accuracy or detection-quality figure must come from that run, not from this document | Field operator; real scenes with documented conditions |
| **Real network (WAN)** | YES — transport with bounded backoff, `Retry-After` honoured | YES — Y3/Y4 SaaS-offline and Internet-loss behaviour against `httptest`/fake SaaS | **NO — NOT_VALIDATED** | Real WAN egress, latency and SaaS reachability during a real run | Field operator; production WAN + real SaaS endpoint |
| **Real Hybrid bandwidth reduction** | YES — byte/sec and FPS controls with motion gating exist | PARTIAL — the only figure ever produced (`66.00%`) comes from a **synthetic** run with an injected `i%3==0` selector that never constructs a pipeline or a `MotionDetector`; it must not be quoted | **NO — NOT_VALIDATED** | Measured egress on the same real cameras and scenes, with and without the selector | Field operator; real cameras + a real SaaS endpoint to measure against |
| **Real SaaS + appliance field end-to-end** | YES — enrollment, heartbeat, control channel, event/evidence upload, remote config and OTA are implemented | YES — against a fake/local SaaS only | **NO — NOT_VALIDATED** | One end-to-end field run against the real SaaS: enroll, stream, upload, apply remote config, run an OTA, recover | Field operator; real appliance + production SaaS |

**Hito Y closed the simulated half honestly**: `docs/testing/failure-lifecycle-y.md`
records that the simulators bind only to loopback and that no real camera or
Internet is used in CI. That statement remains true.

---

## 2. Physical ARM64

The distinction that matters: **cross-build, packaging and QEMU emulation are not
physical ARM64 evidence.** `docs/TESTING_MULTIARCH_SOAK.md` already states this
("Do not reinterpret successful cross-build or QEMU work as Raspberry Pi, Orange
Pi, or other real-device validation"), and
`docs/product/HARDWARE_CERTIFICATION.md` §9 classifies QEMU ARM64 build success
as `INVALID as evidence`.

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **Real ARM64 appliance (Linux)** | YES — `CGO_ENABLED=0` cross-build; ELF header and `GOARCH=arm64` asserted; multiarch release artifacts | YES — Buildx/QEMU Docker Go stage and container smoke | **NO — NOT_VALIDATED** | A certification record per `HARDWARE_CERTIFICATION.md` §6 for one named unit | Owner of a physical ARM64 board; Linux appliance image |
| **Installation on the unit** | YES — `deploy/appliance/scripts/install.sh`, `package.sh` | YES — `deploy/appliance/{appliance,package,bootstrap,ota_upgrade_lifecycle}_test.go` | **NO — NOT_VALIDATED** | Install log, installed layout and `geocam-edge version` from the unit | Owner of the unit; root on a clean Linux ARM64 install |
| **ffmpeg on the appliance** | YES — B10 builds static ffmpeg for amd64/arm64; packaging is fail-closed when ffmpeg is absent | PARTIAL — `package-ffmpeg-fail-closed` CI job proves the packaging mechanics with a stub binary; it deliberately does **not** cross-compile a real ffmpeg on every PR | **NO — NOT_VALIDATED** | `ffmpeg -version` and decode `PASS` on the unit | Owner of the unit |
| **Vision worker / Python runtime** | YES — B2 ships the worker sources into both release artifacts; `check-vision-runtime.sh` fails closed when absent | PARTIAL — the `python-vision-worker` CI job runs the unittest suite with `FakeBackend` and explicitly **no torch, no ultralytics, no CUDA and no model** | **NO — NOT_VALIDATED** | Real `python` / `torch` / `ultralytics` versions and a real model load on the unit | Owner of the unit; provisioned Python + externally supplied weights (SHA-256 recorded) |
| **Soak on the unit** | YES — `internal/soak`, gated by `GEOCAM_SOAK=1` | YES — CI smoke duration, plus manual 15m runs plain and under `-race` | **NO — NOT_VALIDATED** | Soak record (duration, cycles, goroutines, FDs, RSS) measured on the unit | Owner of the unit; a long-running window |
| **Reboot on the unit** | YES — systemd unit, `Type=notify` | YES — Y1 restart lifecycle tests (simulated) | **NO — NOT_VALIDATED** | Clean reboot and unclean power-cut results from the unit | Owner of the unit; physical power control |

### Operational precondition (not a software blocker)

`GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES` defaults to **4** (range 1–16) and the
manager simply does not admit cameras beyond it. A 5–10 camera run therefore
requires raising this knob, and the record must state the value used — otherwise
a run silently measures 4 cameras while appearing to measure 10.

### Note on the development host

The committed benchmark artifacts under `docs/performance/results/` were produced
on the development machine: `"goos": "darwin"`, `"goarch": "arm64"`, Apple M4,
macOS, with `"torch_cuda_reported": "unavailable"`. That is physical ARM64
silicon, but **not a Linux appliance target**, and it is not a target-hardware
record. It must not be cited as physical ARM64 validation.

---

## 3. CUDA / GPU

`HARDWARE_CERTIFICATION.md` §4 governs this section: there is **no bespoke
GPU/NPU inference backend**. No CUDA, TensorRT, RKNN or Hailo integration exists
in `deploy/vision-worker/backend.py`. The `cuda` device value is *requested* and
delegated to Ultralytics/PyTorch, and is silently reduced to CPU when
unavailable. `docs/deployment/hardware.md` treats a board as CPU-only for
sizing, and that remains the correct posture.

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **Real GPU driver stack** | **NO** — no accelerator backend exists to exercise | **NO** | **NO — NOT_VALIDATED** | A record naming the driver version whose `vision.worker.device` reports `cuda` | Owner of a GPU host; vendor driver stack |
| **Device permissions (`/dev/nvidia*`)** | N/A — no global `PrivateDevices=true` was introduced precisely because it can block `/dev/nvidia*`; no repository code opens the device itself | **NO** | **NO — NOT_VALIDATED** | Recorded device access on the unit, with the worker confirming `cuda` | Owner of a GPU host |
| **`torch.cuda.is_available()`** | YES — `backend.py` `_cuda_available()` delegates to `torch.cuda.is_available()` | YES, **SIMULATED ONLY** — `test_worker.py` monkeypatches `_cuda_available`; no real GPU is involved | **NO — NOT_VALIDATED** | Real `torch` version plus a worker-confirmed `cuda` device | Owner of a GPU host |
| **Real model load / inference** | YES — `worker.py` / `backend.py` | **NO** — CI runs with `FakeBackend` and deliberately excludes torch, ultralytics and model downloads | **NO — NOT_VALIDATED** | Real load, plus inference FPS and latency measured on the unit | Owner of a GPU host; external weights (SHA-256 recorded) |
| **Watchdog / startup timing vs. the 30s readiness budget** | YES — `internal/agent/watchdog.go`, `internal/systemd`, worker start timeout (default 30s) | PARTIAL — the watchdog is unit-tested and the datagram asserted; timing has only been observed on the M4 dev host | **NO — NOT_VALIDATED** | Wall-clock time-to-ready from the unit (`HARDWARE_CERTIFICATION.md` §5 step 5) | Owner of the unit; this is the known Full Edge risk |

A CUDA row may only be added to a certification record after a physical run on a
host whose driver stack is named in that record. That has never happened here.

---

## 4. Pilot 5–10 cameras

Plan: `docs/product/PILOT_5_10_CAMERAS.md` (Hito Z3).

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **Plan exists** | YES — executable plan, pre-flight checklist, evidence format | N/A | N/A — a plan is `DOCUMENTED`, not a validation | Nothing; the plan is complete and executable | N/A |
| **Instrumentation to fill the plan** | YES — reused from Hito X (`internal/performance`, `internal/soak`) and Hito Y (`internal/rtsp` simulators, `internal/edgebacklog` fault injection); no new benchmark was written | YES — synthetic loopback scale runs | **NO — NOT_VALIDATED** | The filled evidence record (see below) | Pilot owner |
| **Physical execution (5–10 real cameras)** | YES — the software can run it | **NO** — only synthetic loopback scale runs exist | **NO — NOT_VALIDATED — actual pilot executed: NO** | A completed record in the plan's exact format: date, commit, hardware, OS, arch, camera model/count, resolution, FPS, codec, mode, duration, CPU, RAM, network, decode, inference, drops, reconnects, errors, result | Pilot owner; 5–10 real cameras + appliance + network |
| **Results** | — | — | **NO — NOT_VALIDATED** | The same record, with `NOT_VALIDATED` in every field that was not actually measured | Pilot owner |

No pilot run has produced that record. Until one does, any field of a would-be
results table is `NOT_VALIDATED`, and no placeholder number may be substituted.

---

## 5. Hardware certification

Protocol: `docs/product/HARDWARE_CERTIFICATION.md` (Hito Z7).

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **Protocol exists** | YES — §5 steps 0–8, plus the §6 record format | N/A | N/A — a protocol is `DOCUMENTED`, not a validation | Nothing; the protocol is complete | N/A |
| **Instrumentation the protocol relies on** | YES — `internal/platform` host metrics, `/status` `resources`, the `geocam-perf/hito-x/1` JSON schema with `MEASURED` / `DERIVED` / `NOT_VALIDATED` row enforcement | YES — reused from Hito N/X, gated behind `localbench` + `GEOCAM_PERF=1` | **NO — NOT_VALIDATED** | A real `MEASURED` run on the target unit | Owner of the unit |
| **Any hardware certified** | N/A | N/A | **NO — NOT_VALIDATED — zero units certified** | At least one §6 record for a named unit, produced by a physical §5 run, including `result: certified` and an explicit statement of what it does not certify. `docs/deployment/certifications/` **does not exist** | Owner of the unit |
| **RAM / storage minimums** | N/A | N/A | **NOT ESTABLISHED** — `docs/deployment/hardware.md` states the figures are provisional provisioning assumptions, not validated minimums | Real measurement on named units | Owner of the unit |

Certification is **per unit, not per class**: a certified ARM64 board does not
certify "ARM64", and changing the camera count, mode, RAM/storage or OS/driver
set invalidates the previous record.

---

## 6. Y2 / Y9 — physical power loss and real PID-1 watchdog

Both rows were closed as `PARTIALLY_VALIDATED` by Hito Y, and that classification
stands. These are the two remaining gaps Hito Y explicitly recorded and did not
paper over.

| Item | SW implemented | Local / simulated tested | Physical validated | Evidence required to close | Owner / environment |
| --- | --- | --- | --- | --- | --- |
| **Y2 — physical power loss** | YES — fsync-before-rename plus ENOSPC/EDQUOT classification on every durable write (identity, credentials, cameracreds, edgebacklog) | YES — deterministic abrupt-termination proxies (leftover `.tmp` files, injected rename failures). **Not a real power cut** | **NO — NOT_VALIDATED** | A real power cut on the unit, then verification that identity, credentials and the backlog survive intact | Owner of the unit; physical power control |
| **Y9 — real PID-1 watchdog kill/restart** | YES — `internal/systemd` `sd_notify`, `Type=notify`, `WatchdogSec=60`, gated on a real liveness probe; internal watchdog in `internal/agent` | PARTIAL — datagram assertion plus unit tests; timer cancellation on `STOPPING=1` and `NotifyAccess=main` are static unit assertions | **NO — NOT_VALIDATED** — no test has run on a host where systemd is PID 1 | A real PID-1 watchdog expiry producing a kill/restart cycle, with `StartLimitBurst` behaviour observed | Owner of the unit; a real systemd PID 1 |

Also carried as `NOT_VALIDATED` from the same milestone: real appliance
filesystem-exhaustion/quota behaviour (Y6 used injected ENOSPC/EDQUOT, a
deterministic seam).

---

## Summary

| Axis | Status |
| --- | --- |
| **SOFTWARE 1.0** | **READY** — functional blockers B1–B8 and B10 closed; the final integration gate passed and merged into `main` |
| **RELEASE 1.0** | **NOT_VALIDATED** — B11: no real signed `v1.0.0` release has been executed |
| **DEPLOYED PROD** | **NO / NO TARGET REGISTERED** |
| **FIELD / HARDWARE VALIDATION** | **PENDING** — sections 1–6, every physical row `NOT_VALIDATED` |

**SOFTWARE 1.0 = READY. FIELD / HARDWARE VALIDATION = PENDING.**

Physical validation is **not** a software blocker, and its absence does not
silently reopen a blocker that was implemented and tested locally. Conversely,
"the software exists" never converts a `NOT_VALIDATED` physical row into a
passing one.

## Software blockers found while normalizing this register

No new **functional** software blocker was found. What was found is
documentation drift in `docs/product/HARDWARE_CERTIFICATION.md` (Hito Z7), which
had begun to describe closed software work as still missing. That conflation is
precisely what this register exists to prevent, so the stale claims were
corrected in place:

| Defect | Was | Now |
| --- | --- | --- |
| §2 claimed "The Python Vision Worker is not exercised by CI at all. There is no Python job in `.github/workflows/ci.yml`" | Stale — B6 closed | `.github/workflows/ci.yml:132` runs `python-vision-worker` |
| §2 claimed "The appliance package does not ship the Vision Worker" | Stale — B2 closed | `deploy/appliance/scripts/package.sh:107-109` stages it, `install.sh:141-144` installs it, `check-vision-runtime.sh` fails closed |
| §5 step 8 and §9 claimed no retention policy exists for `events/` or `evidence/` and that the free-disk gate misses MP4 clips | Stale — B3 closed | `internal/fulledge/retention.go` and `internal/evidence/clips.go` exist; the **physical** at-capacity validation remains `NOT_VALIDATED` |

These were documentation defects only. No production code was changed and no
feature was developed to normalize this register.

## What this register deliberately does not do

- It does not certify hardware, and it cannot: `HARDWARE_CERTIFICATION.md` §10
  governs closing Z7, and nothing here substitutes for a physical run.
- It does not convert a `NOT_VALIDATED` row into a `FAILED` row. Nothing in
  sections 1–6 has been executed and failed; it has simply not been executed.
- It does not publish capacity, throughput, accuracy, bandwidth, cost or RAM
  figures. `HARDWARE_CERTIFICATION.md` §7 records which existing numbers are
  synthetic or platform-mismatched, and none of them is usable for certification.
- It does not duplicate the pilot plan or the certification protocol; it points
  at them.
