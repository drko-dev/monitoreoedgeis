# Hito W — Multi-arch and soak validation

This document records the scope and evidence for W10 (ARM64), W11 (AMD64), and W12 (soak testing).

## W10 — ARM64

Validation is intentionally split into distinct evidence levels:

- **Cross-build validated:** `linux/arm64` Go artifact is built with `CGO_ENABLED=0`.
- **Artifact architecture validated:** CI checks the ELF header and `go version -m` reports `GOARCH=arm64`.
- **Appliance package validated:** the release archive contains the expected `geocam-edge`, `VERSION`, and `ARCH` files, and the sibling SHA-256 file verifies from the directory containing the artifact.
- **Container build path validated:** CI exercises the Dockerfile Go build stage for `linux/arm64` through Buildx/QEMU.
- **Runtime emulation:** limited to the container build path above; it is not treated as physical-hardware evidence.
- **Physical ARM64 hardware:** **NOT VALIDATED** by Hito W.

Do not reinterpret successful cross-build or QEMU work as Raspberry Pi, Orange Pi, or other real-device validation.

## W11 — AMD64

CI validates:

- `linux/amd64` Go cross-build;
- ELF metadata;
- `go version -m` with `GOARCH=amd64`;
- appliance package layout and checksum;
- existing full Docker amd64 smoke on GitHub-hosted amd64 hardware, including non-root startup and the production H.264 decode command.

## W12 — Soak

The soak harness lives at:

`internal/soak/soak_test.go`

It is deliberately opt-in so ordinary `go test ./...` remains fast and deterministic.

Run manually:

```bash
GEOCAM_SOAK=1 GEOCAM_SOAK_DURATION=15m \
  go test ./internal/soak -run '^TestSoak$' -count=1 -v
```

Race-enabled:

```bash
GEOCAM_SOAK=1 GEOCAM_SOAK_DURATION=15m \
  go test -race ./internal/soak -run '^TestSoak$' -count=1 -v
```

CI runs a short smoke-duration soak to ensure the harness itself remains executable.

The harness exercises existing production components rather than implementing a benchmark:

- heartbeat lifecycle and repeated sends;
- the existing bounded Cloud frame buffer;
- bounded-full/drop behavior;
- queue draining;
- graceful shutdown.

It records:

- actual duration;
- cycles;
- heartbeats;
- goroutines start/final/peak;
- queue depth/capacity/drop/replay figures;
- Linux file-descriptor count when `/proc/self/fd` is available;
- Linux RSS when `/proc/self/statm` is available.

Unavailable process metrics are reported as unavailable, not as zero.

Acceptance is stability-oriented:

- no panic;
- no deadlock;
- no race under `go test -race`;
- bounded queue;
- clean drain;
- no clearly unbounded goroutine or file-descriptor growth.

## Explicit non-claims

Hito W does **not** establish:

- maximum camera capacity;
- 1/5/10/25/50-camera performance;
- CPU/RAM commercial minimums;
- inference FPS capacity;
- network throughput limits;
- certified hardware;
- physical ARM64 appliance validation.

Those belong to later performance/capacity and hardware-validation work.
