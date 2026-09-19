# Releasing (Hito P)

## Versioning

Git tags are the source of truth: `vX.Y.Z` (semver, `v` prefix required).
No separate `VERSION` file -- the tag itself is passed straight through as
`internal/agent.Version` (see `Makefile`'s `VERSION` variable and
`internal/agent/version.go`).

## Cutting a release

```
git tag v0.2.0
git push origin v0.2.0
```

Pushing a tag matching `v*.*.*` triggers `.github/workflows/release.yml`,
which:

1. Builds linux/amd64 and linux/arm64 via the existing `make build-linux`
   recipe -- same reproducibility guarantees as any local build: `-trimpath`,
   `-ldflags` embedding version/commit/build date, identical flags for both
   architectures (see `Makefile`).
2. Packages each binary into `geocam-edge-vX.Y.Z-linux-<arch>.tar.gz`.
3. Generates `SHA256SUMS` over the tarballs.
4. Publishes a GitHub Release with the tarballs and `SHA256SUMS` attached.

## Out of scope (later hitos)

- Signing / provenance attestation -- Hito S/T.
- SBOM generation.
- Pushing the OCI image to a registry (P3's multi-arch `Dockerfile` already
  builds locally for K3s via `make image`; a registry push is a separate,
  not-yet-scoped concern).
