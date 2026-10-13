# Full Edge Self-Contained Runtime & Model Architecture

## 1. Executive Summary

Full Edge allows GEO CAM edge appliances and desktop hosts to execute local computer vision inference (person detection, pose estimation, vehicle detection, ANPR) on edge hardware (macOS Apple Silicon, Linux amd64, Linux arm64) completely offline, without reliance on cloud inference or runtime internet connectivity during installation and commissioning.

**Core Objective:** An end user or non-technical installer operator must be able to complete "Preparar Full Edge" after a single installer download without:
- Host-installed Python.
- Host-installed pip.
- Homebrew or system package managers (`apt`, `yum`, `brew`).
- PyPI access (`files.pythonhosted.org`).
- Internet connection during runtime provisioning.
- Terminal intervention or shell scripts (`sh -c`, `bash -c`, `curl | sh` are strictly forbidden).
- Manual compilation or wheel building.
- Manual model downloading.

Commercial model weights remain strictly gated behind `MODEL_REDISTRIBUTION_APPROVED=false` until redistribution rights are legally vetted. The runtime infrastructure itself, however, must be 100% self-contained and offline-ready.

---

## 2. Audit of Existing Components (Tarea A)

| Component | Real Location | Current Implementation | Validated State | External Dependencies | Limitations & Gaps | Reusable Assets | Required Changes |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **VisionRuntimeResolver** | `internal/installer/vision_runtime.go` | Host Python search (`findBasePython`), venv detection, `probePython` (dynamic import checks), model manifest inspection. | Unit tests in `vision_runtime_test.go` (MPS/CPU/CUDA probing, version matching). | Relies on host-installed Python (`/usr/bin/python3`, `/opt/homebrew/...`). | If host lacks compatible Python 3.8–3.13 (e.g. only 3.14 or fresh Mac), blocks with `can_provision=false`. | Device probing logic (MPS/CUDA/CPU), state structs (`VisionRuntimeStatus`). | Add resolution of bundled/embedded Python runtime before host fallback. |
| **VisionRuntimeProvisioner** | `internal/installer/vision_runtime.go` | Creates `$GEOCAM_DATA_DIR/vision-runtime` via `basePy -m venv`, upgrades pip, and runs `pip install -r requirements.txt`. | Staging worker assets, transient pip timeout retry logic (`isTransientPipError`). | **PyPI via Internet**. Requires live connection to `files.pythonhosted.org`. | Fails on slow networks or air-gapped hosts with `PIP_TIMEOUT` or read timeouts. | Staging lifecycle, timeout management, config persistence in `edge.env`. | Replace online `pip install` with offline wheelhouse installation (`--no-index --find-links`) or pre-assembled runtime unpacking. |
| **Python Finder & Managed Venv** | `internal/installer/vision_runtime.go` | Candidate scoring (Homebrew over system `/usr/bin/python3`), version compatibility verification. | Tests verifying scoring priority and rejection of incompatible Python. | Host OS PATH and system directories. | On fresh macOS without Xcode CLT, `/usr/bin/python3` triggers an OS installation modal. | Venv health verification (`isVenvHealthy`). | Embed an isolated Python binary (darwin-arm64 standalone) that does not mutate or depend on host system Python. |
| **Vision Dependencies** | `deploy/vision-worker/requirements.txt` & `internal/installer/assets/vision-worker/requirements.txt` | Loose ranges: `ultralytics>=8.3,<9`, `pillow>=10,<12`. | Worker imports verified in Python tests. | Dynamic PyPI dependency resolution (pulls torch, torchvision, polars, opencv, numpy, etc.). | Open ranges cause non-deterministic dependency resolution and multi-hundred MB downloads. | Required package list. | Pinned exact versions and SHA-256 hashes inside a locked manifest. |
| **Models Manifest** | `internal/installer/assets/models-manifest.json` | Pinned JSON with `pack_version`, file sizes, and SHA-256 hashes for `yolo11s-pose.pt` and `yolo11n.pt`. | `LoadModelsManifest()`, embedded in binary via `embed.FS`. | None (compiled into binary). | Only covers model files; does not yet specify runtime dependencies, wheels, or Python version. | Manifest structure, hash verification schema. | Expand or pair with `runtime-manifest.json` for the complete Runtime Pack. |
| **Model Verification & Self-Heal** | `internal/installer/vision_runtime.go` | `ValidateModelFile` (checks regular file, size >= min, sha256). `SelfHealModel` performs atomic copy (temp file + fsync + hash check + `os.Rename`). | Unit tests detecting truncated/20-byte stub files and verifying atomic repair. | None. | Requires valid source model in bundle or operator import. | Complete 4-state lifecycle (`VALID`, `MISSING`, `CORRUPTED`, `INCOMPATIBLE`) and atomic swap pattern. | Reuse this exact atomic validation pattern for runtime components. |
| **Vision Worker** | `deploy/vision-worker/worker.py`, `backend.py` | Out-of-process supervisor (`internal/vision.Worker`) speaking newline-delimited JSON over Unix domain socket. Supports MPS/CUDA/CPU. | Unit & integration tests in `deploy/vision-worker/tests/` and `internal/vision/`. | Python interpreter and PyTorch/Ultralytics environment. | Must be spawned by an external interpreter configured in `edge.env`. | Complete socket protocol, worker scripts, and Go supervisor. | Bundle fixed worker release versioned against the Runtime Pack. |
| **FFmpeg Packaging** | `internal/installer/ffmpeg.go`, `deploy/appliance/scripts/build-ffmpeg-static.sh` | Resolves ffmpeg/ffprobe via Path, Homebrew, Managed, or System. On macOS offers `brew install ffmpeg`. | Complete test suite in `internal/installer/ffmpeg_test.go`. | On macOS requires Homebrew or pre-installed system FFmpeg. | Users without Homebrew cannot obtain FFmpeg without terminal intervention. | Binary validation (`ValidateExecutable`), version parser, paired ffprobe detection. | Bundle precompiled, verified static `ffmpeg` and `ffprobe` for macOS arm64. |
| **Release Assembly** | `deploy/installer/assemble-release.sh`, `.github/workflows/installer-build.yml` | Packages 4 platform artifacts, verifies binary types and SHA256SUMS. Gated by `MODEL_REDISTRIBUTION_APPROVED`. | CI workflow `Installer Build Validation`, `assemble_test.go`. | GitHub Actions runners. | Release package does not bundle Python, wheels, or FFmpeg; only includes the Go binaries and embedded assets. | Fail-closed architecture, hash verification, gate enforcement. | Update packaging pipeline to assemble platform-specific Runtime Packs. |
| **UI & Installer Backend** | `cmd/geocam-edge-ui/`, `internal/installer/service.go` | Wails v2 desktop app, commissioning wizards, "Preparar Full Edge" action, status reporting. | Wails build, launch check (15s), Go unit tests. | Webview2 / WebKit. | Shows generic timeout errors when PyPI download fails; does not indicate offline bundle status. | State machine, event emission, frontend components. | Add offline runtime status indicators and progress reporting during local unpack. |
| **Configuration Persistence** | `internal/config/persistent.go`, `config.go` | Reads/writes `edge.env` in `$GEOCAM_DATA_DIR` atomically. | Unit tests in `internal/config/`. | None. | None. | `WritePersistentValues`, atomic write + fsync + rename. | Persist canonical runtime paths (`GEOCAM_EDGE_YOLO_WORKER_CMD`, `GEOCAM_VIDEO_FFMPEG_PATH`). |

---

## 3. Runtime Pack Architecture (Tarea B)

### 3.1 Directory Layout

The Runtime Pack is a versioned, self-contained bundle packaged alongside or embedded within the desktop installer:

```
runtime/
├── manifest.json              # Signed metadata, exact dependency pins, hashes
├── python/                    # Standalone, relocatable Python interpreter (macOS arm64)
│   ├── bin/python3
│   ├── lib/
│   └── ...
├── wheelhouse/                # Offline wheels with exact version pins and verified SHA-256
│   ├── torch-2.4.0-*.whl
│   ├── torchvision-0.19.0-*.whl
│   ├── ultralytics-8.3.1-*.whl
│   ├── pillow-10.4.0-*.whl
│   └── ...
├── worker/                    # Pinned Vision Worker scripts
│   ├── worker.py
│   ├── backend.py
│   └── requirements.txt
├── ffmpeg/                    # Standalone static multimedia binaries
│   ├── ffmpeg
│   └── ffprobe
├── models/                    # Model infrastructure & authorized weights (or manifest)
│   ├── models-manifest.json
│   └── yolo11s-pose.pt (when MODEL_REDISTRIBUTION_APPROVED=true)
└── licenses/                  # Third-party license texts (LGPL, BSD, Apache, MIT)
    ├── PYTHON.txt
    ├── FFMPEG.txt
    ├── PYTORCH.txt
    └── ULTRALYTICS.txt
```

### 3.2 Runtime Manifest Schema (`runtime-manifest.json`)

```json
{
  "$schema": "https://geocam.io/schemas/runtime-manifest-v1.json",
  "runtime_version": "1.0.0",
  "platform": "darwin",
  "architecture": "arm64",
  "python": {
    "version": "3.11.9",
    "abi": "cp311",
    "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "relative_binary_path": "python/bin/python3"
  },
  "worker": {
    "version": "1.1.0",
    "entrypoint": "worker/worker.py",
    "sha256": "a1b2c3d4..."
  },
  "ffmpeg": {
    "version": "7.1.0",
    "ffmpeg_sha256": "b2c3d4e5...",
    "ffprobe_sha256": "c3d4e5f6..."
  },
  "compatibility": {
    "edge_min_version": "1.1.6",
    "models_pack_min_version": "1.0.0"
  },
  "dependencies": [
    {
      "name": "torch",
      "version": "2.4.0",
      "filename": "torch-2.4.0-cp311-none-macosx_11_0_arm64.whl",
      "sha256": "d4e5f6...",
      "size_bytes": 65432100
    },
    {
      "name": "ultralytics",
      "version": "8.3.1",
      "filename": "ultralytics-8.3.1-py3-none-any.whl",
      "sha256": "e5f6a7...",
      "size_bytes": 789100
    }
  ]
}
```

### 3.3 Packaging Decision: Wheelhouse vs. Pre-assembled Python Runtime

#### Comparison & Tradeoff Analysis (macOS Apple Silicon focus)

1. **Pure Wheelhouse (`wheelhouse/` + host Python):**
   - *Advantage:* Wheels are standard format; smaller payload if host already has Python.
   - *Fatal Flaw on Target Platform:* On a fresh macOS install, `/usr/bin/python3` is a stub pointing to the Apple Xcode Command Line Tools shim. Invoking it launches an operating system dialog prompting for developer tools installation, blocking automated setup. If the host has Homebrew Python 3.14 (unsupported by PyTorch wheels), wheel installation fails immediately.
   - *Conclusion:* Unacceptable as the sole mechanism.

2. **Pre-assembled Virtual Environment (Copied raw `venv/`):**
   - *Advantage:* Fast extraction.
   - *Fatal Flaw:* Python virtual environments embed hardcoded absolute paths in shebang scripts (`#!/Users/.../bin/python`) and dynamic linker configurations. Copying a raw `venv` between user directories or machines breaks script execution and library linking unless complex path patching is performed.
   - *Conclusion:* Fragile and prone to silent relocation bugs.

3. **Recommended Architecture: Standalone Portable Python + Pinned Wheelhouse Bootstrap (Hermetic Bundle):**
   - **Intérprete Standalone:** Embed a verified, relocatable Python 3.11 standalone build (such as `python-build-standalone` for `aarch64-apple-darwin`) inside `runtime/python/`.
   - **Local Isolated Provisioning:** During "Preparar Full Edge", the installer uses this bundled Python to create or populate an isolated runtime at `$GEOCAM_DATA_DIR/vision-runtime`.
   - **Offline Pip Install:** The bundled `pip` is invoked with strict offline flags:
     ```bash
     $GEOCAM_DATA_DIR/vision-runtime/bin/python -m pip install \
       --no-index \
       --find-links $BUNDLE_DIR/runtime/wheelhouse \
       -r $BUNDLE_DIR/runtime/worker/requirements.txt
     ```
   - **Zero Internet Requirement:** PyPI is never contacted (`--no-index`). No compilation occurs (wheels are pure binary for `macosx_11_0_arm64`).
   - **Zero Host Mutation:** The system Python, user libraries, and global environment remain completely untouched.

---

## 4. Integrity Verification & Self-Healing Protocol

### 4.1 Component Integrity States

Every critical component in the runtime adheres to a strict 4-state lifecycle:
- **`VALID`**: File exists, permissions correct, SHA-256 matches manifest exactly, binary passes smoke test.
- **`MISSING`**: Expected file is absent from the target directory.
- **`CORRUPTED`**: File exists but size is zero/truncated, SHA-256 mismatch, or binary execution fails.
- **`INCOMPATIBLE`**: File is valid but targets an incompatible architecture (e.g. x86_64 binary on Apple Silicon) or ABI version mismatch.

### 4.2 Atomic Staging and Activation Protocol

To prevent corrupted or half-written runtimes:
```
1. Extraction & Preparation:
   Extract to temporary staging directory:
   $GEOCAM_DATA_DIR/vision-runtime.tmp.<pid>

2. Cryptographic Verification:
   Verify SHA-256 checksum of every extracted binary and wheel against manifest.json.
   If any check fails: delete staging directory and fail closed.

3. In-Place Self-Test:
   Run probe execution using the staged interpreter:
   python -c "import torch, ultralytics; assert torch.backends.mps.is_available()"
   If execution fails: delete staging directory and fail closed.

4. Atomic Swap:
   If existing $GEOCAM_DATA_DIR/vision-runtime exists:
     Rename to $GEOCAM_DATA_DIR/vision-runtime.backup.<timestamp>
   Rename $GEOCAM_DATA_DIR/vision-runtime.tmp.<pid> -> $GEOCAM_DATA_DIR/vision-runtime
   Remove backup directory.

5. Recovery / Rollback:
   If atomic rename fails:
     Restore previous backup directory to $GEOCAM_DATA_DIR/vision-runtime.
```

---

## 5. FFmpeg Bundled Strategy

1. **Current State:** The desktop installer searches `/opt/homebrew/bin/ffmpeg`, `/usr/local/bin/ffmpeg`, and PATH. On macOS it provides a UI button calling `brew install ffmpeg`.
2. **Offline Target:**
   - On macOS Apple Silicon, bundle static `ffmpeg` and `ffprobe` binaries compiled with minimal required codecs (H.264, H.265, AAC, RTSP) under `runtime/ffmpeg/`.
   - Validate executable permissions, architecture (`Mach-O 64-bit arm64`), and SHA-256 hashes against `runtime-manifest.json`.
   - Persist path to `GEOCAM_VIDEO_FFMPEG_PATH` in `edge.env`.
   - If bundled FFmpeg is present, the installer selects it immediately with `Source: bundled` and bypasses Homebrew detection.

---

## 6. Commercial Licensing Gate (`MODEL_REDISTRIBUTION_APPROVED`)

Model weights (e.g. Ultralytics YOLO models) are subject to specific licensing conditions (AGPL-3.0 vs commercial redistribution licenses).

### Policy & Contracts
```bash
MODEL_REDISTRIBUTION_APPROVED=false # Enforced default in all builds and CI
```

- When `MODEL_REDISTRIBUTION_APPROVED=false`:
  - The offline installer packages the **complete runtime**: Python standalone, wheels, FFmpeg, worker scripts, and `models-manifest.json`.
  - Binary weights (`.pt`) are **not** bundled into the release artifact.
  - The installer UI displays: `Runtime Offline: Listo`, `Modelos: Pendiente de importación`, and allows atomic operator import via `ImportModel`.
  - Milestone status is accurately reported:
    - `RUNTIME_OFFLINE_READY=true`
    - `MODEL_PACK_RELEASE_BLOCKED=true`
- When `MODEL_REDISTRIBUTION_APPROVED=true`:
  - Authorized model weights are placed in `runtime/models/` and packaged into the release archive, completing 100% zero-touch provisioning.

---

## 7. Diagnostic Safety & Privacy

All error reporting and health inspection strings must pass through `SanitizeError`:
- No raw RTSP credentials or tokens exposed in logs or UI.
- No system paths containing sensitive user names leaked to SaaS telemetry.
- Safe structured error codes (`RUNTIME_INTEGRITY_FAILED`, `OFFLINE_PACKAGE_NOT_FOUND`, `MODEL_CORRUPTED`) with localized, actionable user guidance.

---

## 8. Cryptographic Verification Contract & TOCTOU Guarantees (Milestone H1)

Implemented in `internal/installer/runtime_manifest.go` with full test coverage in `internal/installer/runtime_manifest_test.go`:

1. **Manifest Schema & Specification:**
   - Enforces `schema_version = "1.0.0"`.
   - Strict validation of platform (`darwin`, `linux`, `windows`) and architecture (`arm64`, `amd64`).
   - Exact pinned versions for all dependencies (prohibiting open ranges like `>=`, `<=`, `~`, `^`, `*`).
   - Standard 64-character lowercase/uppercase hexadecimal SHA-256 digests.
   - Size-in-bytes positive constraints (`size_bytes > 0`).
   - Duplicate normalization (`canonicalizeManifestPath`): rejects duplicate paths written with different syntax (e.g. `./bin/ffmpeg`, `bin//ffmpeg`, `bin/foo/../ffmpeg`), duplicate filenames, and cross-spec collisions (e.g. worker entrypoint colliding with Python binary path).

2. **Security, Root Canonicalization & Component-by-Component Path Confinement:**
   - **Root Canonicalization:** The runtime root directory is resolved via `filepath.EvalSymlinks(absRoot)`. Broken symlinks or symlink cycles in the root path fail closed immediately (`ROOT_DIR_ERROR`).
   - **Component-by-Component Physical Resolution:** Rather than inspecting only the final leaf node, validation evaluates every intermediate directory component. If any parent directory (e.g. `runtime/python -> /tmp/external-python`) is an external symlink, the escape is detected immediately and rejected with `ErrArtifactSecurity` (`SECURITY_ESCAPING_SYMLINK`).
   - **Confinement via Component Validation:** Confinement checks use `filepath.Rel` decomposed into path elements, strictly avoiding flawed string prefix comparisons (which would falsely allow `/app/runtime_escape` under `/app/runtime`).
   - **Legitimate Internal Symlinks Preserved:** Symlinks pointing strictly within the authorized canonical root (both intermediate directory links and leaf files) are permitted, provided the resolved target is a regular file matching the pinned size and SHA-256.
   - **Broken & Cyclic Link Rejection:** Broken links and cyclic symlink loops (`linkA -> linkB -> linkA`) fail closed with `ErrArtifactSecurity` (`SECURITY_BROKEN_SYMLINK`).

3. **Streaming Verification:**
   - File checksum calculation uses streaming `io.Copy` into `crypto/sha256.New()`, avoiding buffering entire large archives (e.g., PyTorch wheel or Python binary) in memory.

4. **TOCTOU (Time-of-Check to Time-of-Use) Architectural Guarantee:**
   - **Explicit Limitation at Inspection Time:** Path validation and physical canonicalization in `ValidateArtifacts` guarantee strict confinement, integrity, and safety at inspection time. However, filesystem validation alone does not eliminate Time-of-Check to Time-of-Use (TOCTOU) race conditions if an untrusted concurrent process modifies files after inspection.
   - **Activation Guarantee (Milestone H6):** Strong activation integrity will be guaranteed in Milestone H6 (Staging & Activation):
     - Unpacking and validation MUST occur in an isolated temporary staging directory (`$GEOCAM_DATA_DIR/vision-runtime.tmp.<pid>`) with restrictive permissions (`0700`).
     - Validation must pass completely before activation.
     - Activation is executed via atomic filesystem rename (`os.Rename`), ensuring the running service only ever consumes a fully verified, sealed runtime directory.
     - Binaries are marked read-only (`0555` / `0444`) post-staging.

---

## 9. Embedded Python Standalone Architecture (Milestone H2)

Implemented in `internal/installer/embedded_python.go` with full test coverage in `internal/installer/embedded_python_test.go` and tooling in `deploy/installer/fetch-runtime-deps.sh`:

### 9.1 Pinned Python Distribution & Provenance
- **Distribution:** `astral-sh/python-build-standalone` (CPython 3.11.9 install-only standalone).
- **Release Tag:** `20240415`.
- **Target Platform & Architecture:** `darwin/arm64` (Apple Silicon M1, M2, M3, M4).
- **macOS Minimum Version:** macOS 11.0 Big Sur (`minos 11.0`, SDK 14.2).
- **License:** Python Software Foundation License (PSF-2.0, permissive, fully redistributable).
- **Relocatability:** Compiled with `@executable_path/../lib/libpython3.11.dylib` and dynamic linking against system frameworks (`CoreFoundation`, `libSystem.B.dylib`), with zero dependencies on Homebrew, Xcode CLT, or host Python.
- **Archive Cryptographic Pinned Metadata:**
  - URL: `https://github.com/astral-sh/python-build-standalone/releases/download/20240415/cpython-3.11.9%2B20240415-aarch64-apple-darwin-install_only.tar.gz`
  - Archive Size: `17915401` bytes.
  - Archive SHA-256: `7af7058f7c268b4d87ed7e08c2c7844ef8460863b3e679db3afdce8bb1eedfae`.
- **Physical Binary Pinned Metadata:**
  - Relative Path: `python/bin/python3` (symlink) -> `python/bin/python3.11`.
  - Binary Size: `50168` bytes.
  - Binary SHA-256: `906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170`.
  - Mach-O Format: Mach-O 64-bit executable arm64 (`CPU_TYPE_ARM64 = 0x0100000c`).

### 9.2 Subprocess Isolation Contract (`IsolateEnv`)
To ensure the standalone interpreter executes completely isolated from host configuration:
1. **Environment Scrubbing:**
   - Strips all `PYTHON*` variables (`PYTHONHOME`, `PYTHONPATH`, `PYTHONSTARTUP`, `PYTHONUSERBASE`, `PYTHONEXECUTABLE`, `PYTHONSAFEPATH`, `PYTHONOPTIMIZE`, `PYTHONDEBUG`, etc.).
   - Strips `VIRTUAL_ENV` and `__PYVENV_LAUNCHER__`.
   - Injects `PYTHONNOUSERSITE=1`.
2. **PATH Sanitization:**
   - Filters `PATH` segments, removing Homebrew (`/opt/homebrew`, `/usr/local/bin`, `/usr/local/sbin`, `Cellar`), Conda, miniconda, anaconda, pyenv, asdf, nvm, and cargo.
   - Preserves only clean base system directories (`/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`).
3. **Execution Flags:**
   - Subprocess invocations pass isolated mode flags (`-I`) ensuring `sys.path` does not include user directories or host site-packages.
4. **Standard Library Verification:**
   - `VerifyStandardLibrary` executes an in-process verification script asserting that core standard library modules (`os`, `json`, `hashlib`, `platform`, `math`, `re`, `time`) resolve strictly within `sys.prefix`, never from host paths.

### 9.3 Packaging Tooling (`deploy/installer/fetch-runtime-deps.sh`)
- Reproducible, fail-closed fetching and staging tool.
- Validates archive SHA-256 before extraction.
- Invokes dedicated secure extractor (`cmd/runtime-extractor` via Go) instead of raw `tar -xzf`.
- Stages within `$TARGET_DIR/.python.tmp.$$` on the exact same filesystem.
- Sets executable permissions (`chmod -R +x python/bin`).
- Moves atomically into destination runtime path with rollback on failure.
- Supports `--verify-only` for offline build verification.

### 9.4 Physical vs. Synthetic Validation Distinction
- **Synthetic Test Fixtures:** 20 table-driven unit tests verify error handling (missing file, corrupted hash, truncated size, non-executable bits, wrong Mach-O architecture, wrong version, incompatible ABI, incomplete stdlib, escaping symlinks, execution timeout, air-gapped readiness, path relocatability, and paths with spaces) using synthetic fixtures without requiring large binaries in git.
- **Physical Verification:** `TestEmbeddedPython_RealPythonExecution` executes against real CPython 3.11.9 Standalone Apple Silicon Mach-O binary, confirming probe execution, JSON serialization, and standard library hermetic loading.

### 9.5 Security Hardening, State Machine & Rollback Safety Architecture
To guarantee absolute reliability against archive corruption, filesystem errors, and process interruptions:
1. **Entry-by-Entry Pre-Validation (`internal/installer/secure_extractor.go`):**
   - Rejects absolute paths (`/usr/bin`, `\windows\sys`).
   - Rejects path traversal components (`..`, `../`, `/../`).
   - Rejects non-regular file types (device files, fifos, sockets).
   - Enforces tar bomb resource constraints: 1 GB single file limit (`MaxFileSize`), 2 GB cumulative extracted bytes (`MaxTotalBytes`), and 50,000 maximum entries (`MaxEntries`).
   - Verifies symlink targets: rejects absolute targets and targets that resolve outside the staging root.
   - Refuses to write through existing symlinks (`os.Remove` before file creation).
   - Validates hardlink targets to ensure they reference pre-existing files within staging.
2. **Explicit State Machine in Activation Script (`deploy/installer/fetch-runtime-deps.sh`):**
   - States: `INIT` -> `PREPARING` -> `STAGED` -> `BACKING_UP` -> `BACKED_UP` -> `ACTIVATING` -> `ACTIVATED` -> `VERIFYING` -> `COMMITTED`.
   - Error states: `ROLLING_BACK`, `ROLLED_BACK`, `ROLLBACK_FAILED`.
   - Staging occurs strictly in sibling hidden directory `$TARGET_DIR/.python.tmp.<pid>` on the exact same filesystem.
   - Existing runtime is preserved in `$TARGET_DIR/.python.old.<pid>`.
   - **Backup Preservation Guarantee:** Previous backup is NEVER deleted until post-activation verification succeeds (`STATE=COMMITTED`).
   - **Rollback Resilience:** If activation or post-activation verification fails, rollback restores the backup. If rollback itself fails, the backup directory is SAFELY PRESERVED on disk and its path and recovery command are logged (`STATE=ROLLBACK_FAILED`).
   - **Signal Shielding:** Signal traps (`INT`, `TERM`, `HUP`, `EXIT`) trigger automated rollback during the transition window and prevent destructive double cleanup (`IN_CLEANUP` re-entrancy lock).
3. **Atomic Rename vs. Two-Step Directory Swap Distinction:**
   - In POSIX filesystems without atomic exchange syscalls (e.g. Darwin), directory replacement requires a two-step sequence: `mv target backup` followed by `mv staging target`.
   - Between step 1 and step 2, target is momentarily absent.
   - Mitigations: (1) Staging is verified 100% before step 1, (2) signals are trapped to immediately restore backup, (3) backup is kept until post-activation verification succeeds, and (4) unrecovered backups are preserved if restoration fails.
   - Note for Milestone H6: Full upgrade system in H6 will evaluate versioned directories with single-pointer symlink swapping (`ln -sfn ... && mv -f link target`).
4. **Template Manifest vs. Release Separation:**
   - Embedded manifest `assets/runtime-manifest.json` specifies `"manifest_type": "template"` to indicate that dependent components (wheels in H3, worker/ffmpeg in H4) are unbuilt specifications and not ready-for-inference releases.
   - Formalized state: `PYTHON_EXECUTABLE` indicates interpreter availability, but explicitly does NOT imply `RUNTIME_READY` or `INFERENCE_READY`.

### 9.6 Adversarial Test Suite Coverage (`secure_extractor_test.go` & `test-rollback-safety.sh`)
Twelve mandatory rollback safety failure modes are validated via deterministic fault injection (`deploy/installer/test-rollback-safety.sh`):
1. **Case 1 (Fresh install):** Successful clean installation without leftover temporary files.
2. **Case 2 (Valid previous runtime update):** Clean upgrade with backup deleted post-commit.
3. **Case 3 (Error before activation):** Pre-activation failure leaves target untouched and cleans staging.
4. **Case 4 (Error moving old runtime):** Failure backing up preserves existing runtime.
5. **Case 5 (Error activating new runtime):** Activation failure triggers automatic rollback and restores valid runtime.
6. **Case 6 (Error restoring backup):** Restoration failure preserves backup directory intact with manual recovery instructions.
7. **Case 7 (Error in final verification):** Post-activation verification failure triggers rollback and restores previous runtime.
8. **Case 8 (Interruption between renames):** SIGINT between renames is trapped, initiating rollback and restoring valid runtime.
9. **Case 9 (Interruption during cleanup):** Trap handler prevents re-entrancy and double cleanup crashes.
10. **Case 10 (Repeated execution):** Idempotent execution skips redundant work when runtime is already verified.
11. **Case 11 (Exact cryptographic preservation):** Canary files and dylibs remain bit-for-bit identical after rollback.
12. **Case 12 (Absence of deletion of unrecovered backups):** Failed restoration never prunes backup on process exit.

Go unit test suite in `secure_extractor_test.go` (14 cases) validates entry confinement, tar bomb bounds, same-filesystem staging, post-activation validation rollback (`Case 13`), and backup preservation upon rollback failure (`Case 14`).




