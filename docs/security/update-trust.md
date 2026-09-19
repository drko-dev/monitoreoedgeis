# GEO CAM Edge — Update Trust / Supply Chain (Hito S, S9)

Scope: what the appliance's release/update pipeline actually authenticates
today, and the minimum requirements a future signed-update mechanism
(Hito T, T4) must meet. This document defines the **security boundary**;
it deliberately does not build the signing infrastructure itself — see
[Out of scope](#out-of-scope-deferred-to-hito-t-t4) below.

## Current state (audited, not assumed)

Audited: `deploy/appliance/scripts/package.sh`, `update.sh`,
`.github/workflows/release.yml`.

- **`package.sh`** produces the release artifact and a `.sha256` file next
  to it (`sha256sum`/`shasum -a 256` over the artifact).
- **`update.sh`** requires that `.sha256` file and refuses to activate an
  artifact whose hash does not match.
- **`.github/workflows/release.yml`** (Hito P) builds `linux/amd64` and
  `linux/arm64`, packages each into a `tar.gz`, and generates a
  `SHA256SUMS` manifest attached to the GitHub Release.
- No cryptographic signature of any kind exists anywhere in this repo
  today — verified: no `cosign`, `gpg`, `minisign`, or any asymmetric
  signing/verification code in Go, shell, or CI workflow files.

**A SHA-256 checksum detects accidental corruption or in-transit
tampering. It does not authenticate who produced the artifact** — anyone
who can substitute the artifact can also recompute and substitute a
matching `.sha256` file. Precisely because of that distinction:

```
ARTIFACT CHECKSUM:            IMPLEMENTED
ARTIFACT AUTHENTICITY SIGNATURE: NOT IMPLEMENTED
SIGNED OTA UPDATE:             RESERVED FOR T4 / FUTURE IMPLEMENTATION
```

This repo does not call today's checksum-only mechanism a "signed
update," and no code in this hito claims otherwise.

## Minimum requirements for T4 (signed OTA updates)

Specified here as the contract a future implementation must satisfy —
not implemented in this hito:

1. **Asymmetric signature**, not a shared secret/HMAC — a compromised
   Edge must never be able to forge a signature valid for other devices.
2. **The private signing key never lives on the appliance, and never
   lives in this repository.** It belongs to a separate, offline or
   tightly-access-controlled signing authority/process (e.g. a release
   signing service, an HSM, or a maintainer-held key with a documented
   custody procedure) — out of scope to design here, but the appliance
   must never be trusted to hold it.
3. **The appliance holds/trusts only the verification public key**,
   provisioned at build/install time, never fetched from the same
   channel as the artifact it verifies (or the channel providing the key
   must itself be authenticated independently).
4. **Signature verification happens before install/activation** — never
   after the new version is already running. `update.sh`'s current
   checksum gate is the right *position* in the flow (pre-activation);
   T4 adds a signature check at that same gate, in addition to (not
   instead of) the checksum.
5. **Checksum does not replace signature.** Both checks apply: checksum
   for transport-integrity, signature for authenticity. Neither is
   sufficient alone.
6. **Fail closed.** Once signed-update mode is mandatory for a given
   appliance/fleet, a missing or invalid signature must block
   installation/activation — never fall back to checksum-only silently.
7. **Rollback must not become a bypass.** A future rollback path
   (Hito P's `rollback.sh` mechanism, extended) must not let an attacker
   "roll back" to an unsigned or signature-stripped artifact as a way
   around requirement 6 — rollback targets are subject to the same
   authenticity requirements as forward updates once signing is
   mandatory.

## What this hito does NOT do

- No signing key was chosen, generated, or committed. **No production or
  fake production signing key exists anywhere in this change.**
- No PKI, certificate authority, or key-distribution mechanism was
  designed. The requirements above are deliberately implementation-
  agnostic (any of GPG detached signatures, minisign, `cosign`/sigstore,
  or a bespoke Ed25519 scheme could satisfy them) — choosing one is T4's
  job, not S9's.
- No change to `update.sh`'s runtime behavior. It still enforces the
  SHA-256 checksum exactly as before; nothing about the checksum gate was
  altered in this hito.

## Out of scope (deferred to Hito T, T4)

Hito T covers the full OTA lifecycle — download, checksum, **signature**,
upgrade, rollback — as one integrated unit. Building only the signature
piece here, disconnected from the rest of that lifecycle, would risk a
half-implemented, unreviewed trust boundary. S9's job is to make sure
that when T4 is built, it is built against a clear, written contract
(the requirements above) rather than an implicit assumption.
