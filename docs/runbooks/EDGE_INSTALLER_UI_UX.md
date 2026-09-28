# Edge Installer UI/UX — Product Runbook

> Status: DRAFT / implementation roadmap
>
> Scope: installer/operator experience for GEO CAM Edge. This document does not replace the existing technical runbooks for Edge installation, camera onboarding, Full Edge, or release/rollback. It defines the product-facing installation flow and the work required to turn the current Edge software into a guided installer.

## 1. Goal

A technician should be able to install and commission GEO CAM Edge on a supported machine without needing to understand the internal Go architecture, ONVIF internals, RTSP details, worker processes, environment-variable names, or SaaS implementation details.

The installer should guide the technician from a fresh machine to a verified working site through a small number of explicit choices and automated checks.

## 2. Supported delivery targets

The product installer must produce native release artifacts for:

| Target | Architecture | Expected artifact |
|---|---|---|
| Windows | amd64 / 64-bit | installer + executable |
| Linux | amd64 / 64-bit | package/archive + executable |
| Linux | arm64 | package/archive + executable |
| macOS | arm64 / Apple Silicon | signed/notarized app or installer image |

The CLI/headless Edge should remain available for automation and server-style deployments. The GUI installer is an additional product surface, not a replacement for the CLI.

## 3. Proposed UI technology

Use:

- Wails v2 as the desktop shell and Go/GUI bridge.
- React for the interface.
- TypeScript for frontend code.
- Vite for frontend build tooling.
- Existing Go packages/services as the only source of business logic.

The React layer must not reimplement discovery, enrollment, configuration, health, RTSP, ONVIF, credential handling, inference, OTA, or SaaS logic. It should call a narrow local application/service layer backed by the existing Go code.

Target architecture:

```text
React + TypeScript UI
        |
        | Wails bindings / local installer service
        v
Existing Go Edge application services
        |
        +-- enrollment / device identity
        +-- discovery / ONVIF
        +-- camera credentials
        +-- RTSP / stream validation
        +-- configuration / profile selection
        +-- health / status / diagnostics
        +-- local inference / worker control
        +-- SaaS connectivity / sync
        +-- OTA / version reporting
```

## 4. Installer principles

1. Ask the technician only for information the software cannot safely discover itself.
2. Never require the technician to edit configuration files during the normal flow.
3. Never store the SaaS administrator password on the Edge machine.
4. Never print camera/DVR passwords in logs, diagnostics, status, or error messages.
5. Keep Cloud, Hybrid, and Full Edge visibly distinct in the UI while mapping them to the existing Edge configuration model internally.
6. Every step must have a machine-verifiable success/failure state.
7. A failed step must explain what failed and what action the technician can take next.
8. The installer must be resumable where practical; a failure near the end must not force the user to repeat successful discovery/enrollment unnecessarily.

## 5. SaaS enrollment

### Desired UX

The technician should not type a long-lived SaaS admin password into the Edge application.

Preferred product flow:

1. Technician starts the installer.
2. Installer asks for a short-lived enrollment code/token, or performs a temporary authenticated login flow.
3. SaaS validates the installer/user authorization.
4. SaaS creates or authorizes the Edge device.
5. Edge receives its own device identity and rotatable device credential.
6. The temporary enrollment authorization is discarded.
7. Edge uses only its device credential for subsequent SaaS communication.

### Security requirement

The GUI must never persist the SaaS administrator password as an Edge runtime secret.

If the current SaaS APIs do not yet expose a suitable short-lived enrollment mechanism, that becomes a backend requirement for this runbook rather than a reason to store administrator credentials locally.

## 6. Processing mode selection

The installer presents three product choices:

### Cloud

- Local camera connectivity and media pipeline.
- Frames/media required for Cloud inference are sent to SaaS.
- YOLO inference runs in SaaS/Cloud.

### Hybrid

- Local decode and local candidate/motion gating.
- Only selected candidate frames are sent to SaaS.
- YOLO inference remains in SaaS/Cloud.

### Full Edge

- Local media pipeline.
- Local YOLO inference through the Edge vision worker.
- Events/evidence are generated locally and synchronized to SaaS.
- Cloud frame inference is not the primary inference path.

The UI should describe the operational consequence of each mode in simple terms: local compute requirement, network dependency, and where inference happens. It must not expose implementation knobs unless the technician opens an advanced section.

## 7. Source selection

The installer starts camera onboarding with:

```text
What do you want to connect?

[ Camera IP ]
[ DVR / NVR ]
```

### Camera IP

This is the first production target for the UI/UX installer and should use the existing ONVIF/RTSP camera path.

### DVR / NVR

DVR/NVR is a separate technical milestone.

Current product status: `NOT_VALIDATED` for multi-source/multi-channel devices.

The current camera-target identity model is device-oriented and the existing technical design intentionally avoids collapsing multiple DVR/NVR channels into one camera target. Until per-channel identity, discovery, credentials, RTSP selection, status, and processing are implemented and physically validated, the UI must not claim DVR/NVR support.

During early UI development, the DVR/NVR option may be visible but disabled with an explicit status such as:

`DVR/NVR multichannel support: not available in this version.`

DVR/NVR implementation belongs to its own technical milestone/runbook and must not block completion of the first camera-IP installer.

## 8. Camera discovery flow

### Screen: Discover devices

Primary action:

`Search devices on local network`

The UI should show, when available:

- device/camera name
- IP/address
- manufacturer/model
- ONVIF discovery state
- whether credentials are required
- current onboarding state

The normal user flow should not require the technician to know an RTSP path or ONVIF service URL if the device advertises sufficient information.

### Discovery limitations

The UI must represent actual product limitations instead of hiding them. For example, if discovery is limited to the local network segment, the error/help text must say that the device was not discovered and provide a network diagnostic path rather than implying that the camera does not exist.

## 9. Camera credentials

For selected cameras, the installer asks only for the credentials needed by the camera protocol path:

- camera username
- camera password

The UI action should be:

`Test credentials`

The test should validate the actual supported path, not only that a TCP port is open.

Expected result presentation:

```text
ONVIF authentication   OK
RTSP stream            OK
Codec                   H.264
Resolution              1920x1080
FPS                     15
```

On failure, use safe product messages such as:

- Credentials rejected
- Camera unreachable
- ONVIF unavailable or disabled
- RTSP unavailable
- Stream returned but could not be decoded
- Unsupported stream/profile

Do not expose passwords, raw credential payloads, or credential-bearing RTSP URLs.

## 10. Configuration application

After enrollment, mode selection, discovery, and credential validation, the installer applies configuration through Go-owned services.

The GUI must not directly edit arbitrary environment files as its source of truth. If the current product only supports configuration through files/env today, introduce a controlled Go configuration service that validates and writes the supported settings atomically.

Required operations include:

- selected processing mode/profile
- local video-pipeline enablement required by the selected profile
- camera assignments
- camera credential assignment/sync
- SaaS endpoint/device identity
- local worker configuration for Full Edge
- persistent paths required by backlog/evidence where applicable

## 11. Automated validation before completion

The installer cannot declare success only because files were written.

It must verify the running product.

Common checks:

- Edge process/service running
- device enrolled
- SaaS authenticated
- local health endpoint healthy
- selected cameras present
- selected cameras online
- RTSP receiving media
- no unresolved credential error
- correct effective processing profile

Mode-specific checks:

### Cloud

- media pipeline active
- frame/media delivery to SaaS succeeds
- Cloud inference path reports operational

### Hybrid

- local decode active
- local candidate/gating stage active
- candidate upload succeeds
- Cloud inference path reports operational

### Full Edge

- local vision worker healthy
- local inference succeeds
- local event/evidence path operational
- backlog/sync path operational
- event/evidence reaches SaaS

## 12. Final installation screen

A successful installation should end with one simple summary:

```text
GEO CAM Edge is operational

Device               Connected
SaaS                 Connected
Mode                 Full Edge
Cameras online       4 / 4
Inference            Operational
Version              vX.Y.Z

[ Open status ]   [ Finish ]
```

If some non-critical warning remains, the screen must distinguish `Operational with warnings` from full success.

## 13. Local status and diagnostics UI

The installed application should expose a simple status view after commissioning, not only during first-run setup.

Minimum operator information:

- Edge version
- device identity/name
- effective profile
- SaaS connectivity
- uptime
- camera count
- per-camera state
- last safe camera error
- inference state
- backlog/sync state where applicable
- update availability/state

Operator actions should be intentionally limited. Advanced destructive actions should require an explicit confirmation and should not be mixed with the normal status screen.

## 14. Error UX and recovery

Every wizard step needs three states:

`PASS`, `ACTION_REQUIRED`, or `BLOCKED`.

Examples:

| Problem | UI message | Recovery action |
|---|---|---|
| Wrong camera password | Credentials rejected | Re-enter credentials and retry |
| Camera not discovered | No compatible ONVIF device found | Check LAN/ONVIF/network diagnostics |
| RTSP fails | Stream unavailable | Show safe stream diagnostic and retry |
| SaaS unavailable | SaaS cannot be reached | Preserve local configuration and retry connectivity |
| Full Edge worker unavailable | Local inference unavailable | Show worker diagnostic; do not claim Full Edge operational |
| Restart required | Configuration applied; service restart required | Restart automatically when safe and re-run validation |

The user should never need to infer success from raw logs.

## 15. Build, signing and distribution

The Edge repository already has a GitHub release path for appliance artifacts. The GUI product extends that release model rather than creating an unrelated manual process.

Desired release trigger:

```text
git tag vX.Y.Z
        |
        v
GitHub Actions
        |
        +-- tests
        +-- Windows amd64 build
        +-- Linux amd64 build
        +-- Linux arm64 build
        +-- macOS arm64 build
        +-- signing / notarization as applicable
        +-- checksums / integrity metadata
        +-- GitHub Release
```

No official release artifact should be built manually on a developer laptop.

### Platform release requirements

Windows:

- native amd64 GUI build
- installer/package
- code signing before commercial distribution

Linux amd64/arm64:

- retain current appliance/CLI packaging where required
- add GUI artifact/package
- preserve integrity verification

macOS Apple Silicon:

- native arm64 application
- Apple code signing
- notarization before commercial distribution
- distributable `.app`, `.dmg`, or approved installer format

### Release integrity

Preserve the existing fail-closed philosophy: an official release should not silently fall back to an unsigned/unverified artifact when required signing material is missing.

## 16. Relationship with existing runbooks

This document is the installer/product UX layer.

It should reference, not duplicate, the deeper technical runbooks:

- `EDGE_INSTALL_FROM_SCRATCH.md`
- `CAMERA_FROM_SCRATCH_EDGE.md`
- `FULL_EDGE_FROM_SCRATCH.md`
- `EDGE_RELEASE_UPDATE_ROLLBACK.md`

When the UI performs one of those technical operations, the technical runbook remains the detailed engineering source; this document defines how that capability is exposed safely to the installer/operator.

## 17. Implementation milestones

### UX-0 — Baseline audit and contracts

Status: `COMPLETED (AUDITED)`

Contract Audit Document: [`docs/product/UX0_INSTALLER_CONTRACT_AUDIT.md`](../product/UX0_INSTALLER_CONTRACT_AUDIT.md)

Deliverables:

- map every wizard action to an existing Go service/API or identify the missing service boundary
- define installer state machine
- define persistent installer state
- define secret-handling rules
- pin exact Cloud/Hybrid/Full Edge configuration mappings

Exit criterion: no UI button depends on undefined backend behavior (MET — see audit specification).

### UX-1 — Wails application shell

Status: `IMPLEMENTED / TESTED LOCAL`

Specification & Architecture: [`docs/product/UX1_WAILS_SHELL.md`](../product/UX1_WAILS_SHELL.md)

Deliverables:

- Wails v2 application structure (`cmd/geocam-edge-ui`)
- React + TypeScript + Vite frontend
- shared design shell
- Go bindings/service façade (`internal/installer`)
- navigation/state model
- development build on at least one supported desktop platform (macOS arm64 tested)

Exit criterion: GUI can call a safe Go status method and display real Edge state (MET — tested via Wails production build and Go bindings).

### UX-2 — Enrollment wizard

Status: `IMPLEMENTED`

Branch (SaaS): `feature/ux2-edge-claim`
Branch (Edge): `feature/ux2-secure-enrollment`
Detailed documentation: `docs/product/UX2_SECURE_ENROLLMENT.md`

Deliverables:

- ✅ short-lived enrollment flow (Crockford Base32 one-time codes)
- ✅ device credential provisioning (atomic persistence, 0600 perms)
- ✅ secure local persistence (temp+rename+fsync, SHA-256 hash only to SaaS)
- ✅ no SaaS admin password persistence (only one-time code used, consumed on claim)
- ✅ retry/revocation/error UX (SafeError codes, anti-brute-force, idempotent replay)
- ✅ React EnrollmentWizard with code auto-formatting and error states
- ✅ Go EnrollmentProvider with injectable SaaS client for testing
- ✅ SaaS endpoints: enrollment code generation (admin) + public claim
- ✅ 9 SaaS integration tests + 8 Edge Go tests + TypeScript/Vite build clean

Exit criterion: fresh install becomes an authenticated Edge device without storing a long-lived admin password. ✅ Verified

### UX-3 — Mode/profile configuration

Status: `TODO`

Deliverables:

- Cloud / Hybrid / Full Edge selection screen
- simple operational explanation
- backend validation of requested profile
- effective-profile verification after start/restart

Exit criterion: selected product mode matches reported effective runtime profile.

### UX-4 — Camera IP onboarding

Status: `PARTIAL BACKEND EXISTS / UI TODO`

Deliverables:

- local discovery screen
- camera selection
- credential entry and safe test
- assignment
- RTSP validation
- camera health/status presentation

Exit criterion: technician can add a supported IP camera without CLI/config-file editing.

### UX-5 — Commissioning and diagnostics

Status: `TODO`

Deliverables:

- automated post-configuration checks
- mode-specific validation
- PASS/ACTION_REQUIRED/BLOCKED model
- persistent status screen
- safe troubleshooting information

Exit criterion: installer cannot show success unless the selected runtime path is actually operational.

### UX-6 — DVR/NVR multichannel

Status: `SEPARATE MILESTONE / NOT_VALIDATED`

Required technical work before enabling the GUI option:

- per-channel stable identity
- channel enumeration/discovery
- per-channel credential/stream mapping
- RTSP URI/profile selection per channel
- independent supervisor/pipeline/status per channel
- SaaS camera/channel model agreement
- Cloud/Hybrid/Full Edge behavior per channel
- physical DVR/NVR E2E test

Exit criterion: multiple channels from one real DVR/NVR operate independently without identity collapse or silent loss.

### UX-7 — Cross-platform build and GitHub Releases

Status: `PARTIAL — LINUX RELEASE PIPELINE EXISTS`

Deliverables:

- Windows amd64 GUI release
- Linux amd64 GUI release
- Linux arm64 GUI release
- macOS arm64 GUI release
- platform signing/notarization
- checksums/integrity metadata
- tag-driven GitHub Release

Exit criterion: one release tag publishes all supported official artifacts automatically.

### UX-8 — Installer E2E and field acceptance

Status: `TODO`

Test matrix:

- clean install on every supported OS/architecture
- enrollment
- camera discovery
- wrong credential recovery
- camera online/offline recovery
- Cloud commissioning
- Hybrid commissioning
- Full Edge commissioning
- reboot persistence
- upgrade
- rollback
- uninstall/reinstall behavior
- no secret leakage

Exit criterion: a technician unfamiliar with repository internals can complete installation using only the product UI and documented prerequisites.

## 18. Current state summary

| Area | Current state | Next action |
|---|---|---|
| Core Edge/CLI | Exists | Reuse, do not duplicate |
| Backend/UI Contract Audit | Completed (`UX0_INSTALLER_CONTRACT_AUDIT.md`) | Base for UX-1 facade |
| Camera technical runbook | Exists | Use as backend truth for UI |
| Full Edge technical runbook | Exists | Use as backend truth for UI |
| Linux release workflow | Exists for current appliance artifacts | Extend for GUI/cross-platform |
| GUI installer | Shell implemented (`cmd/geocam-edge-ui`) | UX-2 (Enrollment) |
| Enrollment UX | Needs productized flow | UX-2 |
| Cloud/Hybrid/Full Edge selector | Backend modes exist; GUI absent | UX-3 |
| Camera IP onboarding GUI | Backend pieces exist; GUI absent | UX-4 |
| Commissioning dashboard | Not implemented | UX-5 |
| DVR/NVR multichannel | Not validated / separate milestone | UX-6 |
| Windows GUI release | Not implemented | UX-7 |
| macOS Apple Silicon GUI release | Not implemented | UX-7 |
| End-user installer E2E | Not done | UX-8 |

## 19. Recommended implementation order

Do not start by drawing every final screen and then force the backend to match it.

Recommended order:

1. UX-0 — backend/UI contract audit.
2. UX-1 — Wails shell and real status call.
3. UX-2 — enrollment.
4. UX-3 — processing-mode/profile configuration.
5. UX-4 — camera IP onboarding.
6. UX-5 — commissioning/diagnostics.
7. UX-7 — cross-platform release automation.
8. UX-8 — full installer E2E/field validation.
9. UX-6 — DVR/NVR can proceed as a parallel technical track, but its UI must stay disabled until its own acceptance criteria are green.

## 20. Definition of done for Installer v1

Installer v1 is done when all of the following are true:

- official builds exist for Windows amd64, Linux amd64, Linux arm64, and macOS arm64
- technician can enroll a fresh Edge without storing SaaS admin credentials locally
- technician can choose Cloud, Hybrid, or Full Edge
- technician can discover and configure supported IP cameras
- camera credentials are handled securely
- installer validates ONVIF/RTSP and runtime health
- installer validates the selected processing path before declaring success
- device survives reboot with the selected configuration
- status/diagnostic UI is available after installation
- release artifacts are produced automatically from GitHub tags
- official artifacts have the required integrity/signing controls
- CLI/headless operation remains supported
- DVR/NVR is either fully accepted under its own milestone or visibly unavailable; it must never be falsely advertised as supported

---

This runbook is intentionally product-facing. Engineering details discovered while implementing each milestone should update the existing technical documents rather than turning this file into a duplicate architecture manual.
