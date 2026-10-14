# H6D — Release input gate (preparatory, not approval)

Tracking: #160. Status: PARTIAL / BLOCKED FOR REAL RELEASE.

The read-only script `deploy/installer/check-runtime-pack-release.py <pack> --trusted-manifest-sha256 <sha256>` rejects a template manifest, absent external digest, manifest mismatch, missing artifacts, invalid file sizes/hashes, paths outside the pack and symlinked artifacts. Synthetic-only tests: `python3 deploy/installer/test-runtime-pack-release-preflight.py`.

**This is an integrity precheck, not an authenticated signature, full H4A schema validator, licensing auditor, secure pack assembler, permission/immutability verifier, architectural compatibility validator or functional offline certification.** A malicious pack author can create a self-consistent manifest and digest. The release publisher must independently authenticate the pin and provenance. Until approved, `MODEL_REDISTRIBUTION_APPROVED=false`; no models may be redistributed and no readiness flag is granted.

Observed on 2026-10-14: current checked-in manifest is `template`, with placeholder hashes for Worker/FFmpeg, and the local workspace has no corresponding release artifacts. The precheck correctly produces NO-GO. Disk free approximately 4.5 GiB. No downloads, file installation, release/tag or runtime changes were performed.

Next gate: authorized provenance/licensing inventory, real sealed artifact inputs, trusted external pin publisher, immutable activated pack and integration tests on supported platform. Issue #160 requires owner approval for publishing code changes.
