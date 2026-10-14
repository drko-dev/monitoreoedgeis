# H6D-R5 — Hermetic Worker bootstrap & Torchvision Mach-O audit (laboratory)

**Status:** WORKER BOOTSTRAP PASS / PACK E2E PASS / SAME-HOST RELOCATION PASS / CLEAN-MACHINE PORTABILITY UNPROVEN / RELEASE BLOCKED.

## Source-code correction

Python 3.11's `-I` omits the directory of a directly executed `worker.py` from `sys.path`, causing `ModuleNotFoundError: backend` with the former Go product contract `-I -B <worker.py>`. A shared, fixed Go helper (`internal/workerbootstrap`) now emits `-I -B -c <audited_constant> <worker_dir> <worker.py>` and the Vision Worker supervisor rejects arbitrary `-c` code, extra switches or a mismatched import directory. No `PYTHONPATH` or host script lookup is used.

The Worker now requires a regular, confined `backend.py`. The `RuntimeManifest.Worker` schema accepts an optional `backend_relative_path`, `backend_sha256` and `backend_size_bytes` for backward-compatible parsing of templates, but **real hermetic launch requires all three**. `ValidateArtifacts` and the transactional pack's strict allowlist verify its content when declared. Missing or modified backend causes a fail-closed error. Templates without a pinned backend must never be promoted to release.

`YOLO_CONFIG_DIR` is required in the hermetic child environment and must equal the worker's private `HOME`; this prevents Ultralytics from silently writing settings to global `/tmp`. The lab uses an opt-in, operator-supplied external model directory and does not change the production model authorization gate. This source change does **not** modify the existing local pack r4 or generate a release manifest.

## Observed macOS M4 functional evidence

The opt-in `TestLabRTSPToRealYOLO` now uses the shared product bootstrap and the actual hermetic worker validation, with `PATH=/usr/bin:/bin`, bundled Python and FFmpeg, and pre-generated H.264 input from host FFmpeg. Go RTSP -> portable FFmpeg -> bundled Worker -> local YOLO/MPS -> person detection **PASS** (8.15 s on first product-equivalent launch, 8.18 s with private Ultralytics settings). The second run showed the settings file under the test's private directory rather than global `/tmp`. This proves the exercised path, not a full airgap or clean-machine install. No models or binaries were copied to Git.

## Torchvision read-only RPATH audit

- `torchvision/_C.so` and `torchvision/image.so` each retain `LC_RPATH=/Users/ec2-user/runner/_work/_temp/conda_environment_9864892836/lib` and `@rpath/libtorch*` dependencies.
- `otool -l` and `otool -L` confirmed these are Mach-O load metadata, not merely embedded text strings.
- `DYLD_PRINT_LIBRARIES=1` with pack's Python 3.11.9 and `-I`: `torchvision.extension._has_ops()=True`, real `torchvision.ops.nms` returned `[0]`, and 760 dyld lines contained **zero observed loads** from the Conda runner path or `/opt/homebrew`.
- Observed `libtorch_python.dylib`, `libtorch.dylib`, `libtorch_cpu.dylib`, `libc10.dylib`, `torchvision/_C.so` and `torchvision/image.so` loaded from within r4 pack.
- This establishes the observed dynamic loads in the original pack; the same-host relocation experiment below adds stronger evidence, but does **not** prove compatibility on a clean Mac. The absolute LC_RPATH remains a portability finding requiring final clean-machine review. No `install_name_tool` mutation or third-party binary changes were made; changing binaries requires provenance, hashes, codesign and license review.

## Relocation experiment, same Mac M4

A temporary copy-on-write APFS clone of the *entire lab pack* was created under a different randomized /tmp path, without patching any Mach-O file or modifying the source pack. Disk free was 7.1 GiB before and after cloning. The relocated pack's own Python 3.11.9 under -I imported Torch and Torchvision, and NMS returned [0] (**PASS**). The product-equivalent opt-in hermetic Go E2E was then run with H6D_LAB_PACK pointing at the relocated clone, with Python and FFmpeg/FFprobe absolute paths inside that clone, restricted PATH and offline package/hub flags. **RTSP_GO_FFMPEG_YOLO_PASS**, one person detection (23.34 s), using external local model references and a pre-generated H.264 fixture. The temporary clone was removed after the test, with available disk unchanged.

This confirms **functional relocation of the exercised code paths on the same macOS installation**. It does not certify native dependency closure under all conditions, remove the build-runner absolute LC_RPATH, validate a clean-machine launch or prove denial of all network egress. No release approval is implied.

## Remaining gates

1. Independent relocatable-pack and clean-machine tests for all Python native extension loads, including Torchvision's build-runner RPATH.
2. H7B egress-denial tests that permit loopback RTSP yet deny outside connections.
3. Legal/provenance review for all wheels, Ultralytics, model weights and FFmpeg; authenticated upstream provenance and trusted external release pin.
4. H5/H6 Wails provisioning from a sealed and authorized pack, with rollback and installer tests.
5. H8 physical certification. Keep `NewApp()` fail-closed, `MODEL_REDISTRIBUTION_APPROVED=false`, and no release.

No merge, distribution, deployment, production camera or service changes performed.
