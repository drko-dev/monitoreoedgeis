# H6D-R8 — FFmpeg source-to-portable-binary consistency audit

**Status:** Local technical consistency PASS; **portability baseline NO-GO**; reproducible/attested build **NOT VERIFIED**; commercial release **BLOCKED**. Issue #160 / PR #162.

## Source-to-build-tree check on Mac M4 (2026-10-14)

- Previous H6D-R7 verified FFmpeg 8.1.3 upstream source tarball detached PGP signature, authoritative published release key fingerprint and SHA-256 `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`. H6D-R8 reads the same on-disk tarball; it **does not repeat the external identity authentication**.
- With no extract/build/download step, a streaming archive scan compared **10,116/10,116 regular files** in the signed release tarball against their corresponding source-tree files under `runtime-lab/ffmpeg-source/ffmpeg-8.1.3/`. All byte-level SHA-256 checks **MATCH** (no mismatches). Only archive-listed sources were checked: generated build outputs/config, local extras and toolchain inputs were **not authenticated** by this test.
- Locally recorded `configure.log` says network support **no**, static **yes**, shared **no**, FFmpeg/FFprobe enabled, and `License: LGPL version 2.1 or later`. `build.log` contains `LD ffmpeg_g`, `LD ffprobe_g`, `STRIP ffmpeg`, `STRIP ffprobe`. These are **mutable build logs**, not remotely verifiable provenance or reproducible-build attestation. Recorded build/configure logs are hash-inventoried for later comparison.
- The binary-reported `-buildconf` for FFmpeg/FFprobe is consistent with `--disable-everything`, `--disable-autodetect`, `--disable-network`, `--enable-static`, `--disable-shared`, and H.264 decoder/parser. The audited binaries did **not** report `--enable-gpl`, `--enable-nonfree` or `--enable-libx264`. That is not a license determination for other FFmpeg copies or video-clip encoding paths.

## Exact binary matching / dynamic linkage

| Component | SHA-256: source build, portable folder and pack r4 |
| --- | --- |
| FFmpeg | `9e97cd0015b2ed2e2b3bba0401f8554b7cceb93b27558f2ee309ffd824d29b07` |
| FFprobe | `ebf3c455845fee554bfb9ffa0e4bab6268c589c199852b0c3c641b4598282567` |

For each executable, all **three paths** have identical SHA-256. Both report only the `arm64` architecture. The `otool -L` direct dependencies are limited to `/usr/lib/libSystem.B.dylib` and the system `CoreFoundation`, `CoreVideo`, `CoreMedia` frameworks. No Homebrew dylib was observed in the **direct** dependency list. Prior H6D tests exercised the portable FFmpeg H.264 decoder with local RTSP, but direct linkage checks alone do not certify the entire runtime or dyld closure for all conditions.

## Critical portability and trust findings

**Both FFmpeg and FFprobe have Mach-O `LC_BUILD_VERSION minos 27.0`, SDK 27.0**, and run successfully on the local macOS **27.0.1**. That is a genuine deployment-version requirement in the executable and is **not compatible with machines running earlier macOS versions**. `docs/architecture/FULL_EDGE_SELF_CONTAINED.md` documents **macOS 11.0 as the minimum for the H2 standalone Python distribution**, not an independently agreed product-wide FFmpeg target. H6D-R8 uses 11.0 as a conservative comparison baseline; formal supported macOS release targets must be decided separately. **No re-linking or rewriting was performed.**

Both binaries have **ad-hoc linker signatures** with no TeamIdentifier. They are not Developer ID signed/notarized release executables, and their equivalence to the local build tree **does not establish a reproducible or cryptographically attested source-to-binary build**. Current compiler inspection shows Apple clang 21.0.0 and local configure reports `gcc` (Apple clang wrapper); this is a present-day toolchain observation, not an independently authenticated historic build environment.

## New repeatable fail-closed evidence tool

`deploy/installer/audit-ffmpeg-build-chain.py` is read-only, network-free and model-free. It pins tarball and executable digests, streams SHA-256 for every archived source, checks linked/build/pack copies, reads build-config output, inspects Mach-O arch/direct dylibs/`minos`, and rejects missing or altered source files, tampered binaries, unexpected dynamic linkage, GPL-related flags and deployments newer than the explicit comparison baseline.

**Actual Mac M4 outcome:** `technical_consistency_pass=true`, `baseline_portability_pass=false`, `reproducible_build_verified=false`, `release_codesign_verified=false`, `legal_redistribution_approved=false`, `release_eligible=false`. A nonzero tool exit is **expected** due to the deployment-version mismatch. The full JSON report is a local, untracked laboratory artifact; no binaries, signing keys or model weights are committed.

Run on authorized Mac, **no compilation**:

```bash
W=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
python3 "$W/deploy/installer/audit-ffmpeg-build-chain.py" \
  --lab-root "$W/runtime-lab" \
  --pack "$W/runtime-lab/packs/lab-2026.10.14-0e8657f-r4" \
  --max-macos-minos 11.0 --summary
python3 "$W/deploy/installer/test-audit-ffmpeg-build-chain.py"
```

## Follow-up / hard NO-GO gates

1. Decide which macOS versions are actually supported by GEO CAM Full Edge, including FFmpeg, Python/Torchvision and Wails. Rebuild and test in a separate non-shipping lab against a correctly selected **minimum deployment target and SDK**; do not patch `minos` on the existing Mach-O executable blindly.
2. To claim reproducibility, independently rebuild from the PGP-validated tarball with pinned compiler/SDK/config/toolchain, compare binary outputs or normalized reproducibility evidence, record all transforms, validate code signatures and fully verify the output on the target systems.
3. Complete real license review for FFmpeg codecs/linked libraries (including other clipping workflows), all 36 wheels, Ultralytics and model weights; review NOTICE/source obligations. Keep model redistribution disabled.
4. Obtain an independently authenticated signed release manifest pin, H7 full offline certification, H8 clean-machine validation and an approved Wails installer integration.
5. Keep `MODEL_REDISTRIBUTION_APPROVED=false`, `RUNTIME_READY=false`, `INFERENCE_READY=false`. No merge, deploy, release or production changes in this milestone.

**Status distinction: local archived source ↔ build tree and build/portable/pack binary hashes agree; release build provenance and compatibility DO NOT PASS.**
