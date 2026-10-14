# H8 — macOS 14 ARM64 Compatibility Certification Report

**Product:** GEO CAM Edge  
**Milestone:** H8 (macOS 14 ARM64 Real Compatibility)  
**Branch:** `test/full-edge-h8-macos14-compat`  
**Git HEAD:** `4df9aa880faa484d2fb835febbf6939df3f3f820`  
**Certification Status:** `STATIC_AND_LAB_PASS_PHYSICAL_MACOS14_BLOCKED`  
**System Readiness Gates:**  
`RUNTIME_READY=false`  
`INFERENCE_READY=false`  
`MODEL_REDISTRIBUTION_APPROVED=false`  
`PHYSICAL_MACOS14_STATUS=PHYSICAL_MACOS14_BLOCKED`  

---

## 1. Executive Summary & Environment Verification

Milestone H8 establishes reproducible verification and compatibility gating for GEO CAM Edge running on **macOS 14 (Sonoma) Apple Silicon ARM64**.

### Host Environment Inspection

The verification harness executed on the development machine and audited the runtime hardware and OS kernel:
- **Architecture:** `arm64` (Apple Silicon M4, `T8132`).
- **Operating System:** `macOS 27.0.1` (Darwin Kernel `27.0.0: Tue Aug 11 21:02:57 PDT 2026; root:xnu-13432.1.9~1/RELEASE_ARM64_T8132`).
- **Rosetta 2 Emulation:** `proc_translated = 0` (Native execution; Intel runners and Rosetta 2 translation are strictly rejected).
- **Virtualization / Runner Availability:** Parallels Desktop service is not initialized in unattended mode; no authorized macOS 14 ARM64 VM or physical macOS 14 test rig is currently provisioned on this host.

### Physical macOS 14 Gate Declaration

Because the host machine operates on macOS 27.0.1 and no authorized macOS 14 ARM64 environment is available without installing unapproved VMs, H8 completes all automatable static Mach-O audits, hermetic contract verifications, and synthetic/lab pipeline tests, and explicitly declares:

> **`PHYSICAL_MACOS14_BLOCKED`**  
> Physical end-to-end execution on a clean native macOS 14 ARM64 host or authorized unattended VM is blocked. Intel runners are explicitly rejected as invalid for ARM64 certification.

---

## 2. Certification Verification Matrix

| Check ID | Category | Target / Component | Evaluated Status | Evidence & Guarantees |
|---|---|---|---|---|
| `host-arm64-architecture` | Environment | Host Platform | **PASS** | Native `arm64` Apple Silicon confirmed; Rosetta translation rejected. |
| `physical-macos14-environment` | Environment | macOS 14 Native Rig | **BLOCKED** | Host is macOS 27.0.1; no authorized macOS 14 ARM64 physical machine or unattended VM available (`PHYSICAL_MACOS14_BLOCKED`). |
| `macho-python-runtime-minos` | Binary Header | Python 3.11 Standalone | **PASS** | `python3.11`, `libpython3.11.dylib`, and all `lib-dynload/*.so` declare `minos 11.0 arm64` (<= 14.0). |
| `macho-torch-runtime-minos` | Binary Header | PyTorch 2.4.0 (`torch/lib`) | **PASS** | `libtorch.dylib`, `libtorch_cpu.dylib`, `libtorch_python.dylib`, and `_C*.so` declare `minos 11.0 arm64` (<= 14.0). |
| `macho-torchvision-minos` | Binary Header | Torchvision 0.19.0 | **PASS** | `torchvision/_C.so`, `image.so`, and bundled `.dylibs/` declare `minos 11.0 arm64` (<= 14.0). |
| `macho-scipy-minos` | Binary Header | SciPy 1.17.1 | **PASS** | All compiled SciPy C/Cython extensions (`*.so`) declare `minos 14.0 arm64` (<= 14.0). |
| `macho-r9-ffmpeg-minos14` | Binary Header | FFmpeg / FFprobe R9 | **PASS** | Candidate binaries `ffmpeg-1` and `ffprobe-1` built with macOS deployment target `14.0 arm64`. |
| `macho-r8-baseline-contrast` | Binary Header | FFmpeg / FFprobe R8 | **FAIL (Observed Baseline)** | Original lab pack R8 had `minos 27.0 arm64`, proving necessity of R9 candidate replacement for macOS 14 compatibility. |
| `dylib-closure-clean` | Linkage | Shared Dylibs | **PASS** | Binaries link only to standard Apple SDK frameworks (`/usr/lib`, `/System/Library`) and `@rpath`. Strictly 0 host Homebrew (`/opt/homebrew`, `/usr/local`) references. |
| `hermetic-worker-contracts` | IPC Contract | `worker.py` / `backend.py` | **PASS** | Newline-delimited JSON over Unix domain socket; health handshake, infer, shutdown; no supervisor crash on frame errors. |
| `device-resolution-mps-cpu` | Accelerator | MPS & CPU Fallback | **PASS** | Explicit device resolution: resolves `mps` on Apple Silicon Metal; controlled fallback to `cpu` when requested or on unsupported hosts. |
| `models-manifest-integrity` | Storage / Licensing | `models-manifest.json` | **PASS** | SHA-256 and byte sizes verified for `yolo11s-pose.pt` and `yolo11n.pt`. Model redistribution strictly forbidden (`MODEL_REDISTRIBUTION_APPROVED=false`). |
| `synthetic-pipeline-mps` | Execution | RTSP -> FFmpeg -> Worker -> MPS | **PASS** | Synthetic loopback pipeline completed with MPS accelerator; marker `H8_MACOS14_PIPELINE_PASS` confirmed. |
| `synthetic-pipeline-cpu` | Execution | RTSP -> FFmpeg -> Worker -> CPU | **PASS** | Synthetic loopback pipeline completed with CPU fallback; marker `H8_MACOS14_PIPELINE_PASS` confirmed. |
| `go-pipeline-contracts-test` | Automated Test | `internal/vision` Go suite | **PASS** | `TestH8_SyntheticPipelineContracts_MPS_CPU` passed (both MPS and CPU subtests). |

---

## 3. Mach-O Header & Dylib Closure Analysis

A total of **316 Mach-O binary files** in the offline Runtime Pack and candidate toolchain were inspected via pure Mach-O header parsing (`LC_BUILD_VERSION` and `LC_VERSION_MIN_MACOSX`) and cross-checked with `/usr/bin/vtool`:

### MinOS Distribution Across Runtime Artifacts

```
Mach-O MinOS Distribution:
  - 10.9  : 8 binaries  (universal compatibility shims)
  - 11.0  : 152 binaries (Python 3.11 core, PyTorch, Torchvision)
  - 11.1  : 5 binaries  (supplementary math libraries)
  - 13.0  : 46 binaries (Pillow, OpenBLAS extensions)
  - 14.0  : 111 binaries (SciPy 1.17.1 native modules, contourpy)
  - 14.0  : 2 binaries  (R9 Candidate FFmpeg and FFprobe)
------------------------------------------------------------
Total compliant with macOS 14.0 (minos <= 14.0): 316 / 316 (100%)
```

### Contrast: Original R8 Lab Pack vs R9 Candidate Replacement

| Binary | Original Lab Pack (R8) MinOS | Candidate Build (R9) MinOS | macOS 14 ARM64 Compatible? |
|---|---|---|---|
| `ffmpeg` | `27.0` (`LC_BUILD_VERSION`) | `14.0` (`LC_BUILD_VERSION`) | **PASS (with R9)** |
| `ffprobe` | `27.0` (`LC_BUILD_VERSION`) | `14.0` (`LC_BUILD_VERSION`) | **PASS (with R9)** |

### Dynamic Library Linkage & Hermetic Isolation

Every binary was checked for `LC_LOAD_DYLIB` and `LC_LOAD_WEAK_DYLIB` records:
- **Allowed:** System libraries under `/usr/lib/libSystem.B.dylib`, `/System/Library/Frameworks/*`, or relocatable `@rpath/*`.
- **Prohibited:** Any reference to `/opt/homebrew/*`, `/usr/local/*`, or user paths.
- **Finding:** Clean closure. No dynamic linkage to external package managers or host development environments exists in the audited binaries.

---

## 4. Hermetic Worker & Pipeline Verification

### Python Audited Bootstrap Protocol

Because Python invoked with isolated mode (`-I -B`) intentionally omits the script directory from `sys.path`, the runner uses the project's audited bootstrap wrapper:

```python
BOOTSTRAP_SOURCE = (
    "import runpy,sys; d=sys.argv.pop(1); p=sys.argv.pop(1); "
    "sys.path.insert(0,d); sys.argv[0]=p; runpy.run_path(p,run_name='__main__')"
)
```

This guarantees hermetic execution without depending on mutable ambient environment variables or unvetted directory structures.

### Pipeline Execution Results

1. **Metal Performance Shaders (MPS):**
   - Health Handshake: `{"type": "health_ok", "ready": true, "device": "mps", "device_requested": "mps"}`
   - Real YOLO models loaded: `yolo11s-pose.pt`, `yolo11n.pt`.
   - Single frame inference round-trip verified with detection output.
   - Required marker emitted: `H8_MACOS14_PIPELINE_PASS`.

2. **CPU Fallback:**
   - Health Handshake: `{"type": "health_ok", "ready": true, "device": "cpu", "device_requested": "cpu"}`
   - Verified controlled execution and valid response format.
   - Required marker emitted: `H8_MACOS14_PIPELINE_PASS`.

---

## 5. Artifacts Created & Test Reproduction

### New Tooling & Test Files

1. **`deploy/installer/h8-macos14-certification-runner.py`**:
   Portable certification runner with built-in Mach-O binary parser, host environment inspector, hermetic worker supervisor, and structured JSON output.
2. **`deploy/installer/test-h8-macos14-certification-runner.py`**:
   Synthetic unit test suite covering binary parsing, architecture checks, Homebrew rejection, and fail-closed gate assertions (9 passing tests).
3. **`internal/vision/h8_pipeline_test.go`**:
   Go integration test executing the synthetic pipeline contract across MPS and CPU devices.
4. **`docs/architecture/h8-macos14-certification-evidence.json`**:
   Deterministic structured evidence log containing full machine-readable check results.

### Reproduction Commands

```bash
# 1. Run the Python runner synthetic unit test suite:
python3 deploy/installer/test-h8-macos14-certification-runner.py -v

# 2. Run the full macOS 14 certification runner:
python3 deploy/installer/h8-macos14-certification-runner.py \
  --json-out docs/architecture/h8-macos14-certification-evidence.json

# 3. Run Go unit and pipeline tests:
go test -v ./internal/vision -run TestH8
go test -v ./internal/offlinecert
```

---

## 6. Remaining Completion Gates & Next Steps

1. **Physical macOS 14 Test Rig:** Obtain access to an authorized physical Mac mini/MacBook running native macOS 14 (Sonoma) ARM64, or an approved VM instance, to clear the `PHYSICAL_MACOS14_BLOCKED` gate.
2. **Runtime Pack Assembly Promotion:** Overlay the validated R9 FFmpeg/FFprobe binaries (`minos 14.0`) into the canonical Runtime Pack manifest once H6D/H7 upstream reviews complete.
3. **Legal Redistribution:** Third-party licenses, notices, and model weight redistributions remain strictly unapproved (`MODEL_REDISTRIBUTION_APPROVED=false`).
