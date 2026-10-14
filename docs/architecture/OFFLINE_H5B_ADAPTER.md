# H5B — Transactional Runtime Pack preparation adapter (preparatory)

Status: PARTIAL / NOT WIRED TO DESKTOP. Tracking issue #155.

## Scope
- Implements an **opt-in**, injected H5A `FullEdgePreparationAPI` adapter for H6B transactional `Prepare` / `Recover`.
- Does not modify the product default, which remains `UnavailableFullEdgePreparation`.
- A transactionally committed and integrity-verified pack maps to non-ready `FAILED / RUNTIME_UNCERTIFIED`, never to `READY`; runtime integrity is not functional certification.
- Recovery errors map to `RECOVERY_REQUIRED` only when the H6B result indicates recovery ambiguity / an uncertified rollback.
- Cancellation remains unsupported rather than falsely acknowledging an in-flight transaction.
- Duplicate request IDs return the prior snapshot without re-running preparation while the adapter instance lives.

## Limits
- No H5A desktop/Wails wiring, no release pin/provider, no running Worker, no Python/FFmpeg/models or real inference.
- No cross-process request-ID idempotence; H6A lock and reconcile remain responsible for persistent transaction integrity.
- Progress is conservative; no 100% until a separate certified runtime backend exists.
- Model redistribution stays disabled, and no production services or cameras are touched.

## Tests
- Disabled or missing-backend fail closed.
- Transaction success cannot certify readiness.
- Duplicate request idempotence.
- Rollback-uncertified recovery mapping and diagnostics.
- Unsupported cancellation does not claim success.

Next: integrate with an authenticated release/pin provider and functional readiness evaluator in a subsequent reviewed slice, then pass H7B and H8.
