# H6D-R10 — candidate macOS 14 FFmpeg full E2E under H7B on Mac M4

**Status:** Mac M4 / macOS 27 same-host lab integration validated; clean macOS 14 target **NOT VERIFIED**. H6D issue #160 / PR #162; no pack promotion, installer activation, merge, deploy or distribution.

## Context

H6D-R9 built two identical clean candidate versions of FFmpeg/FFprobe 8.1.3 from the source authenticated in R7, using macOS deployment minimum 14.0 and SDK 27.0. Their binary hashes match across both clean builds and direct system dylib closure and H.264 decoding were checked. R9's virtual inspection substitution showed all **316/316** existing package Mach-O minimum-version fields at or below 14.0. Neither R9 nor R10 establishes API compatibility in macOS 14.

The Mac M4 owner host runs **macOS 27.0.1**. Parallels Desktop is installed but its service returned “Login failed: Unable to connect to Parallels Service”; no working macOS 14 VM was found through the inspected locations. **H8 clean macOS 14 proof cannot be claimed on this host.**

## Local full-pipeline evidence (H6D-R10)

An APFS copy-on-write ephemeral clone of the **non-release** `r4` Runtime Pack was constructed under `/tmp/geocam-h6d-r10-...`. In that clone only, FFmpeg and FFprobe were overlaid as new files with hashes:

| Candidate | R9 SHA-256 |
|---|---|
| ffmpeg | `fb05859cd1517959a8b5edc609b02c4028005619763c27ad59602d0b46a32082` |
| ffprobe | `130d9a09aa290508c65ba6fe067d1d500745e8036a96e1e488c39c4ac1dd0979` |

The clone's `lab-manifest.json` was updated **only** to match the new FFmpeg/FFprobe byte sizes and SHA-256. All readiness, release and redistribution flags were checked to be explicitly `false`; there was no trusted release manifest or certification pin. All other Python/wheel/Worker files were left as in r4, with existing isolated model weights referenced externally and **not copied**.

The Go opt-in `TestLabRTSPToRealYOLO` was executed inside the H7B process sandbox:

- H7B boundary probes: IPv4 and IPv6 TCP/UDP outbound attempts to documentation addresses all returned `EPERM=1`; loopback TCP and sandbox child/grandchild inheritance **PASS**.
- E2E used local RTSP simulator → candidate portable FFmpeg → bundled isolated Python 3.11/verified Worker → YOLO/MPS → person detection callback.
- Explicit required test marker `RTSP_GO_FFMPEG_YOLO_PASS` was found and sandboxed Go test exited **0**. H7B reporter showed `LAB_EGRESS_EVIDENCE_PASS_NOT_CERTIFIED`.
- Existing H.264 fixture was generated before the test using development-host tooling, not within the sandboxed decoder pipeline. The sandbox does not constitute a full airgap (localhost proxy and other process-escape paths are outside tested coverage).
- SHA-256 of the **original r4** FFmpeg/FFprobe before and after this test matched their old R8 known hashes, confirming no original-binary replacement.
- The temporary pack clone was removed after test; no model, binary, tarball or pack file was added to Git.

## Repeatable, controlled laboratory harness

`deploy/installer/test-h6d-r10-candidate-e2e.sh` reproduces this opt-in experiment on the owner-authorized macOS arm64 Mac. It requires five explicit absolute paths: existing r4 laboratory pack, prebuilt R9 candidates, external local models directory, pre-generated H.264 fixture and H7B Python runner. It fails closed when paths, hashes, Mach-O minimums or non-release flags are invalid; invokes H7B with mandatory functional detection marker; always destroys its own exact temporary clone. It will not overwrite the original pack or download anything.

Example:

```bash
W=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
H7=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h7b
bash "$W/deploy/installer/test-h6d-r10-candidate-e2e.sh" \
  "$W/runtime-lab/packs/lab-2026.10.14-0e8657f-r4" \
  "$W/runtime-lab/r9-macos14-double-build" \
  /Users/gustavomarcon/Documents/proyectos/monitoreoia/geocam/models \
  /tmp/geocam-h6d-person-30.h264 \
  "$H7/deploy/installer/h7b-egress-cert.py"
```

## Remaining completion gates

1. Test the same selected pack and pipeline on a **real clean macOS 14 arm64** host or authorized known-good VM; check Python native extensions, Torchvision, FFmpeg, FFprobe, model inference, permissions, codesigning, notarization and OS security controls. No such test has happened yet.
2. Complete fully trusted release Runtime Pack manifest/publisher-independent signed pin and full-tree integrity, transactional H5/H6 Wails installer, crash/rollback and recovery. The default app remains fail-closed.
3. Complete H7 comprehensive offline isolation (especially possible localhost relay, external XPC helpers) and H8 installer physical cert.
4. Legal review and release-appropriate licenses for third-party wheels, Torch, Ultralytics/model weights, FFmpeg and notices remain **NO-GO** for shipping. `MODEL_REDISTRIBUTION_APPROVED=false`.

**H6D-R10 Mac27 lab pipeline PASS does not claim macOS 14 support or release eligibility.**
