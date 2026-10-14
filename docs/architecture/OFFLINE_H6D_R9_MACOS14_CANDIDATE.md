# H6D-R9 — macOS ARM64 portability baseline & duplicate clean FFmpeg builds

**Status:** macOS 14 **PROVISIONAL TECHNICAL CANDIDATE**, two clean same-host FFmpeg builds reproducible byte-for-byte, candidate Mach-O minimums and H.264 smoke **PASS**; clean macOS 14 runtime validation, trusted release signing, legal clearance and Full Edge packaging **BLOCKED**. Issue #160 / PR #162. No Wails activation.

## Actual full-pack minimum-version inventory

H6D R8 found FFmpeg/FFprobe with Mach-O `minos 27.0`; **macOS 11** had been documented as the H2 standalone Python minimum only, not as the final supported Full Edge OS policy.

R9 scanned the **316 Mach-O regular files** in the existing `runtime-lab/packs/lab-2026.10.14-0e8657f-r4` laboratory pack using `xcrun vtool -show-build` on the development Mac. Counts:

| Mach-O LC_BUILD_VERSION minos | Original r4 (316) | Candidate FFmpeg/FFprobe substitutions for audit only (316) |
|---|---:|---:|
| macOS 11.0 | 152 | 152 |
| macOS 11.1 | 5 | 5 |
| macOS 13.0 | 46 | 46 |
| macOS 14.0 | 111 | 113 |
| macOS 27.0 | 2 | 0 |

The macOS 14 cohort includes SciPy native extensions; the macOS 13 group includes OpenCV components. **Provisional 14.0** is the lowest plausible target under this *observed Mach-O header inventory* without replacing additional libraries. This is a suggested technical **comparison baseline**, not an approved product policy, API compatibility proof or tested older-Mac install. A full binary-tree audit does not guarantee all runtime-loaded libraries, weak-linked symbols, Python, SDK-specific APIs or Wails bundle use only compatible APIs.

The original r4 pack remains **NO-GO** for the 14.0 comparison; no pack files were overwritten or repacked.

## Offline same-host candidate build (two independent clean source extractions)

Script: `deploy/installer/build-ffmpeg-r9-macos14-lab.sh`. It requires local macOS arm64 and the same SHA-256-pinned, R7 PGP-authenticated FFmpeg 8.1.3 tarball, rejects an existing output path, never downloads anything, and uses two new source-tree extractions. Each build independently runs `./configure`, `make -j4`, and stores its binary hashes, build logs and Mach-O metadata. The wrapper was executed under process-local `sandbox-exec` policy denying outbound IP. Both builds used:

- Apple clang 21.0.0 / Xcode 27.0 / macOS 27 SDK.
- `MACOSX_DEPLOYMENT_TARGET=14.0`, `--extra-cflags=-mmacosx-version-min=14.0`, `--extra-ldflags=-mmacosx-version-min=14.0`.
- Stable non-shipping configure prefix `/opt/geocam-lab-ffmpeg`; `--disable-everything --disable-autodetect --disable-network --enable-static --disable-shared --disable-debug`, strict H.264 codec/parser, file/pipe protocols, FFmpeg/FFprobe programs, restricted encode/filter/demux/mux set. No `--enable-gpl`, `--enable-nonfree` or libx264 encoder in this candidate.
- Build paths differ, and temporary object/build directories were disposed of on successful completion. Only a separate ignored 15 MB evidence/candidate folder remains: `runtime-lab/r9-macos14-double-build`.

**Two independent clean builds on the same host/toolchain produced byte-for-byte identical final binaries:**

| Artifact | Build 1 = Build 2 SHA-256 |
|---|---|
| ffmpeg | `fb05859cd1517959a8b5edc609b02c4028005619763c27ad59602d0b46a32082` |
| ffprobe | `130d9a09aa290508c65ba6fe067d1d500745e8036a96e1e488c39c4ac1dd0979` |

All four new outputs report Mach-O `minos 14.0`, SDK 27.0, arm64 and direct system dylib/framework links only (libSystem, CoreFoundation, CoreVideo, CoreMedia). The linker signatures are still **ad-hoc**, without TeamIdentifier/Developer ID or notarization.

Standalone codec smoke test on the host: candidate `ffprobe-1` identified local H.264 fixture as `320x320`; candidate `ffmpeg-1` decoded one grayscale raw frame to stdout (**102,400 bytes = 320 × 320**, PASS). This does not prove a complete real-time YOLO E2E using the candidate or runtime behavior on macOS 14.

This establishes deterministic results under one toolchain/environment, **not independent reproducible builds** by another operator or OS/SDK. No trusted attestation/chain of custody for the resulting binaries was generated.

## Repeatable checks (read-only after initial build)

```bash
W=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
PACK="$W/runtime-lab/packs/lab-2026.10.14-0e8657f-r4"
CAND="$W/runtime-lab/r9-macos14-double-build"

python3 "$W/deploy/installer/audit-full-edge-macos-minos.py" \
  --pack "$PACK" --baseline 14.0
# Expected exit 1 because original FFmpeg/FFprobe still require macOS 27.

python3 "$W/deploy/installer/audit-full-edge-macos-minos.py" \
  --pack "$PACK" --candidate-dir "$CAND" --baseline 14.0
# Expected 316/316 headers <=14, exit 0; does NOT edit PACK or mean release-ready.

python3 "$W/deploy/installer/test-audit-full-edge-macos-minos.py"
bash -n "$W/deploy/installer/build-ffmpeg-r9-macos14-lab.sh"
```

Local JSON evidence is ignored by Git:
- `runtime-lab/h6d-r9-macos-minos-original.json`
- `runtime-lab/h6d-r9-macos-minos-candidate.json`
- `runtime-lab/r9-macos14-double-build/{configure-*,build-*,digests.txt,toolchain.txt,two-build-comparison.txt,...}`

## Remaining gates / NO-GO

1. **Confirm an actual supported product macOS baseline** across Python, Torch/torchvision, SciPy, OpenCV, Wails app bundle and FFmpeg. macOS 14 remains provisional until agreed and verified. A Mac running macOS 27.0.1 **cannot** prove functional compatibility on 14.
2. Independently rebuild from verified source on a clean controlled macOS 14 environment, compare hashes or explain compiler/SDK differences, check SDK availability and supported API usage, and run full H6D E2E and H7 network isolation on target hardware. Consider macOS 14 patch-level and OS signing/notarization requirements.
3. Legal review of LGPL/codec dependencies, all 36 wheel notices, Ultralytics AGPL/Enterprise terms and model weights; independent publisher-authenticated release manifest, code-signing and H8 clean-machine installer tests remain pending.
4. Never promote candidate outputs to `r4`, alter its laboratory manifest, replace Wails binaries or enable provisioning without separate authorization. **`RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`**.

**Result:** BUILD_TWO_CLEAN_SAME_HOST_MATCH=PASS; MACHO_BASELINE_14_CANDIDATE=PASS; MACOS_14_CLEAN_MACHINE=NOT_TESTED; REPRODUCIBLE_INDEPENDENT_BUILD=NOT_VERIFIED; READY_FOR_WAILS=NO; READY_FOR_RELEASE=NO.
