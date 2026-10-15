# GEO CAM — Distribution profiles and Full Edge go/no-go boundaries

**Status: CORE-ONLY packaging guard implemented (tests); FULL_EDGE distribution remains blocked.** This work is release assembly *hardening*, not an authorization to tag, deploy, publish, sign or distribute any binaries or third-party components.

## Separate the two deliverables

| Profile | Includes | Claims allowed | Technical/legal state |
| --- | --- | --- | --- |
| **CORE_ONLY** | Existing Edge desktop GUI installers for Windows amd64, macOS arm64, Linux amd64/arm64; own app/daemon binaries and GUI resources | Build architecture, version, archive member names and SHA-256 can be checked | Core-only packaging validation implemented. **Not an independent commercial redistribution/OS signing license clearance** |
| **FULL_EDGE_RUNTIME** | Signed and authenticated Python standalone, pinned wheels (including Ultralytics), FFmpeg/FFprobe, Worker, optional models | Only after separate real signed release assembly, legal provenance and clean-host certification | **BLOCKED**. No approved publisher release trust root, independently cleared wheel/model/codec distribution, physical clean macOS14 certification or full H7 installed airgap |
| **EXTERNAL_USER_MODEL** | User-provided local weights stored **outside** the distributed installer, optionally with an independently authenticated provenance manifest | Does not mean installer redistributes model weights | An architecture option **only**. Does **not** remove the licensing obligations of the Ultralytics code/architecture/runtime or authorize inference by itself |

This project uses Ultralytics under AGPL-3.0 unless a separate Enterprise or other suitable authorization is actually established. Removing model weights from an installer does **not** automatically resolve obligations of distributing or integrating the Ultralytics software. Complying with applicable AGPL source-release conditions or obtaining an appropriate commercial license are the two common routes; this document is not legal advice.

## Existing four-platform assembler is now fixed to CORE_ONLY

`deploy/installer/assemble-release.sh` still validates all four platform BUILD_INFO references, SHA-256 digests, platform binary formats and package versions before copying artifacts. A new read-only `audit-core-only-installer.py` inspects **every accessible ZIP/TAR member name and type**. It rejects visible paths that look like model weight files (`.pt`, `.onnx`, `.safetensors`, etc.), Ultralytics/site-packages/wheelhouse/model/runtime-pack directories, FFmpeg/FFprobe executables, nested dependency packages, unsafe traversal/control-character paths and nonregular archive entries. A matching freshly recalculated SHA-256 **does not bypass this profile**. Any `MODEL_REDISTRIBUTION_APPROVED` value other than literal `false` causes an explicit failure; setting an environment variable can never prove distribution rights.

The Windows PE executable and application binaries on other platforms are **opaque** to filename-only checks. The helper deliberately reports `opaque_executable_payload_unverified=true` and `approved_for_release=false` even on successful archive inspection. No static scanner can prove all third-party license rights or detect arbitrary model bytes hidden in an executable. Hermetic source-to-binary build attestation and independent commercial review remain requirements for an authorized release.

No model weights, wheels, FFmpeg binaries, runtime packs, signing keys or licensed Python runtime artifacts were added to source or installer outputs by this change. It does **not** remove the existing model authorization flags, does not wire `NewApp()` to a new installer, and does not build or distribute anything automatically.

## Reproduce the safe tests

```bash
python3 deploy/installer/test-audit-core-only-installer.py -v
(cd deploy/installer && go test -count=1 -run 'TestAssembleRelease' .)
# Existing full-edge signed runtime remains gated and is tested separately:
go test -race -count=1 ./internal/installer ./cmd/geocam-edge-ui ./internal/vision ./internal/agent
```

The assembler's integration test injects a deliberately fake `models/yolo11n.pt` into a temporary macOS ZIP and **recomputes its BUILD_INFO hash**. The new core-only gate still rejects the model. Another test rejects the `MODEL_REDISTRIBUTION_APPROVED=true` environment-variable bypass. All fixture artifacts are generated inside test temporary directories.

## Remaining independent approval gates

1. Third-party redistribution rights for Python, all wheels/Ultralytics, FFmpeg build/codecs, other bundled native libraries and YOLO models. *A user-supplied model changes who distributes model bytes, not software licensing.*
2. A publisher-owned independent trust anchor, signed immutable Runtime Pack, updater/executor ownership and proper ACL/codesigning controls. PR #170 implements opt-in safeguards but does not provide an approved production installer.
3. Real clean macOS 14 ARM64 on-device H8 certification (native Python, Torch, FFmpeg, Worker, person E2E and installation/rollback); macOS27 lab success does not substitute.
4. H7 fully installed offline/allowlisted LAN camera test, including process/host airgap, proxy and localhost-relay bypass review.
5. Shipping platform code signing/notarization, signing key governance, reproducible release build chain and independent final QA.
6. Owner-approved PR merge and **separate explicit authorization** for any tag, deploy or release.

**Final flags unchanged:** `MODEL_REDISTRIBUTION_APPROVED=false`, `READY_FOR_INSTALLER=false`, `READY_FOR_RELEASE=false`, `RUNTIME_READY=false`, `INFERENCE_READY=false`. A successful CORE_ONLY preflight must never flip any of these.
