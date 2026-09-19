# GEO CAM Edge — OTA Foundation (Hito T, T1-T4)

Scope: what T1-T4 actually implement in this repo — version comparison,
update discovery, verified download and atomic staging. This is the
foundation layer only; T5-T10 (privileged apply/activation, rollback,
fleet rollout policy) are explicitly out of scope and untouched here. See
[update-trust.md](update-trust.md) for the T4 signing contract this
implementation satisfies.

## Architecture

The heartbeat cadence (Hito D/`internal/heartbeat`) is reused as the OTA
discovery clock — there is no second poll loop. After each successful
heartbeat, `internal/agent/heartbeat_module.go` wires
`heartbeat.Options.OnSuccess` to run `ota.Module.CheckOnce` in its own
goroutine (never blocking the next scheduled heartbeat, and never turning
an OTA failure into a heartbeat failure).

```
heartbeat success -> ota.Module.CheckOnce
                        -> transport.Client.GetNextOTARelease   (Edge-authenticated)
                        -> ota.IsUpdateEligible                 (T1)
                        -> ota.FileDownloader.FetchAndStage      (T3, no Edge creds)
                             -> VerifyManifestSignature           (T4, Ed25519)
                             -> VerifyArtifactChecksum             (T4, SHA-256)
                             -> VerifyEligible                     (T1, re-checked)
                             -> VerifyArchiveLayout                (full appliance layout)
                             -> atomic rename into ota/pending/<release-id>/
                             -> atomic write of ota/apply.request  (LAST step)
```

`ota/apply.request`'s presence is the entire contract handed to the
privileged updater (IA2/T5+): a fully downloaded and verified artifact is
ready to be **revalidated** and applied. This package never runs
`install.sh`/`update.sh`/`systemctl`, and never touches anything under
`/opt`.

## T1 — Version comparison (`internal/ota/version.go`)

Reuses `internal/agent.Version` as the sole source of truth — no second
version representation. `ParseVersion` accepts the canonical `vX.Y.Z` form
(and a bare `X.Y.Z`, since the unbuilt dev default `"0.1.0"` lacks the `v`
prefix); anything else is rejected outright, never compared
lexicographically. `IsUpdateEligible` requires a strictly newer candidate —
equal or older is not eligible. Downgrade/rollback is a separate,
operator-driven mechanism (T7), not this check.

## T2 — Update discovery (`internal/ota/module.go`)

`Module.CheckOnce` is a small, externally-driven sync cycle mirroring
`internal/remoteconfig.Module.SyncOnce`: fetch → compare → hand off to the
downloader. An OTA transport error never fails the heartbeat that
triggered it — see the `OnSuccess` wiring above. Overlapping calls (a slow
download still running when the next heartbeat succeeds) are skipped, not
queued.

## T3 — Download (`internal/ota/download.go`)

`FileDownloader` uses a completely separate `http.Client` from the
Edge↔SaaS `transport.Client` — it never sets `Authorization` or
`X-Device-Id`. HTTPS is required on every request; the initial (SaaS-
issued) URL is trusted for scheme only, while `CheckRedirect` applies the
full guard (HTTPS + reject private/loopback/link-local resolved
addresses) to every redirect target, since a redirect is attacker/CDN-
controlled in a way the initial URL is not. This is not a general
arbitrary-URL downloader: it fetches exactly the three URLs a release
descriptor names, from GitHub Releases.

Staging is atomic: everything is downloaded and verified into a scratch
temp dir first; only on full success is that dir renamed into
`ota/pending/<release-id>/` and `ota/apply.request` written (temp file +
rename), in that order. A crash at any point before both of those succeed
leaves no `apply.request` and no misleading partial pending dir.

## T4 — Ed25519 signature + checksum (`internal/ota/verify.go`)

Satisfies every requirement in
[update-trust.md](update-trust.md#minimum-requirements-for-t4-signed-ota-updates):

- **Asymmetric (Ed25519)**, Go stdlib, no HMAC/shared secret.
- **Private key never on the appliance, never in this repo.** Only
  `geocam-edge ota sign` (release-pipeline-only CLI, driven from the
  `OTA_SIGNING_KEY` GitHub Actions secret) touches it.
- **Appliance holds only the public key**, provisioned out-of-band via
  `GEOCAM_OTA_PUBLIC_KEY_FILE` (root-managed file), never fetched from the
  artifact channel itself.
- **Verification order, fail closed, no checksum-only fallback**:
  1. Ed25519 signature of `SHA256SUMS` under the configured public key.
  2. Artifact's real SHA-256 against that now-trusted `SHA256SUMS`.
  3. Forward-update eligibility (T1), re-checked at verification time.
  4. Full appliance archive layout (`geocam-edge`, `VERSION`, `ARCH`) and
     architecture match.
  A missing/unreadable public key or missing/empty signature is rejected
  outright — never silently treated as "signature not required."

`geocam-edge ota verify` exposes this exact logic as a CLI subcommand so it
can be re-run independently by an operator or reused unmodified by IA2's
privileged updater before it ever activates a staged release.

## Release pipeline

`.github/workflows/release.yml` now runs
`deploy/appliance/scripts/package.sh` (the same script `update.sh` already
expects) instead of a second, reduced tarball format — one OTA artifact
shape, not two. Signing is fail closed: the job errors out before
packaging anything if `OTA_SIGNING_KEY` is not configured, so an unsigned
release can never be published.

## Configuration

| Variable                     | Purpose                                                    |
| ----------------------------- | ----------------------------------------------------------- |
| `GEOCAM_OTA_PUBLIC_KEY_FILE`  | Path to the Ed25519 public key used to verify releases.     |

## Out of scope (T5-T10, not implemented here)

Applying a staged release (privileged updater, systemd coordination),
rollback, staged/canary fleet rollout, and any UI/reporting around update
status are IA2's and later hitos' responsibility. This package's authority
ends at writing `ota/apply.request`.
