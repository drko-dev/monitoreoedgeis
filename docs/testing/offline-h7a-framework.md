# H7A offline certification framework

**Scope:** reusable, local/CI-safe test infrastructure only. This is not H7
completion and does not certify a Full Edge installation. The base is
`6f381a4d337ce2271823b92884ea4c5b6a1f03d1`.

## Safety boundary

- Synthetic packs contain only tiny text fixtures; they are not executable,
  installable wheels, Python runtimes, models, or camera configurations.
- `offline-cert` only parses `RuntimeManifest`, checks target OS/architecture,
  and hashes declared files. It never starts provisioning, Python, pip, Homebrew,
  the Vision Worker, or camera/network clients.
- The subprocess sandbox uses its own temporary home/tmp/PATH and a deterministic
  allowlisted environment. A fake `pip` executable records and rejects attempts.
- The CI test process runs with `GOPROXY=off` after Go modules are prepared.
  GitHub Actions itself and the runner still have network access. A private PATH
  and a proxy/PyPI trap do **not** block direct sockets; this workflow is not a
  network namespace/firewall proof. Full no-egress evidence remains pending an
  isolated CI runner or equivalent OS-level sandbox.
- No test contacts cameras, modifies host networking, installs Torch/wheels, or
  requires production credentials. Physical checks are deferred and separately
  authorized.

## Commands

```sh
go test ./internal/offlinecert ./cmd/offline-cert
go test ./internal/installer -run '^TestRuntimeManifest_'
go test -race ./internal/offlinecert ./cmd/offline-cert
go run ./cmd/offline-cert -self-test \
  -target-os linux -target-arch amd64 -base-sha "$(git rev-parse HEAD)"
```

To inspect an actual assembled pack without provisioning:

```sh
go run ./cmd/offline-cert \
  -pack-dir /path/to/runtime-pack \
  -target-os linux -target-arch arm64 \
  -base-sha "$(git rev-parse HEAD)"
```

Output is deterministic JSON with a deliberately narrow scope. `passed` means
only manifest/schema, target-tuple, or declared-file integrity checks passed.
Offline provisioning, interrupted activation/rollback, and physical runtime
certification are emitted as `pending`, never inferred from artifact hashes.

## Scenario matrix

| Scenario | H7A evidence | Status / guarantee |
|---|---|---|
| Corrupt manifest | Existing RuntimeManifest parser tests; synthetic harness checks malformed input | PASS, schema-level |
| Altered SHA-256 | Existing installer tests and synthetic pack hash mismatch | PASS, file integrity only |
| Missing / truncated artifact | Existing tests plus synthetic fixture | PASS, integrity validator only |
| External / broken symlink | Existing installer adversarial tests plus synthetic fixture | PASS on Unix; Windows symlink test may skip due privileges |
| Path traversal | Existing schema/artifact tests and synthetic manifest | PASS, manifest-path validation |
| Non-executable runtime binary | Current manifest integrity API does not assert executable mode | PENDING H3/H6 activation contract |
| Incompatible architecture | Existing `ValidateCompatibility` and synthetic fixture | PASS, target metadata only (not binary execution) |
| Host Python absent / Python env contamination | Clean child environment and private PATH tests | PASS, child process only |
| PyPI attempt | Fake `pip` trap proves attempted command is observable and fails | PASS, instrumentation only; not a complete network-egress proof |
| Filesystem failure injection during preparation | Deterministic `FaultPlan` hook is available, but the H6 preparer has no injectable adapter on this base | PENDING H6 integration seam |
| Preparation interruption / rollback failure / backup preservation | No integrated H6 activation entry point on this base | PENDING H6 |
| Repeated execution | Repeated artifact validation is stable | PASS for validator; provisioning idempotency remains pending |
| Partial pack | Missing declared artifact is rejected | PASS, artifact integrity only |
| Runtime/worker/model compatibility | Runtime manifest checks target tuple; integrated compatibility handshake is not available here | PENDING H3-H6 |
| Python import failure | Synthetic runtime deliberately is not executable | PENDING integrated runtime |
| Internet available but provisioning forced offline | Existing provisioning cannot be driven safely through a no-egress seam here | PENDING H3/H6 plus isolated runner |
| Physical CPU/MPS/worker/camera certification | Explicitly outside CI | PENDING integrated build and authorization |

The existing `internal/installer/runtime_manifest_test.go` already covers a
larger set of schema, hash, symlink, traversal, and compatibility cases. H7A
reuses that suite without editing it; these harness tests add synthetic-pack,
subprocess-environment, timeout, trap, and deterministic-report primitives.

## Certification classes

- **Unit:** `internal/offlinecert` and CLI report behavior, small fixtures only.
- **Contract:** existing `RuntimeManifest` parser/schema/hash/path tests.
- **Offline process-boundary:** sandboxed environment and denied-tool trap; no
  assertion that kernel networking is disabled.
- **Physical certification:** not implemented/enabled by this workflow; later
  work must require an integrated H3-H6 artifact and authorized test rig.

## Remaining H7 dependencies

H3 wheelhouse/runtime provisioning and H6 atomic activation must provide a
testable entry point before package-download absence, interruption recovery,
rollback, executable permissions, or runtime imports can become PASS. H4/H5
integration is also needed before declaring a complete Full Edge combination.
Do not interpret this harness PR as H7 completion.
