# UX-7 — Cross-Platform Installer GUI Build & Release

`UX7_BUILD_PIPELINE = COMPLETE`
`UX7_WINDOWS_AMD64_BUILD = PASS` (real GitHub Actions run, `windows-latest`)
`UX7_LINUX_AMD64_BUILD = PASS` (real GitHub Actions run, `ubuntu-latest`)
`UX7_LINUX_ARM64_BUILD = PASS` (real GitHub Actions run, `ubuntu-24.04-arm`, native)
`UX7_MACOS_ARM64_BUILD = PASS` (real GitHub Actions run, `macos-14`; also
run for real locally during UX-1 re-verification)
`UX7_SIGNING_IMPLEMENTATION = COMPLETE`
`UX7_WINDOWS_SIGNING_EXECUTION = BLOCKED_EXTERNAL_SECRET`
`UX7_MACOS_SIGNING_EXECUTION = BLOCKED_EXTERNAL_SECRET`
`UX7_PUBLISH_FAIL_CLOSED = YES`
`UX7_CHECKSUM_FINAL_ARTIFACTS = YES` (computed after signing, not before)

## Scope: separate from the daemon/appliance pipeline

`monitoreoedgeis` already has `.github/workflows/release.yml` — a real,
existing, tag-driven pipeline that builds the **Edge daemon/appliance**
(Linux amd64 + arm64 only, static ffmpeg, `package.sh`, SHA256SUMS,
fail-closed Ed25519 signing via `OTA_SIGNING_KEY`). Untouched by this
milestone. What this milestone adds is the equivalent for the **Wails
desktop Installer GUI**, a different toolchain (CGO + native webview per
OS) and a different platform set (adds Windows, has no "runs as a systemd
service" concern).

## Two workflows, two purposes

**`installer-build-validation.yml`** (test builds, never publishes): one
job per platform on a native runner, uploads a GitHub Actions artifact, no
signing secrets needed. Runs on `pull_request` (the permanent design) and
`workflow_dispatch`.

**`installer-release.yml`** (official release, `installer-v*.*.*` tag
only): builds all four platforms, then signs Windows and macOS for real,
then `publish` — which now depends on **every** build job **and both**
signing jobs, with no `if: always()`, and additionally refuses to run if
any `*-unsigned` artifact directory is still present. There is no path
left from "signing didn't happen" to "an unsigned Windows/macOS binary
got published as official." Checksums (`SHA256SUMS-<platform>.txt`) are
computed after signing, on the final signed artifact, not the pre-sign one.

## Real Windows/macOS signing implementation (not a placeholder anymore)

**Windows** (`sign-windows` job, `windows-latest`): requires
`WINDOWS_SIGNING_CERT_BASE64` + `WINDOWS_SIGNING_CERT_PASSWORD`; decodes
the PFX to a `RUNNER_TEMP` file, locates `signtool.exe` on the Windows SDK
path, signs with `/fd SHA256 /tr http://timestamp.digicert.com /td SHA256`
(RFC3161 timestamp), runs `signtool verify /pa /v` on the result, then
deletes the certificate file (`if: always()`).

**macOS** (`sign-macos` job, `macos-14`): requires
`APPLE_SIGNING_CERT_BASE64` + `APPLE_SIGNING_CERT_PASSWORD` +
`APPLE_SIGNING_IDENTITY` + `APPLE_NOTARY_KEY_ID` + `APPLE_NOTARY_ISSUER_ID`
+ `APPLE_NOTARY_PRIVATE_KEY`; creates a temporary keychain, imports the
`.p12`, `codesign --options runtime --timestamp` (hardened runtime,
required for notarization), `codesign --verify --deep --strict`, zips the
`.app` with `ditto`, `xcrun notarytool submit --wait` using an App Store
Connect API key (not just an Apple ID — that alone doesn't satisfy
`notarytool`), `xcrun stapler staple` + `validate`, then destroys the
keychain (`if: always()`).

Neither of these is a placeholder `exit 1` anymore. They are real,
standard commands for this exact purpose. What's genuinely untested is
whether they run flawlessly against a real certificate on the first try —
that can only be confirmed once real secrets exist, which is exactly
`UX7_WINDOWS_SIGNING_EXECUTION`/`UX7_MACOS_SIGNING_EXECUTION =
BLOCKED_EXTERNAL_SECRET`.

## Real build evidence (GitHub Actions, this branch, no tag, no release)

A temporary `push` trigger on `feature/ux-final-closure` was used once to
get real runner evidence for all four platforms without opening a PR (per
this milestone's own instruction), then removed once the evidence was
collected — the permanent trigger stays `pull_request` +
`workflow_dispatch`.

First attempt (run `36421923965`) found a real, structural gap: Linux
`amd64`/`arm64` both failed with
`Package webkit2gtk-4.0 was not found in the pkg-config search path` —
GitHub's `ubuntu-latest` (24.04) and `ubuntu-24.04-arm` runner images ship
only `libwebkit2gtk-4.1-dev`, not the `4.0` package Wails' webview binding
looks for by default. Windows and macOS already passed on that same run.
Fix: added `-tags webkit2_41` to both Linux build commands (Wails' own
supported way to target WebKitGTK 4.1), in both workflow files.

Second attempt (run `36439281617`, after the fix, same branch): all four
platforms passed for real.

| Job | Runner | Result |
|---|---|---|
| build-windows-amd64 | `windows-latest` | **success** |
| build-linux-amd64 | `ubuntu-latest` | **success** |
| build-linux-arm64 | `ubuntu-24.04-arm` (native) | **success** |
| build-macos-arm64 | `macos-14` | **success** |

This is real GitHub Actions runner output, not a YAML-syntax inference —
the first run's real failure and the second run's real fix-then-pass are
both evidence the pipeline was actually exercised, not just parsed.

## Why signing execution stays blocked

No Windows code-signing certificate or Apple Developer signing/
notarization credentials exist in this session — genuinely external, not
fabricable. The implementation is complete and would run the moment those
six secrets exist; until then `sign-windows`/`sign-macos` fail at their
first "require secrets" step, exactly as designed, and `publish` can never
run for an official release without them.

## Next action requiring Gustavo

1. Provide `WINDOWS_SIGNING_CERT_BASE64`/`WINDOWS_SIGNING_CERT_PASSWORD`
   and/or `APPLE_SIGNING_CERT_BASE64`/`APPLE_SIGNING_CERT_PASSWORD`/
   `APPLE_SIGNING_IDENTITY`/`APPLE_NOTARY_KEY_ID`/`APPLE_NOTARY_ISSUER_ID`/
   `APPLE_NOTARY_PRIVATE_KEY` as repository secrets, when official signed
   releases are actually wanted.
2. Decide whether/when to push a real `installer-v*.*.*` tag once signing
   secrets exist, to validate `installer-release.yml`'s `publish` path for
   real (never triggered in this pass — doing so was out of scope without
   secrets, since the run would necessarily fail at signing by design).
