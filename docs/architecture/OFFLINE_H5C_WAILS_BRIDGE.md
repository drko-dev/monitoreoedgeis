# H5C — Explicit Wails to H6B transactional binding

Status: PARTIAL, opt-in construction boundary only. Tracks #155.

The Wails-generated bindings already provide Status/Start/Cancel/Diagnostic/Recover. NewApp() remains backed by UnavailableFullEdgePreparation (fail-closed).

NewAppWithPinnedRuntimePack(options) constructs the Wails App with RuntimePackPreparationAdapter and TransactionalRuntimePackProvisioner **only** after a release-owned caller explicitly enables it and supplies a trusted, externally sourced SHA-256 manifest pin and pack/data directories. No web UI values or environment variables are accepted as trust roots. H6B re-verifies the manifest and filesystem during Prepare.

This new constructor is not called by the shipping entrypoint and does not activate anything by itself. It is intended for a future reviewed release assembly with a bundled sealed runtime. Default production behavior is unchanged.

Remaining work: authenticated release pin provider and sealed pack, restart-persistent status/recovery reconciliation, safe cancellation semantics, complete H4 provenance/integrity/TOCTOU review, authorized models, Wails application bootstrap selection and actual offline certification. H6B transaction integrity does not imply RUNTIME_READY or INFERENCE_READY.
