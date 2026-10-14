# H6D — macOS ARM64 laboratory Runtime Pack

**Result: laboratory assembly and pack-backed inference path validated; not a release, not Wails-ready, and not offline-certified.**

## Scope and provenance

- Worktree branch: `docs/h6d-runtime-pack-release-preflight`.
- Source commit at assembly: `0e8657f9a7938bee95e6e4f29c4e8d112aa96279`.
- PR #162 remains open. Its existing remote checks apply to the remote PR head only, not to local changes.
- Local-only pack: `runtime-lab/packs/lab-2026.10.14-0e8657f-r4` (excluded by this worktree's `.git/info/exclude`).
- The assembler hard-links the already verified local Python tree, 36-wheel wheelhouse and portable FFmpeg files. It does not download or duplicate their contents. The pack and source artifacts share inodes and must be treated as one mutable local artifact set; this is not a relocatable archive.
- No model weights are in the pack. Its manifest contains external, test-only references to the two existing local model files and sets `model_redistribution_approved=false`.

The generated `lab-manifest.json` is explicitly `pack_type=laboratory-only`; `release_eligible`, `trusted_pin_present`, `runtime_ready`, `inference_ready` and model approval are all false. It inventories each regular file by path, size and SHA-256, and records declared wheel origins/licensing without converting them to verified provenance or legal approval. It is not `runtime-manifest.json`, a signature, or a trusted pin.

## Artifact evidence

| Artifact | Version / architecture | Size | SHA-256 | Evidence |
|---|---|---:|---|---|
| Python standalone interpreter | CPython 3.11.9, arm64 | 50,168 B | `906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170` | Executed from pack with `-I`; imports Torch, Torchvision and Ultralytics |
| Vision Worker | repository asset | 5,464 B | `bac3cbf7b6f424e2e7bf8ab32e7f925fb27b18b3c42ab55749719f1ed66fa502` | Packaged with its `backend.py` dependency |
| Worker backend | repository asset | 9,358 B | `246fc14019f9c1bd50d409ec6a499b427471ba07d6ceab8bd9e4336dcfa1fedb` | SHA-256 recorded |
| FFmpeg | 8.1.3, Mach-O arm64 | 4,046,264 B | `9e97cd0015b2ed2e2b3bba0401f8554b7cceb93b27558f2ee309ffd824d29b07` | Version command and actual Go decoder path exercised |
| FFprobe | 8.1.3, Mach-O arm64 | 3,853,448 B | `ebf3c455845fee554bfb9ffa0e4bab6268c589c199852b0c3c641b4598282567` | Version command and portable dylib closure check |
| Wheelhouse | 36 locked wheels | 173,935,126 B total | Per-wheel lock SHA-256s | 36/36 size and hash checks passed; installed package tree is included |

The pack manifest contains 30,676 regular-file entries. A complete post-assembly rehash reported zero size/hash mismatches and zero symlink escapes. Python and FFmpeg files in the pack have the same inode as their verified source artifacts. `otool` closure audit for bundled FFmpeg/FFprobe passed (2 executables, no external dylib dependency).

## Functional and isolation tests

`TestLabRTSPToRealYOLO` gained an opt-in `H6D_LAB_PACK` path. In pack mode it verifies the lab-only manifest and SHA-256/size for the interpreter, Worker and FFmpeg, launches the bundled Python/Worker in isolated mode, sets the Go decoder to the bundled FFmpeg, and requires a returned `person` detection.

The E2E invocation used pack revision `r3`. Final documented revision `r4` was reassembled with provenance/license declarations added to the lab manifest; its full 30,676-entry file inventory (all paths, sizes and hashes) exactly matches `r3`. Thus tested runtime bytes are identical, while the final manifest metadata was validated separately.

Observed on Mac M4:

- Pack-backed Go → local RTSP simulator → bundled FFmpeg H.264 decoder → bundled Vision Worker → YOLO on MPS: **PASS**, one person detection (`frame=1`, `detections=1`).
- Worker reports `device=mps`; the isolated Python probe reported `torch 2.4.0`, `torchvision 0.19.0`, `ultralytics 8.3.1`, and `torch.backends.mps.is_available() == True`.
- The test ran with `PATH=/usr/bin:/bin`, `GOPROXY=off`, `GOSUMDB=off`, `PIP_NO_INDEX=1`, `HF_HUB_OFFLINE=1`, `TRANSFORMERS_OFFLINE=1`, and empty `PYTHONHOME`/`PYTHONPATH`. The decoder and worker were selected by absolute paths from the pack. There was no `ffmpeg`/`ffprobe` or Homebrew directory in `PATH`; macOS `/usr/bin/python3` was present but not used (the Worker command was the pack's absolute interpreter path). The Go test runner itself was invoked by its host developer-tool path, outside the tested runtime.
- Input fixture `/tmp/geocam-h6d-person-30.h264` was generated before the test using host `/opt/homebrew/bin/ffmpeg`; SHA-256 `916fa279b8683bfed0e71ef2635871aa7c16db203e8e8159dc5d850753b02a41`. This is an external, pre-generated laboratory input, not a shipping dependency. The bundled minimal FFmpeg is decode/probe-focused and does not support the `-loop` option or H.264 muxer used by the fixture generator.
- An egress-denied run was attempted with `mac-lab-deny-ip-egress.sb`; it denied loopback RTSP TCP (`operation not permitted`), so the integrated E2E could not run under that policy. The successful run is **not proof of airgap**. Offline flags and disabled package index prevent the tested code paths from using PyPI/model hubs, but do not prove all direct socket egress is impossible.
- A direct `python -I -B worker.py` launch failed with `ModuleNotFoundError: backend`. For the lab test only, a small bootstrap explicitly adds the verified pack's `worker/` directory to `sys.path`. The repository's `BuildRuntimePackWorkerLaunchSpec` currently emits `-I -B <worker.py>` directly; the product launch contract must be corrected and tested before Wails integration. This work does not change that production-facing contract.

## Dynamic dependencies and portability limits

- FFmpeg/FFprobe passed `check-ffmpeg-dylib-closure.py`.
- A read-only scan covered 311 Python Mach-O executables/extensions and 710 load references; `otool` succeeded for all scanned files. No Homebrew dylib load reference was found. Raw `/opt/homebrew` strings exist in several wheel binaries; their meaning was not independently resolved, although no matching `LC_LOAD_DYLIB`/`LC_RPATH` dependency was observed. Review them before release.
- `torchvision/_C.so` and `torchvision/image.so` contain `@rpath/libtorch*` references whose recorded `LC_RPATH` points to a build-runner Conda path outside this pack. Importing Torch first made `torchvision.extension._has_ops()` and an actual `torchvision.ops.nms()` call pass on this Mac; nevertheless, that absolute build-time RPATH is a **relocation/release blocker** and needs an independently reproducible, bundle-relative closure check. The single inference pass does not certify every native extension or clean-machine behavior.
- Python's source archive is not present for independent re-verification here. Its interpreter hash matches the repository's declared binary hash, while the archive source/hash remain declarations. The FFmpeg source tarball's observed SHA-256 is recorded in the manifest; its PGP signature was not verified.

## Licensing and release gates

- The 36 wheel files match the lock; their `origin` and license labels are declarations. The local audit does not verify license texts/METADATA against each distribution; all 36 remain `license_verified_from_distribution=false` and `redistribution_approved=false`.
- Ultralytics is declared AGPL-3.0. The exact commercial/redistribution obligations for the Worker and model use are unresolved. No weights were copied or uploaded. `MODEL_REDISTRIBUTION_APPROVED=false` remains unchanged.
- FFmpeg's local build record describes a minimal static H.264 decoder build without GPL/libx264. Source signature and redistribution obligations are not cleared; `redistribution_reviewed=false`.
- Runtime Pack release manifest remains a template; no trusted external digest/pin or authorized release publisher was supplied. `check-runtime-pack-release.py` correctly rejects the lab pack because it has no `runtime-manifest.json`; no release manifest or fake pin was created.
- Issue #160 is open and has no `status:approved` / `type:*` labels at this check. The local work is therefore not pushed to or added to PR #162.

## H5/H6 and next step

Synthetic H5/H6-facing Go tests passed: `go test ./internal/vision ./internal/installer ./cmd/geocam-edge-ui`; the same packages passed under `go test -race`. `go vet ./cmd/... ./deploy/... ./internal/...` passed. The repository-wide `go test ./...` and `go vet ./...` stop only at the existing ignored `runtime-lab` directory, where several standalone harness files each declare `main`; product packages passed before that local harness build error. The harness files were not edited.

No actual Wails provisioning was enabled, no RecoveryManager transaction was applied to a real runtime, and no readiness state was asserted. Before Wails integration: fix the isolated Worker import bootstrap; resolve Torchvision's absolute RPATH and audit all native dependencies; obtain approved/provenanced artifacts and legal review; generate a sealed copy with an authenticated external pin; then connect H5/H6 while keeping default preparation fail-closed. H7B must still verify process-tree egress denial; H8 physical M4 certification is not implied.
