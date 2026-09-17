# Hito I10 — Edge → Cloud real bandwidth/cost metrics

Telemetry only. No monetary cost, no pricing catalog, no billing — this
document measures **real resources consumed by the Cloud transport**
(`internal/cloudsink`), nothing else.

## What's instrumented

`internal/cloudsink.CloudSink` now tracks, per process (atomic counters,
never a mutex on the hot path — see `internal/cloudsink/cloudsink.go`):

- `frames_encoded`, `jpeg_bytes_generated`, `encode_latency_avg_ms`
- `frames_upload_attempted`, `frames_upload_succeeded`, `frames_upload_failed`
- `jpeg_bytes_uploaded` (only the body of **successful** uploads — a failed
  attempt's body was generated but never delivered, so it does not count)
- `upload_latency_avg_ms`
- `effective_bytes_per_sec`, `effective_frames_per_sec` — a **rolling
  10-second window** (`internal/cloudsink/rate.go`), not a since-process-start
  average. That distinction matters: after the offline buffer (Milestone I6)
  drains a backlog built up during an outage, a since-start average would
  report a rate diluted by the idle outage time, understating real load right
  when it's highest. `since_seconds` still exists as plain uptime, but is no
  longer the denominator of either effective rate.

`jpeg_bytes_uploaded` is deliberately not called `network_bytes`: it is the
JPEG body only. HTTP/TLS overhead is not measured and not estimated — the
spec for this milestone explicitly forbids inventing that number.

## Exposure

Published under `Snapshot.Cloud` (`"cloud"` key) on the existing `/status`
surface (`internal/health.Reporter.SetCloudStatus`), `omitempty` when the
cloud sink is disabled/unconfigured. **Not** added to the SaaS heartbeat
payload (`internal/transport/heartbeat.go` builds its own separate
`HeartbeatSystem`/body structs) — no SaaS contract change, so nothing to
audit there for this milestone.

## Real benchmark

### TC70 → SaaS E2E (preferred path)

`internal/cameratest/cloud_e2e_integration_test.go` (`-tags integration`,
`TestIntegration_CloudFramePush`) now logs the same real counters
(`I10 BENCHMARK: ...`) at the end of a real TC70 → local SaaS run, when
`TAPO_ONVIF_*` / `GEOCAM_E2E_*` credentials and a reachable local SaaS are
available.

**Not run in this environment**: no TC70/SaaS credentials or reachable local
SaaS stack were available. See the local fallback below.

### Local fallback (encode + loopback HTTP)

`internal/cameratest/cloud_local_bandwidth_bench_test.go` (`-tags
localbench`, `TestLocalBandwidthBenchmark`) runs the exact same
`CloudSink.Route` path (real JPEG encode, real `net/http` client/server
round trip over loopback) against an `httptest.Server`, at this repo's
default video config (`config.DefaultVideoTargetFPS=5`,
`DefaultVideoOutputWidth=640`, `DefaultVideoOutputHeight=360`) and
`cloudsink.JPEGQuality=85`.

Run with:

```
go test -tags localbench ./internal/cameratest/ -run TestLocalBandwidthBenchmark -v
```

**Measured 2026-09-17** (this machine, loopback, no real camera/network):

| Metric | Value |
|---|---|
| Duration | 30s |
| Stream | 640×360, JPEG quality 85 |
| Target FPS | 5.0 |
| Frames encoded / upload attempted / succeeded / failed | 150 / 150 / 150 / 0 |
| JPEG bytes uploaded (total) | 34,221,681 |
| Avg bytes/frame | 228,145 |
| Effective bytes/sec | 1,139,977.6 |
| Effective Mbps | 9.120 |
| Effective frames/sec | 5.00 |
| Encode latency (avg) | 16.84 ms |
| Upload latency (avg, loopback) | 0.42 ms |
| Server-side bytes received (cross-check) | 34,221,681 (== uploaded) |

### Limitations — read before using these numbers for I7

1. **Frame content is pseudo-random noise**, not real footage. Random noise
   is close to worst-case for JPEG (entropy defeats DCT compression); real
   camera footage at the same resolution/quality is typically **significantly
   smaller per frame**. Treat 228 KB/frame and 9.12 Mbps as an **upper
   bound**, not a representative real-world figure.
2. **Upload latency is loopback-only** (0.42 ms avg) — it measures HTTP
   client/server/JSON-less body-copy overhead, not real network RTT to a
   deployed SaaS. Do not use it for I7 capacity planning; only the TC70 → SaaS
   E2E path (not run here) gives a real-network number.
3. **No p50/p95** are reported: the runtime counters expose only cumulative
   averages (`encode_latency_avg_ms`, `upload_latency_avg_ms`) by design, to
   keep the hot path allocation-free. A percentile histogram was judged out
   of scope for this milestone — averages were "easy to measure realistically"
   per the I10 goal, percentiles were not.
4. **No multi-camera extrapolation** was performed or is implied here. The
   numbers above are single-camera, single-process.

## I6 offline buffer × I7 bandwidth limiter — post-review fixes

An independent review of the I6/I7/I10 integration (PR #16) found two
cross-milestone bugs, both fixed on the same branch:

- **Oversized buffered frame killing the whole drain loop (P0).** The I6
  drain goroutine replays each buffered frame through the I7 rate limiter's
  `TokenBucket.Wait`. If a frame's JPEG permanently exceeds the currently
  configured burst (`GEOCAM_CLOUD_BURST_BYTES` lowered, or changed across a
  restart while frames were still spooled from before), `Wait` returns a
  non-cancellation error that the drain loop used to treat identically to a
  shutdown signal — silently and permanently stopping *all* replay, not just
  that one frame, until a process restart. It now discards only that frame,
  logs a warning (no JPEG bytes, no secrets), and keeps draining. The buffer
  surface (`VideoPipelineSummary.CloudBuffer` / `CloudBufferStats`) gained a
  new counter for this, kept separate from the pre-existing
  `dropped_buffer_full`:

  ```json
  "cloud_buffer": {
    "dropped_oversize": 0
  }
  ```

- **Starvation between direct traffic and replay (P1).** The direct upload
  path (`Allow`) and the I6 replay path (`Wait`) shared one `TokenBucket`
  with no fairness: sustained direct traffic from other cameras could keep
  consuming tokens as they refilled, indefinitely postponing an
  already-durable buffered frame (never dropping it, just never sending it).
  `TokenBucket` now tracks an in-flight-waiter count; while a replay is
  inside `Wait`, `Allow` yields (`false`, no tokens touched) instead of
  racing it. Priority is held only for the duration of that one `Wait` call
  and released the moment it returns, for any reason — including
  cancellation, so a caller waiting-then-giving-up never leaves direct
  traffic blocked behind it.

## What this gives I7

- **Real cost at quality 85, 5 FPS, 640×360**: ~9.1 Mbps **worst-case**
  (random-content upper bound) sustained per camera, ~228 KB/frame
  worst-case.
- **What has the biggest impact on bandwidth**: resolution and JPEG quality
  dominate `jpeg_bytes_generated` (both are already isolated, fixed knobs —
  see `cloudsink.JPEGQuality` and `config.VideoOutputWidth/Height`); FPS
  scales `effective_bytes_per_sec` linearly for a given frame size.
- **Variability observed**: none captured yet beyond the single 30s local
  run above — a real multi-run TC70 benchmark (varying quality/FPS) is
  needed before I7 can pick a limit with confidence, precisely because
  today's number is a synthetic upper bound, not a footage-representative
  one.
- **I7 must still run the real TC70 → SaaS benchmark** (this milestone wires
  it up and it already logs the same telemetry) before setting any
  commercial/bandwidth limit.

No commercial limit is decided here — that stays I7's call, per this
milestone's scope.
