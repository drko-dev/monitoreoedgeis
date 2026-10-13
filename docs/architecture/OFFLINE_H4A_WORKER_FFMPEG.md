# Offline H4A: Runtime Pack media and Vision Worker contract

## Scope and non-goals

H4A prepares a verified-path contract for the existing macOS `darwin/arm64`
Runtime Pack and an inert Vision Worker launch specification. It does not add
FFmpeg/FFprobe binaries, run provisioning, start Python or FFmpeg, load models,
connect cameras, perform inference, or claim `RUNTIME_READY`/`INFERENCE_READY`.
No product process launch path is wired to the new contract in this phase.

`MODEL_REDISTRIBUTION_APPROVED=false` remains a release gate. No YOLO weights or
PyTorch wheels are added or downloaded.

H4A was committed as `d2b3405d4bff39ee877df2efc98f6e412465a29d` and submitted
as PR #143 against approved issue #142. H4B is being prepared on a separate
branch based on that commit. H4A's contract tests passed; its GitHub checks are
independent of H4B.

## Existing components verified

- `internal/installer/runtime_manifest.go`: `RuntimeManifest.Binaries` already
  represents native tools with name, relative path, version, SHA-256 and exact
  size. `ValidateSchema` checks manifest metadata, path traversal, duplicate
  declarations, hashes' syntax and positive sizes. `ValidateArtifacts` checks
  physical files, byte counts, hashes, and symlink containment. It does not
  check executable mode. The embedded `assets/runtime-manifest.json` is
  explicitly `manifest_type: template` and contains example hashes; it is not a
  certified release pack.
- `internal/installer/ffmpeg.go`: `DependencyResolver.Resolve` searches
  configuration, PATH, system/Homebrew paths and managed data; `Install`
  supports Homebrew and `InstallManaged` can download an artifact. These are
  existing legacy/setup flows and MUST NOT be used as the Full Edge Runtime
  Pack resolver.
- `internal/installer/vision_runtime.go`: `StageWorkerAssets` copies embedded
  `worker.py`, `backend.py` and `requirements.txt`. The current resolver and
  provisioner support host Python/venv and package installation; they are not
  proof of an offline bundled runtime.
- `internal/vision/worker.go`: `Worker` already supervises a child, performs a
  Unix-socket health handshake, applies start/inference timeouts, handles
  cancellation/shutdown, and publishes lifecycle states. `spawnAndHandshake`
  uses `exec.Command` and currently inherits the parent environment. `Ready`
  describes worker health, not artifact provenance, licensing or successful
  model inference.
- `deploy/vision-worker/worker.py`: the worker has CLI entry points for socket,
  model paths and inference configuration; it reports health after backend
  initialization. H4A does not invoke it.
- `deploy/installer/assemble-release.sh`: the current assembler packages
  platform installers but does not assemble a sealed Runtime Pack containing
  FFmpeg/FFprobe and the worker/runtime dependency set.

## H4A code contract

`internal/installer/runtime_pack_contract.go` adds:

- `ResolveRuntimePackMedia`: accepts only a sealed `release` manifest targeting
  `darwin/arm64`; validates the manifest and complete pack artifact set before
  returning paths for exactly the declared `ffmpeg` and `ffprobe` binaries.
  It checks the resolved media artifacts are regular executable files and returns
  canonical in-pack paths. It performs no PATH, Homebrew, host-path, network or
  subprocess lookup. Internal symlinks are resolved to their canonical target;
  escaping/broken symlinks and traversal are rejected by the existing
  artifact validator.
- `BuildRuntimePackWorkerLaunchSpec`: creates metadata for the bundled Python
  (`-I -B` isolated mode) and worker entrypoint, bundled media paths, persistent
  caller-authorized models directory, a pack-only PATH, explicit FFmpeg
  variables and a bounded startup timeout. It also requires bundled Python to
  be a regular executable file. It does not execute the command.

## H4B worker integration seam

`internal/vision.Worker` now has an explicit hermetic mode. In that mode it
validates that Python, the Worker entrypoint, FFmpeg and FFprobe are contained
in the Runtime Pack; rejects unknown/duplicate environment variables and
`PYTHONPATH`/`PYTHONHOME`; checks the persistent model directory; and assigns
the allowlisted environment as a full replacement on `exec.Cmd.Env`. It
requires `-I`, an authorized model pack and a preverified runtime pack. Invalid
or incomplete configuration fails before a process starts. The legacy Worker
path remains unchanged when hermetic mode is not selected.

`internal/agent.buildBundledVisionSink` is an explicit adapter from the
installer launch-contract interface to the existing supervisor. It is not
called by current `newVisionSink`: no runtime pack selector or pack activation
path is wired into production setup yet. This preserves legacy behavior and
prevents a template manifest from silently enabling Full Edge. The contract
builder currently marks model authorization false, so the adapter returns an
authorization error before constructing a worker until a separately
approved/verified model pack is available.

The launch environment is a replacement, not an overlay. Python runs with
`-I -B`; `PATH` points only to the bundled media directory. `HOME` and `TMPDIR`
are explicitly scoped to worker state. Existing supervision continues to own
startup timeout, handshake, cancellation, structured stdout/stderr logging,
error state and restart backoff. A process handshake is not an inference
certification.

This does **not** yet make Full Edge's Go video decoder hermetic. The active
camera pipeline constructs `processing.FFmpegDecoderConfig` from
`config.VideoFFmpegPath` (`internal/agent/agent.go`, `newCameraPipeline` in
`internal/processing/pipeline.go`); an empty path becomes `ffmpeg` and is
resolved through the Go process PATH in `internal/processing/ffmpeg_decoder.go`.
The Worker child environment cannot constrain that separate Go subprocess.
Production Runtime Pack selection must provide the verified absolute bundled
FFmpeg path to the video pipeline and fail closed in bundled mode. This wiring
is still pending; no claim is made that Homebrew/host FFmpeg is excluded from
the current Full Edge camera decoder.

SHA-256 plus size establish that files match the manifest; they do not prove
publisher identity or legal provenance. A release still requires reviewed
source URLs/artifact provenance, pinned real checksums, redistribution terms,
signature/release integrity policy, macOS code-signing/quarantine behavior, and
the required executable mode. No checksum is fabricated in this change.

## Readiness contract

Keep these facts distinct in future status/reporting:

1. Runtime pack and worker artifacts verified.
2. Compatible runtime/dependencies provisioned.
3. Licensed and integrity-verified model pack available.
4. Worker process healthy and reports expected models loaded.
5. Inference exercised on approved test data/device.

The existing worker `ready` handshake covers only its process/backend startup
semantics. It must not be promoted to Full Edge inference certification by
itself. H4A tests only the contract metadata and synthetic file verification.

## H3 integration boundary

H3 owns the offline Python/wheelhouse provisioning contract. H4A deliberately
does not edit `wheelhouse.go`, `wheelhouse_test.go`,
`assets/wheelhouse-lock.json`, or `fetch-wheelhouse.sh`. Integration must map
the H3-packaged Python, dependency paths and compatibility tuple into the same
release manifest, then prove that `-I` still resolves the installed bundled
site-packages correctly. Do not enable the launch spec until that is verified.

H3 remains open as PR #140. No H3 files or wheelhouse code are copied into this
branch. H3 must provide the compatible bundled Python and offline dependency
layout; H4 integration must pass that verified pack specification into the
adapter and prove imports resolve under `-I`. The current adapter is a seam,
not an active runtime wiring.

Packaging is also blocked on actual authorized FFmpeg/FFprobe artifacts. The
release assembler is not changed to insert fabricated or unverified assets.
Once provenance, real hashes/sizes and redistribution terms are approved, H4
must add the pack to assembly and verify it before signing/releasing. Atomic
activation/rollback and physical macOS ARM64 validation remain pending. H7A's
future harness should be integrated deliberately; its package/workflow are not
imported or modified here.

## Evidence and remaining work

Automated tests create tiny temporary synthetic files with computed checksums;
they are contract tests, not real FFmpeg/Python binaries. H4A tests exercise
missing FFmpeg, absent FFprobe declaration, hash and size mismatch, wrong
architecture, non-executable mode, path traversal, external symlink, host PATH
presence, release-vs-template gate, bundled Python selection and isolated args.
H4B tests exercise the supervisor's explicit environment replacement, host
Python/Homebrew/FFmpeg exclusion, containment, environment allowlisting,
missing models and the authorization gate. Existing fake-worker tests continue
to cover the legacy handshake, timeout, cancellation and restart lifecycle.

Not implemented/validated: hermetic FFmpeg selection for the Go camera decoder;
release-pack assembly; real FFmpeg/FFprobe builds, hashes, source or redistribution
rights; actual macOS binary execution or `-version`; real Python/runtime imports
with H3 wheels; process startup/handshake using a bundled Python; model pack
availability, authorization or loadability; pack assembly, interrupted install,
rollback/preservation; external-network isolation; physical Apple-Silicon/MPS
inference. Contract tests do not prove offline installation or `INFERENCE_READY`.
Distribution remains blocked until H3 integration and artifact provenance,
checksums, compatibility and redistribution terms are approved.
