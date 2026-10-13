# Offline H5A — Full Edge Installer UX Contract

## Scope and status

H5A adds an opt-in desktop UI and a typed Go/Wails boundary for a future hermetic Full Edge preparation executor. It does not provision a Runtime Pack, install dependencies, launch a Vision Worker, or certify a runtime. The default adapter is deliberately unavailable and reports `PREPARATION_NOT_INTEGRATED`; no successful state is simulated. H5A is not H5 complete and does not establish `RUNTIME_READY`.

## Current installer seam

The desktop app is a Wails bridge in `cmd/geocam-edge-ui/app.go`, with React UI under `cmd/geocam-edge-ui/frontend`. Existing `GetVisionRuntimeStatus` / `PrepareVisionRuntime` and `GetFFmpegStatus` / `InstallFFmpeg` remain the legacy path and can inspect/use host Python, pip, or platform package management. The H5A adapter is separate and does not call those methods.

The new preparation panel is visible only when Full Edge is selected. Existing host-based setup controls are moved under the application's Advanced Details state and explicitly labeled as legacy and not offline-certified. They remain available for backward compatibility; they are not the normal H5A action.

## UI/backend boundary

`installer.FullEdgePreparationAPI` defines `Status`, `Start`, `Cancel`, `Diagnostic`, and `Recover`. The Wails bridge forwards those calls. The default `UnavailableFullEdgePreparation` returns a blocked status, rejects actions, and explains that no provisioning was attempted. H3/H4/H6 executors are not linked.

The React service is a thin typed bridge. It falls back to the same blocked state when Wails bindings are unavailable (for example, browser preview). No frontend mock can report READY. The local hook prevents duplicate clicks within the view; the eventual backend must serialize across all callers/processes and implement idempotency for request IDs.

## States and expected transitions

| State | Meaning | User action |
| --- | --- | --- |
| `initial` | No pack state has been resolved | None until backend detects a pack |
| `runtime_pack_detected` | Pack is present and eligible for verification | Start preparation |
| `preparation_pending` | Operation accepted | Wait / cancel only if backend permits |
| `verifying_integrity` | Pack manifest and artifact hashes are checked | Wait |
| `installing_dependencies` | Bundled dependencies are installed | Wait / safe cancellation only |
| `preparing_worker` | Bundled worker is prepared | Wait |
| `verifying_media` | Bundled FFmpeg/FFprobe are verified | Wait |
| `validating_model` | Model authorization and integrity are checked | Wait |
| `runtime_ready` | All backend certification gates passed | Read-only final status |
| `recoverable_error` | Retry is explicitly safe | Retry only when backend says `retryable` |
| `blocked_error` | Hard prerequisite or safety gate failed | Diagnostics; no retry unless a new backend status allows it |
| `cancelled` | Operation cancelled; prior runtime must remain intact | Retry only if backend returns safe eligibility |
| `recovery_pending` / `recovering` / `recovered` | Previous installation requires/restores recovery | Recover only when backend says `recovery_available` |

Legal transitions are encoded by `CanTransitionPreparation`. A backend integration must persist operation identity and recovery status so restart/relaunch cannot silently convert partial preparation to READY.

## Readiness gates

The Go contract and UI require `runtime_ready`, 100% progress, and a backend certification with non-empty certificate/runtime/model versions, valid SHA-256 values for both manifests, verified platform/evidence, runtime pack, dependencies, worker, FFmpeg, FFprobe, model authorization/integrity, successful inference smoke test, and certification timestamp. Python/dependency presence alone is insufficient. H5A's unavailable adapter never returns certification.

The final backend implementation must additionally validate each snapshot before publishing it and bind certification to the exact Runtime Pack, worker and model-pack compatibility versions. This UI contract is not a substitute for backend certification or H7 physical/offline certification.

## Errors and diagnostics

The UI maps known codes to localized, operator-safe messages for hash mismatch, missing dependency, insufficient disk, unsupported architecture, pending model license approval, unauthorized model, cancellation, and recovery pending. Unknown errors use a generic message. The diagnostic area is expandable; displayed text is bounded and redacts common host paths and secret-like values, and stack-trace-shaped text is replaced. The backend must still emit structured, sanitized diagnostics and must never send credentials, full environment values, or stack traces.

Retry and cancellation availability come only from backend flags and state. Recovery does not delete backups; a future executor owns verified atomic replacement and rollback. H5A does not implement filesystem mutations.

## Compatibility and dependencies

The new flow is opt-in and does not alter enrollment, current processing mode, daemon operation, or existing legacy APIs. Legacy setup is preserved behind Advanced Details and is explicitly not an offline-certified route.

- H3: Runtime Pack / offline dependency artifacts and integrity contract remain unmerged; no H3 integration is performed here.
- H4: bundled Vision Worker and FFmpeg/FFprobe integration remains pending.
- H6: model authorization/version compatibility and related certification gates remain pending.
- H7: offline/physical certification is separate; H5A cannot certify production readiness.

## Tests and verification scope

Go tests cover state validation, certification gates, allowed transitions, unavailable adapter behavior, and the local single-flight/idempotency helper. Frontend unit tests cover readiness gating, action eligibility, cancellation/recovery state checks, bounded progress/error mapping, and diagnostic redaction. These tests use no cameras or external services and do not execute real provisioning.

Pending H5B: connect a real backend executor only after H3/H4/H6 contracts are integrated; add end-to-end UI/backend tests for all state transitions, persistence, process interruption, rollback, and offline behavior; regenerate Wails bindings using the pinned Wails toolchain; then run the Runtime Pack and H7 certification suite.
