# Network measurement and capacity evidence matrix

Scope: Hito X network instrumentation plus the evidence format used to consolidate camera-scale, decode and inference measurements. This document does not certify hardware or camera limits.

## What the traffic meter measures

`internal/transport.TrafficMeter` is optional and thread-safe. When installed on a transport client it records only application payload bytes presented to or read from `net/http`, plus request counts.

Stable categories:

- `heartbeat`
- `control`
- `discovery`
- `frames`
- `events`
- `evidence`
- `ota`

The meter never stores payloads, headers, tokens, credentials, device IDs or URLs containing secrets.

### Important accounting boundary

`bytes_sent` and `bytes_received` are **application payload bytes**. They are not Ethernet/IP/TCP/TLS/HTTP wire bytes. Header bytes, TLS framing, retransmissions and lower-layer overhead are intentionally **NOT MEASURED** rather than estimated.

A request that is handed to `http.Client.Do` increments `requests` and its request payload count even if the request later fails. This is an application-attempt metric, not proof that every byte reached a remote NIC.

Do not mix these counters with storage-byte accounting.

## Cloud / Hybrid / Edge interpretation

- **Cloud:** frame-upload bytes normally dominate when sampled frames are sent to SaaS.
- **Hybrid:** any reduction from gating/substream/FPS policy must be measured on a real run before stating a percentage.
- **Edge:** local inference does not itself imply zero network traffic; control, heartbeat, event metadata and evidence may still be sent.

No mode-to-mode reduction percentage is asserted by Hito X without a measured run.

## Capacity evidence rows

`internal/performance.CapacityRow` has exactly three evidence states:

- `MEASURED` — numeric values came from an identified execution and require traceable run evidence.
- `DERIVED` — values are calculations over identified source evidence and must include the derivation.
- `NOT_VALIDATED` — no benchmark claim was established. Numeric benchmark fields are rejected for these rows so placeholders cannot look measured.

Numeric fields are pointers in Go and omitted when unavailable. Missing data must never be serialized as a fabricated zero.

### Traceability

A measured row records enough evidence to reproduce or audit the run:

- date/time;
- commit SHA;
- hardware ID;
- OS;
- architecture;
- relevant configuration;
- input/source;
- exact command;
- result path or artifact when one exists.

## Matrix shape

The schema is intentionally small and versionable. Depending on which Hito X block produced the evidence, a row may contain:

- hardware / architecture / CPU / cores / RAM / GPU;
- processing mode;
- resolution and input FPS;
- camera count;
- decode FPS;
- inference FPS;
- CPU percent;
- RAM usage;
- application network bytes per second;
- duration;
- evidence and notes.

IA1 provides camera-count/resource measurements. IA2 provides decode/inference measurements. This block provides network counters and the common evidence semantics; it does not duplicate either benchmark.

## Example: descriptive, not validated

```json
{
  "status": "NOT_VALIDATED",
  "hardware_id": "candidate-arm64-sbc",
  "architecture": "arm64",
  "notes": ["physical hardware NOT VALIDATED"]
}
```

Adding `camera_count`, `decode_fps`, `inference_fps`, CPU/RAM measurements, network rate or duration to that row would fail validation because it would make an untested row look quantitative.

## Example: measured run

```json
{
  "status": "MEASURED",
  "hardware_id": "github-runner-amd64",
  "camera_count": 5,
  "duration_seconds": 3,
  "evidence": {
    "date": "2026-09-19T20:00:00Z",
    "commit_sha": "<exact SHA>",
    "hardware_id": "github-runner-amd64",
    "os": "linux",
    "arch": "amd64",
    "command": "GEOCAM_PERF=1 ..."
  }
}
```

That row proves only what its run exercised. A GitHub-hosted runner is not a certified appliance.

## Hardware/camera matrix rules

Never fill a matrix cell such as `Raspberry Pi 5 = 10 cameras` or `mini-PC = 25 cameras` without a benchmark on that physical target under the stated video/inference workload.

Cross-build, QEMU and synthetic loopback tests remain useful engineering evidence but are not physical-hardware capacity validation.

Projections are allowed only as `DERIVED` rows with an explicit formula and source evidence; they must not be displayed or described as `MEASURED`.

## Current Hito X state for this block

- application-payload network instrumentation: implemented and testable;
- evidence/matrix schema: implemented and validated;
- measured network capacity runs: none in this block;
- derived capacity runs: none in this block;
- physical ARM64 capacity: `NOT_VALIDATED`;
- camera-limit certification: `NOT_VALIDATED`.
