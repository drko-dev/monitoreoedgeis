# GEO CAM Edge — Physical Validation Register

**This is the single matrix of what still requires the physical world.** It exists
so that a physical gap is never read as pending development, and so that pending
development is never read as a physical gap.

| | |
| --- | --- |
| **Register date** | 2026-09-22 (verified against `origin/main` for GEO CAM closeout) |
| **`origin/main`** | `f4ab26185275a19b80b64aef03737cbc7d5c0bbf` |
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

## 7. NEXT-05 — executable camera-to-audit run sheet

This run sheet extends this canonical physical register; it is not a second
validation record. Execute only against an explicitly approved test/staging
tenant and test camera. Before each test, capture commit/image versions,
device/camera identifiers (non-secret), topology, model/threshold settings and
clock synchronization. Redact credentials, RTSP URLs, customer identifiers and
raw personal evidence from attached logs. Each test row needs a separate
evidence artifact and operator/date. All results below remain `NOT_RUN` until
observed on physical hardware.

| ID | PRECONDITION | ACTION | EXPECTED | METRICS | RESULT | EVIDENCE |
|---|---|---|---|---|---|---|
| E2E-01 | Approved test tenant, Edge identity active, operator and target camera identified | Verify device enrollment and Cloud identity read-only; confirm tenant/site/camera association | Edge reports the intended identity; Cloud derives the same owner without exposing credentials | enrollment status, device ID, organization/site association, credential version (never secret) | NOT_RUN | Redacted `/edge/me` response + operator checklist |
| E2E-02 | Camera powered, network route available, credentials provisioned through the approved secret channel | Probe camera connectivity and authenticate with the configured camera credential | Reachable/authorized camera is distinguished from unreachable or unauthorized | probe latency, result class, camera model/firmware, reconnect counter | NOT_RUN | Redacted probe output and camera model/firmware |
| E2E-03 | RTSP URL is available locally and excluded from report | Start the configured RTSP stream and observe the Edge pipeline | Stream reaches online state and decodes frames; no claim from metadata alone | input/decoded/output FPS, codec, resolution, read failures, reconnects, p50/p95 decode latency | NOT_RUN | Sanitized Edge logs/status snapshot |
| E2E-04 | Controlled real scene with an authorized person present and configured person model | Observe the scene for the documented test window | Person event is generated only if the configured detector actually detects it | inference count/FPS, confidence, inference p50/p95, event latency | NOT_RUN | Event ID, timestamp, sanitized model/result evidence |
| E2E-05 | Controlled real scene with an authorized vehicle present and configured vehicle model | Observe the scene for the documented test window | Vehicle event is generated only if the configured detector actually detects it | inference count/FPS, confidence, inference p50/p95, event latency | NOT_RUN | Event ID, timestamp, sanitized model/result evidence |
| E2E-06 | E2E-04 or E2E-05 produced a real event | Confirm the event is present in the Cloud tenant through its authenticated API/UI | Event identity, camera and time match; no cross-tenant visibility | event creation timestamp, end-to-end event latency, worker persistence count | NOT_RUN | Redacted event view/API response + audit reference |
| E2E-07 | Event has a real capture/clip attached | Open the evidence through authorized SaaS review | Evidence is retrievable, tenant-scoped and corresponds to the event | evidence availability latency, size, content type, checksum, read status | NOT_RUN | Redacted evidence metadata/checksum and review screenshot without unnecessary PII |
| E2E-08 | Authorized reviewer account and test event | Review/acknowledge the event using the supported review flow | Review state persists and actor/action is auditable | review transition, actor role, audit event ID, response latency | NOT_RUN | Review state and matching audit record |
| E2E-09 | Approved false-alarm test scene and documented expected classification | Submit the observed event to the configured false-alarm review process | False alarm is recorded as a review outcome, not silently deleted or relabeled | review outcome, actor, event ID, audit trail | NOT_RUN | Redacted review/audit record |
| E2E-10 | Edge and SaaS available; test telemetry enabled | Observe multiple heartbeat intervals and compare Edge values with Cloud read model | Cloud marks liveness by server arrival time and stores only values the Edge measured | heartbeat age, CPU/RAM/disk/temp, per-camera FPS, packets/bytes, reconnect count | NOT_RUN | Redacted heartbeat and Cloud status snapshots |
| E2E-11 | Test tenant/camera; operator can safely interrupt only the test WAN path | Disconnect WAN/SaaS reachability while leaving camera/Edge LAN connected | Edge processing continues; durable outbox accumulates eligible events without loss or false acknowledgement | outage start/end, outbox pending/in-flight/retry/blocked counts, local event count | NOT_RUN | Edge status and redacted local backlog metrics |
| E2E-12 | E2E-11 outage state recorded | Restore WAN and observe reconnect/flush | Edge reconnects with backoff and flushes buffered events once; Cloud idempotency avoids duplicates | reconnect count, flush duration/FPS, queued/acked/blocked, duplicate conflicts | NOT_RUN | Before/after outbox snapshot, Cloud event IDs and logs |
| E2E-13 | Test Edge appliance and approved maintenance window | Restart only the test Edge service/device using its documented non-destructive procedure | Device returns to ready state; identity, encrypted credentials and durable backlog remain valid | restart duration, readiness/heartbeat time, recovered queue size, errors | NOT_RUN | Service journal excerpt and before/after status |
| E2E-14 | Test Cloud worker replica/process and approved staging access | Restart only the test worker using the staging operator procedure | Worker returns ready; queued/latest-frame processing resumes according to documented semantics | restart duration, readiness, queue depth/drops, inference/event/evidence latency | NOT_RUN | Worker logs and status snapshots |
| E2E-15 | Test tenant and test SaaS endpoint; no customer traffic | Make SaaS unavailable without affecting production | Edge remains operational locally, reports transport failure honestly, and buffers eligible records | outage duration, retry/backoff sequence, outbox growth, resource use | NOT_RUN | Sanitized client logs and backlog snapshot |
| E2E-16 | Test camera can be safely powered off or isolated | Interrupt camera connectivity, then restore it | Camera state becomes offline/degraded, reconnects when restored, and no fabricated frames/events appear | offline detection delay, reconnect count, read failures, recovery time | NOT_RUN | Sanitized camera/Edge status and timestamps |
| E2E-17 | Test-only camera credential can be safely revoked/replaced | Use an intentionally invalid test credential, then restore the correct one | Authentication failure is distinct from network failure; no credential is logged; recovery succeeds after correction | auth-failure state, retry behavior, reconnect count, recovery time | NOT_RUN | Redacted error category and corrected recovery state |
| E2E-18 | Supported remote-config version and harmless test setting chosen | Apply one approved, reversible test setting and observe acknowledgement | Edge applies or safely rejects the version; Cloud reports the exact terminal status | desired/applied version, apply latency, status/error code, rollback state | NOT_RUN | Redacted config version and ACK/audit record |
| E2E-19 | Supported non-destructive command (e.g. request status/rediscovery) queued for test device | Poll, execute and report the command | Command is claimed once and reaches a terminal result; unsupported commands fail safely | poll/execute/report latency, command state, retries, audit event | NOT_RUN | Command ID and sanitized report/audit entry |
| E2E-20 | Signed, approved test release; rollback plan and isolated test Edge; explicit operator authorization | Exercise OTA only if the exact artifact/signature/rollback path is safe for the test unit | Signature/checksum/compatibility checks pass, update is observable, and rollback is available | version before/after, verification result, update duration, restart/readiness, rollback result | NOT_RUN | Release digest/signature verification (no secret), version/status logs |

### End-to-end chain and exit rule

```text
CAMERA → RTSP → EDGE / CLOUD WORKER → YOLO → EVENT → EVIDENCE → SAAS → REVIEW → AUDIT
```

Report each ID independently. `PASS` requires the expected state plus the
listed measured metrics and attached evidence. `NOT_RUN` is not failure and is
not pass. Do not run destructive purge/offboarding, change production Legal
Hold, or use OTA on a production unit as part of this physical test sheet.

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
