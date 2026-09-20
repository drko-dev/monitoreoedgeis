# GEO CAM Edge — Release 1.0 Readiness (Hito Z6)

## Scope of this document

Z6 defines what **1.0** means for GEO CAM Edge, states where the software
actually stands against that definition, and records the release mechanism that
already exists and is already exercised by CI. It does not produce a release,
does not create a tag, and does not certify anything.

Three different things get called "1.0" in conversation. This document keeps
them apart, using the labels `AGENTS.md` already defines:

| Term | Meaning | Who decides it |
| --- | --- | --- |
| **SOFTWARE 1.0** | The integrated `main` passes the full software gate and no functional blocker remains open. | Code + CI + the open blockers in §5 |
| **RELEASE 1.0** | A signed `v1.0.0` GitHub Release exists with verifiable artefacts. | Requires SOFTWARE 1.0, a real `OTA_SIGNING_KEY`, and a tag |
| **DEPLOYED PROD** | A named production appliance/VM is running a released version. | Requires a real production target, which does not exist yet |

Nothing in this document should be read as moving any of the three forward.
**No `v1.0.0` tag or release exists today**, and the only version embedded in the
tree is the placeholder `0.1.0`.

## 1. Where 1.0 stands right now

| Component | Status | Evidence |
| --- | --- | --- |
| Version embedding and reporting | IMPLEMENTED / TESTED | `internal/agent/version.go`, `Makefile` `VERSION`/`LDFLAGS`, `geocam-edge version` |
| Reproducible build (`-trimpath`, embedded version/commit/date) | IMPLEMENTED / TESTED | `Makefile` `build`/`build-linux` |
| Dual-architecture build (linux/amd64, linux/arm64) | IMPLEMENTED / TESTED | `Makefile` `build-linux`, CI `Cross-build deployment targets` |
| Appliance packaging (`package.sh`) | IMPLEMENTED / TESTED | `deploy/appliance/scripts/package.sh`, `deploy/appliance/*_test.go` |
| Tag-driven signed release workflow | IMPLEMENTED | `.github/workflows/release.yml` (never yet triggered by a real tag) |
| OTA signature sign + verify tooling | IMPLEMENTED / TESTED | `cmd/geocam-edge` `ota sign` / `ota verify`, `internal/ota` |
| CI software gate | IMPLEMENTED / exercised on every PR | `.github/workflows/ci.yml` |
| **Functional blockers closed** | **NO — see §5** | |
| **Physical validation complete** | **NO — see Z7** | `docs/product/HARDWARE_CERTIFICATION.md` |
| **A production target exists** | **NO** | No appliance/VM is registered anywhere in the repo |

## 2. The release mechanism that already exists

This is not a proposal; it is what a `v*.*.*` tag already triggers.

`.github/workflows/release.yml` runs on `push` of a tag matching `v*.*.*`, with
`contents: write`, and:

1. **Requires the signing key, fail closed.** If the `OTA_SIGNING_KEY` secret is
   unset, the job fails *before* anything is published: "refusing to publish an
   unsigned release." There is no unsigned and no checksum-only path.
2. **Packages the full appliance artefact per architecture** by reusing
   `deploy/appliance/scripts/package.sh` — the same producer the local build
   uses, deliberately not a second, reduced tarball format.
3. **Generates `SHA256SUMS`** over the `.tar.gz` artefacts.
4. **Signs it** with an Ed25519 detached signature: `go run ./cmd/geocam-edge ota
   sign -sha256sums dist/SHA256SUMS -private-key <tmp> -out
   dist/SHA256SUMS.sig`, with the key material written to a `umask 077` temporary
   file and removed by a trap.
5. **Creates the GitHub Release** attaching `*.tar.gz`, `*.sha256`,
   `SHA256SUMS` and `SHA256SUMS.sig`.

Versioning is tag-driven (`docs/RELEASING.md`): `vX.Y.Z` is the source of truth,
there is no `VERSION` file, and the tag is passed through as
`internal/agent.Version` via `-ldflags`.

Verification on the device side is fail-closed too: `GEOCAM_OTA_PUBLIC_KEY_FILE`
must be provisioned out of band, and an unset or unreadable key rejects every
candidate release rather than falling back to a checksum-only check.

**Consequence for 1.0:** publishing `v1.0.0` is a *configuration and
authorization* step (a real `OTA_SIGNING_KEY` plus a tag), not an engineering
one. The engineering work that remains is in §5.

> Correction carried in this delivery: `docs/RELEASING.md` still lists
> "Signing / provenance attestation" under "Out of scope (later hitos)". That
> is stale — signing is implemented and wired fail-closed as described above.
> The document is corrected to match the workflow.

## 3. The software gate, exactly as it runs

Every command below is verbatim from `.github/workflows/ci.yml`. The gate is
real and runs on every pull request.

| Job | Step | Command |
| --- | --- | --- |
| `build` | Check formatting | `test -z "$(gofmt -l .)" \|\| { gofmt -l .; exit 1; }` |
| `build` | Vet | `go vet ./...` |
| `build` | Test | `go test ./...` |
| `build` | Build | `go build ./cmd/geocam-edge` |
| `build` | Cross-build | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64\|arm64 go build -o /dev/null ./cmd/geocam-edge` |
| `build` | Short soak smoke | `GEOCAM_SOAK=1 GEOCAM_SOAK_DURATION=3s go test ./internal/soak -run '^TestSoak$' -count=1 -v` |
| `build` | Soak smoke with race | same, `-race` |
| `build` | Short camera-scale perf smoke | `GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=5 GEOCAM_PERF_DURATION=3s go test ./internal/perf -run '^TestScaleFull$' -count=1 -v` |
| `build` | Perf smoke with race | same, `-race` |
| `multiarch-artifacts` | Release-artifact validation for both architectures | Hito W10/W11 |
| `docker-go-stage-multiarch` (amd64, arm64) | Multi-arch image build | |
| `docker-amd64-smoke` | Container smoke | |

**What the gate does *not* cover** — verified gaps, each of which matters for a
1.0 claim:

- **The general `go test ./...` run is not raced.** Only `internal/soak` and
  `internal/perf` get the race detector. The rest of the suite — including the
  concurrency-heavy `internal/agent`, `internal/processing`, `internal/rtsp` —
  is raced only when someone runs it locally.
- **No Python job exists.** `deploy/vision-worker/tests/` is never executed by
  CI, so the Vision Worker's protocol and device-resolution tests are
  unverified in CI even though they exist.
- **The real decode/inference benchmarks never run in CI** — they are behind
  the `localbench` build tag and `GEOCAM_PERF=1`, and the 25/50-camera profiles
  are documented as manual only.
- **ARM64 is emulated** (QEMU) in the image jobs, which is explicitly not
  physical ARM64 evidence.
- **No service/camera/target is ever exercised** end to end: the hardware
  certification protocol and the physical pilot are out of CI's reach.

For the final integration gate, `docs/TESTING_MULTIARCH_SOAK.md` records the
project's own multi-arch/soak procedure.

## 4. What a 1.0 release must contain

Beyond the current artefact, closing Z10 requires the appliance package to ship
the Full Edge runtime. Today `package.sh` stages the Go binary, the static
`ffmpeg`, the systemd unit templates, the scripts and the example config — and
**not** the Python Vision Worker, no Python runtime, and no model weights. A
released 1.0 must carry the worker and a reproducible, verifiable way to install
and check its runtime.

Constraints that do **not** change with 1.0:

- **PyTorch stays out of the Go process.** No `import "C"`, no third-party Go
  dependency, `CGO_ENABLED=0` on every target. Local inference remains an
  out-of-process Python worker over a Unix socket.
- **Model weights stay external and verified.** No silent auto-download. A
  missing weight is the explicit `model_missing` state, never a substitute.
- **`cpu` / `cuda` / `auto` is unchanged, and CUDA is not validated.** No
  invented accelerator support; no CUDA certification without hardware.

## 5. Blockers to SOFTWARE 1.0

Each is a real, evidenced gap. None is a documentation change.

| # | Blocker | Evidence | Effect if unresolved |
| --- | --- | --- | --- |
| **B1** | **Camera-target provisioning does not exist.** `rtsp.Manager.SetTargets` has no production caller; discovery/inventory does not feed it. | `internal/rtsp/manager.go`; callers only in `internal/perf` and tests | Every profile supervises **zero** cameras. This blocks Gateway, Hybrid and Full Edge equally, and makes the physical pilot's camera path unreachable. |
| **B2** | **The appliance does not ship the Vision Worker.** | `deploy/appliance/scripts/package.sh` file list | Full Edge cannot start from a released artefact without hand provisioning. |
| **B3** | **No retention policy for events or evidence.** No count/bytes/TTL eviction for `events/`; the free-disk gate covers JPEG captures but not MP4 clips. | `internal/fulledge/store.go`, `internal/evidence/` | Unbounded disk growth under sustained detections; certification step 8 cannot pass. |
| **B4** | **Remote config reports `applied` when nothing can be applied.** With the video pipeline disabled the module substitutes a no-op adapter and the engine ACKs success. | `internal/agent/remoteconfig_module.go`, `internal/remoteconfig/adapter.go` | The SaaS believes tuning took effect; silent configuration drift. |
| **B5** | **`/status` can imply Full Edge is active in Cloud/Hybrid.** The fulledge service is pre-built for runtime mode transitions and publishes a status block regardless of mode. | `internal/agent/fulledge_module.go`, `internal/health/health.go` | Operators misread which profile is running. |
| **B6** | **The Python Vision Worker has no CI job.** | `.github/workflows/ci.yml` (no Python job) | The worker's real protocol/device tests never run in the gate. |
| **B7** | **No production bandwidth limits are configured.** `GEOCAM_CLOUD_MAX_BYTES_PER_SEC`, `_BURST_BYTES`, `_MAX_FPS` all default to `0` = unlimited. | `internal/config/config.go` | The values are an open upstream decision; a 1.0 appliance ships with no uplink cap unless an operator sets one. |
| **B8** | **No measured Hybrid bandwidth saving.** The only figure in the repo is a synthetic selector. | `docs/performance/hybrid-j10-j11.md` | No efficiency claim may accompany 1.0 for Hybrid. |
| **B9** | **Physical validation is not done** (pilot + hardware certification). | `docs/product/HARDWARE_CERTIFICATION.md`, `docs/product/PILOT_5_10_CAMERAS.md` | SOFTWARE 1.0 can be declared; **DEPLOYED PROD** cannot. |
| **B10** | **The release workflow never builds the static `ffmpeg`.** `release.yml` calls `package.sh` directly and never invokes `deploy/appliance/scripts/build-ffmpeg-static.sh`, so a published tarball omits `ffmpeg` and `package.sh` merely warns about it. | `.github/workflows/release.yml`; `deploy/appliance/scripts/package.sh` | Even after B1 and B2 close, a released appliance cannot start the video pipeline — which every profile needs for decode — without separately provisioning `ffmpeg`. |
| **B11** | **No release has ever been cut, so the pipeline is unexercised.** `git tag -l` returns zero tags; there is no `dist/`; whether the `OTA_SIGNING_KEY` secret even exists cannot be verified from the repository. | `git tag -l`; `.github/workflows/release.yml` | RELEASE 1.0 is unproven end to end even though its mechanism is implemented and unit-tested. |
| **B12** | **Documentation drift, corrected in this delivery.** `docs/RELEASING.md` declared signing out of scope; `docs/deployment/appliance.md` claimed the GitHub Release tarball was a bare binary; and `docs/PROJECT_STATUS.md` carried a stray `<<<<<<< HEAD` merge-conflict marker with no counterpart. | `docs/RELEASING.md`, `docs/deployment/appliance.md`, `docs/PROJECT_STATUS.md` | Stale release documentation is how an operator ends up mis-provisioning an appliance. All three are fixed here. |

## 6. Definition of done

**SOFTWARE 1.0 — READY** requires all of:

1. The full gate on §3 passes on the integration branch, extended for 1.0 with
   `-race` on the modified/concurrency-heavy packages and with a Python job
   running `deploy/vision-worker/tests/`.
2. B1–B6 are closed by merged PRs (B7–B8 may be closed by an explicit,
   documented decision instead — see §7).
3. No profile is advertised as commercial-ready while a functional blocker and
   the physical validation are both open.

**RELEASE 1.0** additionally requires: a real `OTA_SIGNING_KEY` configured in
GitHub, and a `v1.0.0` tag whose workflow run publishes `*.tar.gz`,
`SHA256SUMS` and `SHA256SUMS.sig`, with the signature verified against the
public key provisioned on a device.

**DEPLOYED PROD** additionally requires a registered production target:
hostname/appliance ID, architecture, provisioned public key, previous version,
rollback evidence and health output. **No such target exists.** Until one does,
EDGE PROD stays correctly recorded as `N/A / no target` — and that is a
statement about deployment, not about the software.

## 7. Retention: decide explicitly, do not drift

Storage retention is the one item where "later" is a legitimate answer — but
only if it is *written down* as part of the 1.0 decision. Two acceptable
outcomes:

- **Resolve it before 1.0.** Configurable bounds with deterministic
  fail-safe behaviour at capacity. No "30 days" or any other commercial number
  needs to be invented: the requirement is a bounded, documented, non-destructive
  policy, not a business figure.
- **Defer it explicitly.** Then Z10 must **not** be closed as commercial-ready,
  and the limitation must be visible in the Full Edge profile documentation, the
  env examples and the certification record.

What is not acceptable is a silent 1.0 with unbounded evidence growth and
CAP-blocked disks, which is the current state (B3).

## 8. Classification

| Item | Status |
| --- | --- |
| Tag-driven signed release workflow (`release.yml`) | IMPLEMENTED (not yet triggered by a real release tag) |
| Ed25519 sign tooling + fail-closed key requirement | IMPLEMENTED / TESTED |
| OTA verify + fail-closed public key | IMPLEMENTED / TESTED |
| Appliance packaging for both architectures | IMPLEMENTED / TESTED |
| CI software gate | IMPLEMENTED, exercised per PR |
| `-race` across the general suite in CI | **NOT IMPLEMENTED** (only `soak`/`perf` are raced) |
| Python Vision Worker in CI | **NOT IMPLEMENTED** |
| Full Edge runtime in the released artefact (B2) | **NOT IMPLEMENTED** |
| Camera-target provisioning (B1) | **NOT IMPLEMENTED** |
| Event/evidence retention (B3) | **NOT IMPLEMENTED** — decision pending (§7) |
| Remote-config truthfulness when unapplicable (B4) | **NOT IMPLEMENTED** |
| Unambiguous Full Edge status reporting (B5) | **NOT IMPLEMENTED** |
| Production bandwidth limits (B7) | **OPEN DECISION** — defaults are unlimited |
| Measured Hybrid saving (B8) | **NOT_VALIDATED** |
| Marker `v1.0.0` / Release 1.0 | **NOT_VALIDATED — tag created: NO** |
| Real `OTA_SIGNING_KEY` configured | **NOT_VALIDATED — configured: NO** |
| DEPLOYED PROD target | **N/A — no production target registered** |

## 9. How to close Z6

Z6 closes when the software gate passes on the integration branch with the 1.0
extensions of §6.1, and each of B1–B9 is either closed by a merged change or
recorded as an explicit, dated decision. Declaring SOFTWARE 1.0 does **not**
require a production target and does **not** imply RELEASE 1.0 or DEPLOYED PROD;
those remain separate, and the last one remains `N/A` until a real appliance is
registered.
