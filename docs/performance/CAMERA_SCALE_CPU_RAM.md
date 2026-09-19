# Camera scale — CPU/RAM benchmark

Scope: Hito X, camera-scale + CPU/RAM resource benchmark block only. Decode
FPS, inference FPS, commercial hardware sizing, and network capacity
estimation are explicitly out of scope for this document and this harness.

## What this measures

`internal/perf` drives a real `internal/rtsp.Manager` (the same production
RTSP supervision code the Edge agent uses) against N simulated cameras built
with the reusable `internal/rtsptest.Simulator` from Hito W — no new mock,
no physical camera, no Internet dependency. For the configured duration it
samples the Edge process's own resource usage and the manager's real stream
counters, then reports a single JSON result plus human-readable log lines.

This is a **synthetic, single-process, single-host** benchmark. It does not
run decoding, inference, or real network transport to physical cameras.

## MEASURED / DERIVED / NOT VALIDATED

- **MEASURED** (read directly from the OS or the running process during a
  run): `goroutines_*`, `rss_*_bytes` (Linux `/proc/self/statm` only),
  `fd_*` (Linux `/proc/self/fd` only), `packets_received`, `bytes_received`,
  `errors`, `camera_active`, `final_state_counts`, `duration_seconds`
  (wall-clock elapsed, not the requested duration).
- **DERIVED** (arithmetic over measured values, computed by the harness,
  never sampled directly): `cpu_seconds_used` (delta of two
  `getrusage(RUSAGE_SELF)` reads, user+sys time), `throughput_packets_per_second`.
- **NOT VALIDATED by this benchmark**: any claim about how many physical
  cameras a given piece of hardware can support in production. A CI runner
  or a developer laptop running 50 synthetic TCP simulators on loopback says
  nothing about real network conditions, real H.264/H.265 bitstreams, real
  camera reconnect behavior, or contention with the video pipeline's actual
  decode/inference load. Only field measurement on target hardware with real
  cameras validates capacity.

## CPU and RAM: what the numbers actually mean

- `cpu_seconds_used` is process **user+system CPU time** consumed by the Go
  test process during the run (`getrusage`), not wall-clock time and not a
  percentage. It includes the harness's own overhead (spinning up N
  `rtsptest.Simulator` TCP listeners), not just the manager's supervision
  loops — there is no isolated "manager-only" measurement.
- `rss_*_bytes` is OS-level **resident set size** of the whole test process
  (`/proc/self/statm`), i.e. total physical memory the kernel accounts to
  it. It is **not** the same thing as Go heap size
  (`runtime.MemStats.HeapAlloc`) — RSS includes the Go runtime, stacks,
  simulator buffers, and everything else in the process, and can differ
  substantially from heap-only figures.
- On non-Linux platforms (e.g. macOS during local development) `rss_*_bytes`
  and `fd_*` are reported as `-1`, meaning **UNAVAILABLE**, never `0`. Do not
  interpret `-1` as "zero usage."
- `goroutines_*` come from `runtime.NumGoroutine()` and are always available.

## Running it

### CI smoke (always runs, cheap)

`TestScaleSmoke` in `internal/perf/scale_test.go` runs unconditionally as
part of `go test ./...` (1 and 5 simulated cameras, ~300ms each). It exists
only to prove the harness itself works, not to produce capacity data.

### Manual / full benchmark (opt-in, one profile per run)

```bash
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=<1|5|10|25|50> GEOCAM_PERF_DURATION=<duration> \
  go test ./internal/perf/... -run TestScaleFull -v
```

Example, run once per profile to cover the required scenarios:

```bash
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=1  GEOCAM_PERF_DURATION=30s go test ./internal/perf/... -run TestScaleFull -v
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=5  GEOCAM_PERF_DURATION=30s go test ./internal/perf/... -run TestScaleFull -v
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=10 GEOCAM_PERF_DURATION=30s go test ./internal/perf/... -run TestScaleFull -v
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=25 GEOCAM_PERF_DURATION=60s go test ./internal/perf/... -run TestScaleFull -v
GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=50 GEOCAM_PERF_DURATION=60s go test ./internal/perf/... -run TestScaleFull -v
```

The test prints a `PERF_RESULT_JSON:` block (machine-readable) and a set of
human-readable `t.Logf` summary lines. `TestScaleFull` fails if any camera
never reaches an active/online state, which is the harness's own
determinism guarantee, not a hardware capacity signal.

Every run: uses dynamic TCP ports, cleans up all simulators and the manager
before returning, requires no root, no Internet, and no real credentials.

## Sample results (development laptop, macOS, synthetic — NOT hardware sizing data)

| cameras | duration | cpu_seconds_used | goroutines peak | errors | camera_active |
|---------|----------|-------------------|------------------|--------|----------------|
| 1       | 0.3s     | 0.006             | 9                | 0      | 1              |
| 5       | 0.3s     | 0.009             | 25               | 0      | 5              |
| 10      | 3s       | 0.064             | 44               | 0      | 10             |
| 50      | 5s       | 0.227             | 204              | 0      | 50             |

`rss_*_bytes` and `fd_*` were `-1` (UNAVAILABLE) on this platform because it
is not Linux — re-run on a Linux target to get real RSS/FD numbers. These
figures come from this repository's development machine, not from the Edge
appliance's target hardware, and must not be read as a capacity
recommendation.
