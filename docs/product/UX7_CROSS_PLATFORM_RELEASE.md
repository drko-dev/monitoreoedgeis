# UX-7 — Cross-Platform Installer GUI Build & Release

`UX7_BUILD_PIPELINE_WRITTEN = YES`
`UX7_BUILD_PIPELINE_EXECUTED = NOT_VALIDATED` (never triggered — would need
an `installer-v*.*.*` tag push, not authorized in this pass)
`UX7_WINDOWS_AMD64 = NOT_VALIDATED` (pipeline written, never run)
`UX7_LINUX_AMD64 = NOT_VALIDATED` (pipeline written, never run)
`UX7_LINUX_ARM64 = NOT_VALIDATED` (pipeline written, never run)
`UX7_MACOS_ARM64 = COMPLETE (build), NOT_VALIDATED (via this pipeline)` —
the actual darwin/arm64 Wails build was run for real, locally, during
UX-1 re-verification (`go run .../wails@v2.16.0 build`, succeeded); the
GitHub Actions job that would reproduce this on a `macos-14` runner has
never executed.
`UX7_OFFICIAL_SIGNING = BLOCKED_EXTERNAL_SECRET`
`UX7_RELEASE_PIPELINE = WRITTEN_UNTESTED`

## Scope: separate from the daemon/appliance pipeline

`monitoreoedgeis` already has `.github/workflows/release.yml` — a real,
existing, tag-driven pipeline that builds the **Edge daemon/appliance**
(Linux amd64 + arm64 only, static ffmpeg, `package.sh`, SHA256SUMS,
fail-closed Ed25519 signing via `OTA_SIGNING_KEY`). That pipeline is
untouched by this milestone and needs no changes — it already does
everything Section 10 of this audit asks for, for the artifact it covers.

What did not exist, and what this milestone adds, is the equivalent for
the **Wails desktop Installer GUI**, which needs a different toolchain
(CGO + native webview per OS) and targets different platforms (adds
Windows, drops the "runs as a systemd service" concern entirely).

## What was added

`.github/workflows/installer-release.yml` — tag-driven
(`installer-v*.*.*`), one job per platform on a native runner:

| Platform | Runner | Why native |
|---|---|---|
| Windows amd64 | `windows-latest` | Wails/CGO + WebView2 |
| Linux amd64 | `ubuntu-latest` | Wails/CGO + GTK3/WebKit2GTK |
| Linux arm64 | `ubuntu-24.04-arm` (native ARM64 runner) | same, cross-compiling Wails/CGO from amd64 is not reliable enough to trust as a validated GUI artifact |
| macOS arm64 | `macos-14` | Wails/CGO + WebKit |

Each build job produces a real GUI application bundle per platform (not a
bare cross-compiled Go binary) plus a `SHA256SUMS-<platform>.txt`. A
`publish` job (gated on all four builds, `if: always()`) attaches every
built artifact to the GitHub Release as `prerelease: true`, so unsigned
test artifacts are always producible even with zero signing secrets
configured — satisfying `UX7_UNSIGNED_TEST_ARTIFACTS`.

Signing is two separate fail-closed jobs, `sign-windows` and `sign-macos`
(Linux artifacts ship with a checksum but no code-signing convention to
satisfy here): each explicitly fails if its platform's secret
(`WINDOWS_SIGNING_CERT`/`WINDOWS_SIGNING_CERT_PASSWORD`,
`APPLE_SIGNING_CERT`/`APPLE_NOTARIZATION_APPLE_ID`) is absent, rather than
silently skipping and letting the unsigned artifact be mistaken for an
official signed release. **Neither signing job has a real `signtool`/
`codesign`+`notarytool` invocation yet** — they are deliberately left as
an explicit, loud placeholder (`exit 1` with a message) rather than a
fabricated signing step, because no certificate exists to test one
against. This is `UX7_OFFICIAL_SIGNING = BLOCKED_EXTERNAL_SECRET`, exactly
as this milestone's instructions anticipated.

## What was verified, and how

- **YAML syntax**: parsed successfully with `PyYAML` locally (the
  `'on'` key reads back as the boolean `True` — a known YAML 1.1 quirk
  with `PyYAML` specifically; GitHub Actions itself parses `on:` as a
  literal key and is unaffected).
- **Job graph**: 7 jobs present with the expected `needs:` edges
  (4 builds → 2 signs → 1 publish).
- **Local build parity**: the darwin/arm64 job's exact command
  (`wails@v2.16.0 build -platform darwin/arm64`, minus the `-platform`
  flag since the local run auto-detected the host) was run for real on
  this machine and succeeded (see `UX1_WAILS_SHELL.md`'s closure note).

**What was not verified**: an actual GitHub Actions run. Triggering one
requires pushing an `installer-v*.*.*` tag, which this milestone's own
rules forbid ("no crear tags/releases oficiales" / "PR: NO automático").
Windows and Linux-arm64 native-runner behavior for this specific Wails
app is therefore unverified beyond YAML correctness and the parity
argument above.

## Why this stays partially unvalidated

- No code-signing certificate (Windows) or Apple Developer signing/
  notarization credentials exist in this session — genuinely external,
  not something to fabricate.
- No authorization exists in this session to push a release tag and
  consume real GitHub Actions runner-minutes / publish a real
  (even prerelease) GitHub Release.

## Next action requiring Gustavo

1. Decide whether to push an `installer-v0.0.0-test` tag (or similar) to
   let `installer-release.yml` actually run once, non-officially, to
   validate the Windows/Linux-arm64 jobs for real.
2. Provide `WINDOWS_SIGNING_CERT`/`WINDOWS_SIGNING_CERT_PASSWORD` and/or
   `APPLE_SIGNING_CERT`/`APPLE_NOTARIZATION_APPLE_ID` as repo secrets, and
   implement the real `signtool`/`codesign`+`notarytool` commands in the
   two placeholder jobs, when official signing is actually wanted.
