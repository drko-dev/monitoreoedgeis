# H6D-R11 — Signed Runtime Pack → transactional provisioner, explicit opt-in only

**Status: SYNTHETIC INTEGRATION IMPLEMENTED / RELEASE STILL BLOCKED.** Issue #160, approved for non-production preparatory work. This does not create a sealed pack, publisher release key, or production installer.

## Why this boundary is necessary

The H6B transactional provisioner historically accepted an independently supplied `ExpectedManifestSHA256`; the #166 `signed_manifest.go` module implemented Ed25519/P-256 verification separately. A digest **authenticates only relative to a trusted independent source**, while a signature proves publication under a trusted publisher key. Merely having those modules compiled into the same application did not enforce either on the other's input.

### New opt-in APIs

- `installer.NewSignedRuntimePackPreparationBackend(options, signed, trust)` accepts an explicitly enabled H6B options structure, a publisher **signed manifest supplied from outside the pack**, and a publisher `TrustStore` supplied from outside the pack.
- Requires a correctly formatted 64-hex **independent manifest pin** as well as a signed envelope, trusted key, and valid manifest. A key embedded in pack bytes is not a publisher trust anchor.
- Clones the signed envelope at construction; consults the live trust store again at every Prepare/Recover, including key revocation and expiry. **Signatures do not automatically override permissions.**
- Before the H6B transaction lock, ensures canonical/disjoint pack and data paths, reads bounded signed manifest bytes through a non-symlink path, verifies SHA-256 against the externally provided pin and **compares the parsed on-disk `runtime-manifest.json` with the signed manifest payload**. No signature of a different pack is accepted.
- Calls `ValidateSignedManifest` with `RequireRelease=true`, target architecture/OS, artifact digest validation and revocation/expiry checks. H6B then independently rechecks the pinned SHA-256/allowlist while staging/committing. Failed authentication is `SIGNED_MANIFEST_REJECTED`, with no persistent transaction mutation.
- Revalidates during Recover. A revoked signer **cannot be treated as READY during startup**, even when a prior on-disk transaction was committed. The backend continues to return `COMMITTED_UNCERTIFIED` on valid synthetic commits; `RuntimeReady=false`, `InferenceReady=false` in all cases.

- `cmd/geocam-edge-ui.NewAppWithSignedRuntimePack(options, signed, trust)` wires the protected backend through the existing Wails preparation adapter behind **explicit release-owner opt-in**, retaining the existing bounded startup reconciliation. The default shipping `NewApp()` is **unchanged** and continues to use `UnavailableFullEdgePreparation{}`.
- The older, independently pinned but unsigned `NewAppWithPinnedRuntimePack` still exists for its pre-existing opt-in cases; it has **not** silently acquired a trusted publisher key. A future authorized signed release must explicitly select the new signature-gated constructor.

## Test matrix (synthetic only)

- Signed Ed25519 release + externally supplied matching pin/TrustStore and complete manifest: transactional Prepare and Recover succeed **uncertified**; no READY.
- Disabled or missing trusted digest, signature or trust anchor: reject without creating data directories.
- Corrupted signature, unknown or revoked key, correctly signed different manifest with its own correct digest, tampered Worker artifact, independently pinned digest mismatch: reject before transaction.
- Revocation **after** prior committed synthetic pack blocks a subsequent Recover.
- Caller mutation of `SignedRuntimeManifest` after construction cannot change the bound, cloned signature.
- Wails default app remains fail closed. New explicit constructor rejects missing signature, key or pin, and malformed envelope.
- Existing H5/H6 transactional recovery/rollback tests and security suites should pass unchanged.

## Remains blocked

1. Publisher-owned independently authenticated trust anchors (including secure update, rotation and key lifecycle) and a legally cleared **real** signed manifest are **NOT CONFIGURED**. The test keys are ephemeral synthetic fixtures and are never exported as product keys.
2. This is not a solution for path-based execution TOCTOU across all components. Final activated pack immutability/file-descriptor based exec and true release trust chain remain outstanding.
3. Physical H8 macOS14 clean-host test, H7 installed-product isolation, installer rollback under real package activation, notarization/Developer ID, licenses (Ultralytics/FFmpeg/36 wheels) and model redistribution rights all remain **NO-GO**.
4. `MODEL_REDISTRIBUTION_APPROVED=false`, `READY_FOR_INSTALLER=false`, `READY_FOR_RELEASE=false`. No default Wails runtime activation, model weights, binary packaging, deploy, merge, release or production actions.

**This PR implements the signed-to-transaction security integration boundary, not commercial certification.**
