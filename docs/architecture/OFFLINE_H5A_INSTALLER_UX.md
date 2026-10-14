# Offline H5A — Full Edge Installer UX & Preparation State Contract

## Scope and status

H5A implements the desktop installer user interface and backend preparation state contract for Full Edge offline provisioning. It establishes a typed Go/Wails boundary and a clear, non-technical operator interface for the "Preparar Full Edge" action.

H5A does **not** declare H5 complete, `RUNTIME_READY`, or `INFERENCE_READY`. It does not provision a live Runtime Pack, execute pip/curl/docker/brew commands, launch the Vision Worker, or connect to cameras. Production builds use the fail-closed `UnavailableFullEdgePreparation` adapter until H4/H6 dependencies are merged.

## Installer seam & architecture

The desktop installer is a Wails bridge in `cmd/geocam-edge-ui/app.go`, with a React frontend under `cmd/geocam-edge-ui/frontend`.
- Legacy setup paths (`GetVisionRuntimeStatus`, `PrepareVisionRuntime`, `GetFFmpegStatus`, `InstallFFmpeg`) remain intact for backward compatibility under an expandable "Detalles avanzados" section, clearly labeled as uncertified legacy host setup.
- The new Full Edge preparation panel is visible when the operator selects Full Edge processing mode. It communicates exclusively through the typed `installer.FullEdgePreparationAPI` interface without shelling out to package managers or network tools.

## Backend preparation state contract

The lifecycle is modeled by an explicit 8-state machine:

| State | Meaning | User action / Availability |
| --- | --- | --- |
| `NOT_PREPARED` | Runtime Pack not yet prepared or inspected | "Preparar Full Edge" available if pack detected |
| `CHECKING` | Active inspection of environment, pack manifests, and prerequisites | Wait; cancellation allowed |
| `PREPARING` | Active hermetic unpacking, wheelhouse installation, and worker setup | Wait; safe cancellation allowed |
| `VERIFYING` | Active integrity validation, media checks, model authorization, and smoke test | Wait; safe cancellation allowed |
| `READY` | Terminal state backed by complete, valid backend `RuntimeCertification` | Read-only certified status |
| `RECOVERY_REQUIRED` | Previous interrupted run left an un-reconciled backup or required recovery | "Recuperar runtime anterior" available |
| `FAILED` | Preparation failed (recoverable error or blocked prerequisite) | "Reintentar" if retryable; diagnostic available |
| `CANCELLED` | User cancelled operation; prior runtime preserved intact | "Reintentar" available |

### State transitions

Allowed transitions are strictly enforced by `CanTransitionPreparation`:
- `NOT_PREPARED` → `CHECKING`, `PREPARING`, `FAILED`
- `CHECKING` → `NOT_PREPARED`, `PREPARING`, `VERIFYING`, `READY`, `RECOVERY_REQUIRED`, `FAILED`, `CANCELLED`
- `PREPARING` → `VERIFYING`, `FAILED`, `CANCELLED`, `RECOVERY_REQUIRED`
- `VERIFYING` → `READY`, `FAILED`, `CANCELLED`, `RECOVERY_REQUIRED`
- `READY` → `CHECKING` (on explicit operator re-check only)
- `RECOVERY_REQUIRED` → `PREPARING`, `FAILED`
- `FAILED` → `CHECKING`, `PREPARING` (retry), `RECOVERY_REQUIRED`
- `CANCELLED` → `CHECKING`, `PREPARING`, `NOT_PREPARED`, `RECOVERY_REQUIRED`

Direct illegal transitions (e.g. `NOT_PREPARED` → `READY`, `FAILED` → `READY`, `READY` → `PREPARING`) fail closed and are rejected.

## Per-component progress reporting

In addition to overall progress (0–100%), the contract reports real-time health across six runtime subsystems:
1. **Python**: Python 3.11 Standalone (macOS arm64).
2. **Wheelhouse**: Hermetic binary wheels (PyTorch, Torchvision, Ultralytics, Pillow).
3. **Worker**: Vision Worker package & IPC interface.
4. **FFmpeg**: Bundled static FFmpeg binary.
5. **FFprobe**: Bundled static FFprobe binary.
6. **Modelos autorizados**: Detection models (license authorization & weights integrity).

Each component reports a `ComponentStatus`:
- `PENDING`
- `IN_PROGRESS`
- `READY`
- `FAILED`

## Readiness gates

The `READY` state is strictly gated by `CertifiedReady()`:
1. Snapshot state must be `READY`.
2. Overall progress must be exactly 100%.
3. Every reported component must report `READY`.
4. Backend `RuntimeCertification` must be non-nil and pass all 16 cryptographic and operational validations:
   - Non-empty `CertificationID`, `RuntimePackVersion`, `Platform`, `ModelPackVersion`.
   - Valid 64-character SHA-256 digests for `RuntimeManifestSHA256` and `ModelManifestSHA256`.
   - `EvidenceLevel == "verified"`.
   - `RuntimePackVerified == true`, `DependenciesVerified == true`, `VisionWorkerReady == true`.
   - `FFmpegVerified == true`, `FFprobeVerified == true`.
   - `ModelAuthorized == true` (strictly rejects unauthorized models / missing license approvals).
   - `ModelIntegrityVerified == true`.
   - `InferenceSmokeTestPassed == true`.
   - Non-zero `CertifiedAt` timestamp.

Readiness cannot be simulated by the UI, inferred from Python/dependencies presence alone, or triggered by a client timer.

## Concurrency, idempotency & safety

- **In-process single-flight**: `PreparationStartGate` uses a mutex to serialize concurrent callers. Subsequent `Start` calls with the *same* `requestID` succeed idempotently; calls with a *different* `requestID` while an operation is active are rejected with `ErrPreparationInProgress`.
- **Cancellation**: Safe cancellation is allowed only while preparation is active (`CHECKING`, `PREPARING`, `VERIFYING`). It preserves the existing runtime and reverts to `CANCELLED`.
- **Recovery**: Interrupted executions that leave pending backups trigger `RECOVERY_REQUIRED`, enabling non-destructive restoration via `Recover`.
- **Diagnostics sanitization**: Technical diagnostics redact absolute host filesystem paths (`[PATH]`), secret keywords (`[REDACTED]`), and stack trace patterns, preventing accidental leakage in non-technical UI views.

## Compatibility & dependencies

- H1–H3 contracts (RuntimeManifest, EmbeddedPython, Wheelhouse) are reused as merged.
- H4 (bundled FFmpeg and Vision Worker) is pending integration.
- H6 (model authorization, version compatibility, atomic upgrade/rollback) is pending integration.
- H7 (offline certification and physical validation harness) is pending integration.
