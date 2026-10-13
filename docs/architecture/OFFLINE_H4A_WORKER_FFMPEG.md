# Offline H4A: Runtime Pack media and Vision Worker contract

## Scope and non-goals

H4A prepares a verified-path contract for the existing macOS `darwin/arm64`
Runtime Pack and an inert Vision Worker launch specification. It does not add
FFmpeg/FFprobe binaries, run provisioning, start Python or FFmpeg, load models,
connect cameras, perform inference, or claim `RUNTIME_READY`/`INFERENCE_READY`.
No product process launch path is wired to the new contract in this phase.

`MODEL_REDISTRIBUTION_APPROVED=false` remains a release gate. No YOLO weights or
PyTorch wheels are added or downloaded.

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
  (`-I` isolated mode) and worker entrypoint, bundled media paths, persistent
  caller-authorized models directory, a pack-only PATH, explicit FFmpeg
  variables and a bounded startup timeout. It also requires bundled Python to
  be a regular executable file. It does not execute the command.

The launch environment is a proposed replacement environment, not an overlay:
the later integration must assign it to `exec.Cmd.Env` instead of appending to
the host environment. The current `internal/vision.Worker` does not consume this
spec yet. H4B must wire it only after confirming the active runtime activation
root and ownership of the persistent model directory.

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

H4B remains responsible for real artifact sourcing and redistribution review,
release assembly, atomic installation/activation and rollback behavior,
worker wiring, macOS ARM64 execution validation, and actual offline integration
certification. H7A's future harness should be integrated deliberately; its
package and workflow are not imported or modified here.

## Evidence and remaining work

Automated tests create tiny temporary synthetic files with computed checksums;
they are contract tests, not real FFmpeg/Python binaries. They exercise missing
FFmpeg, absent FFprobe declaration, hash and size mismatch, wrong architecture,
non-executable mode, path traversal, external symlink, host PATH presence,
release-vs-template gate, bundled Python selection, isolated argument contract,
and persistent models path validation.

Not validated in H4A: real FFmpeg/FFprobe builds or hashes; artifact source or
redistribution rights; actual macOS binary execution or `-version`; Homebrew
absence on a clean machine; real Python/runtime imports; actual env replacement
or Worker timeout/cancellation; model availability/authorization; install
interruption, rollback and preservation; no-network operation; physical
Apple-Silicon/MPS inference. These require H3/H4B artifacts and the later
certification environment. The release must remain blocked from distributing
FFmpeg until its provenance, checksum and redistribution terms are approved.
