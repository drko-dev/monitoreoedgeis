# UX-1: Wails v2 + React Desktop Installer Application Shell

> **Target Version:** GEO CAM Edge Installer v1.0  
> **Status:** IMPLEMENTED / TESTED LOCAL — **UX1_STATUS = COMPLETE** (re-verified during Final UX Closure: go build/vet/test, frontend tsc/Vite build, Wails darwin/arm64 build all green, no redesign)
> **Scope:** Working desktop application shell bridging Wails v2 to the Go installer facade.  
> **Reference Documents:**  
> - [`docs/runbooks/EDGE_INSTALLER_UI_UX.md`](../runbooks/EDGE_INSTALLER_UI_UX.md)  
> - [`docs/product/UX0_INSTALLER_CONTRACT_AUDIT.md`](UX0_INSTALLER_CONTRACT_AUDIT.md)

---

## 1. Application Location & Layout

The Wails v2 desktop application is located under `cmd/geocam-edge-ui/`, matching standard Go layout conventions (`cmd/<binary-name>`):

```text
drko-dev/monitoreoedgeis/
├── cmd/
│   ├── geocam-edge/            # Existing headless daemon CLI
│   └── geocam-edge-ui/         # NEW: Wails v2 desktop GUI entrypoint
│       ├── wails.json          # Wails v2 project configuration
│       ├── main.go             # Application window lifecycle & options
│       ├── app.go              # Wails <-> Go bridge facade
│       ├── app_test.go         # App bridge integration tests
│       └── frontend/           # React + TypeScript + Vite GUI
│           ├── package.json
│           ├── vite.config.ts
│           ├── tsconfig.json
│           ├── index.html
│           ├── wailsjs/        # Automatically generated Wails bindings
│           │   └── go/main/App # GetSystemReport, GetInstallerState
│           └── src/
│               ├── main.tsx
│               ├── App.tsx
│               ├── App.css
│               ├── types/      # DTOs matching internal/installer
│               ├── services/   # Safe IPC API client
│               ├── hooks/      # useInstaller hook
│               └── components/ # Header, SystemCard, StateCard, etc.
└── internal/
    └── installer/              # NEW: Dedicated in-process Go facade
        ├── types.go            # SystemReport, InstallerState, DTOs
        ├── service.go          # System inspection & state derivation
        ├── privilege_unix.go   # POSIX privilege level detection
        ├── privilege_windows.go# Windows token elevation detection
        └── service_test.go     # Unit tests (secrets, lock, states)
```

---

## 2. Architecture & Hybrid Boundary

As determined in UX-0, the application shell implements **Phase 1 (In-Process Setup)** of the Hybrid Architecture:

```text
+-------------------------------------------------------------+
|               Wails v2 Desktop App Process                  |
|  +-------------------------------------------------------+  |
|  |             React 19 + TypeScript + Vite              |  |
|  +-------------------------------------------------------+  |
|                             | Wails JS Bridge                |
|                             v                               |
|  +-------------------------------------------------------+  |
|  |                  cmd/geocam-edge-ui                   |  |
|  |            App.GetSystemReport / State                |  |
|  +-------------------------------------------------------+  |
|                             |                               |
|                             v                               |
|  +-------------------------------------------------------+  |
|  |               internal/installer.Service              |  |
|  |  (Read-only environment inspection & state derivation)|  |
|  +-------------------------------------------------------+  |
+-------------------------------------------------------------+
          | (Safe reads only)                   | (Does NOT acquire)
          v                                     X
   Host OS & Hardware               .geocam-edge.lock (Advisory Lock)
   (CPU, RAM, Disk, Paths)
```

### Architectural Guarantees
1. **Single Entrypoint Facade:** The frontend communicates strictly with `App` in `cmd/geocam-edge-ui`, which delegates exclusively to `internal/installer.Service`. The UI does not import or call `internal/config`, `internal/rtsp`, `internal/credentials`, or other domain packages directly.
2. **Instance Lock Safety:** The installer does NOT acquire the `internal/instance.Acquire` lock on `DataDir`. Background daemons running concurrently will not be blocked, killed, or disrupted by the installer opening.
3. **No Daemon Duplication:** Opening the installer shell does not spawn or start an Edge pipeline daemon.

---

## 3. Go Facade Contracts (`internal/installer`)

The facade exposes two safe, non-mutating query methods:

### 1. `GetSystemReport(ctx context.Context) (*SystemReport, error)`
Returns a sanitized overview of host capabilities and Edge prerequisites:
- `os`, `arch`, `edge_version`, `commit`, `build_date`, `hostname`
- `privilege_level`: `STANDARD_USER`, `ADMINISTRATOR`, `ROOT`, `UNKNOWN`
- `platform_supported`: Boolean flag verified via `platform.Detect()`
- `data_dir`: Resolved data path (`config.DefaultDataDir` or configured path)
- `service_installed`: Boolean flag indicating whether background OS service is registered
- `daemon_running`: Probes local loopback health endpoint (`127.0.0.1:8091/healthz`)
- `owns_instance_lock`: Always `false` (guarantees UI does not claim lock)
- `config_present`: Boolean flag indicating whether configuration file exists
- `enrolled`: Boolean flag indicating whether valid identity and credentials exist
- `total_ram_mb`, `free_ram_mb`, `disk_path`, `free_disk_gb`
- `ffmpeg_present`, `ffprobe_path`
- `has_gpu_support`, `gpu_info`

### 2. `GetInstallerState(ctx context.Context) (*InstallerState, error)`
Evaluates disk and runtime facts to derive the state machine position:
- `state`: `NEW`, `SYSTEM_CHECK`, `NEEDS_ENROLLMENT`, `ENROLLED`, `ACTION_REQUIRED`, `BLOCKED`
- `reason_code`: e.g. `FRESH_INSTALL`, `DEVICE_NOT_ENROLLED`, `DEVICE_ENROLLED`, `UNSUPPORTED_PLATFORM`
- `safe_message`: Localized, sanitized human-readable guidance
- `recoverable`: Boolean flag indicating whether technician can take corrective action
- `next_allowed_actions`: Array of permitted next action keys (e.g. `["PROCEED_TO_ENROLLMENT", "REFRESH"]`)
- `device_id`: Public Edge device ID (if already enrolled)

---

## 4. Frontend Structure & Visual Design

- **Framework:** React 19, TypeScript 5.6, Vite 7.
- **Styling:** Custom accessible slate-dark theme (`App.css`), high contrast, no external CSS libraries or remote fonts/CDNs. Works fully offline.
- **Component Model:**
  - `Header`: Product branding, version badge.
  - `SystemCard`: Verified checklist of OS, architecture, memory, disk, and platform support.
  - `StateCard`: Dynamic status badge, safe message, device ID container.
  - `Button`: Accessible button with loading indicators, keyboard focus rings (`focus-visible`).
  - `ErrorAlert`: Diagnostic alert with retry action.
  - `Advanced Information`: Collapsible section displaying DataDir, Daemon status, Instance lock safety, and Privilege tier.
- **Non-Mutating Policy:** The `Continue` button detects the state. Under `NEEDS_ENROLLMENT`, it displays an informative modal/notice that enrollment will take place in milestone UX-2. It does NOT make network calls to SaaS and does NOT write files to disk.

---

## 5. Security & Threat Mitigation

| Risk | Mitigation in UX-1 |
|---|---|
| **Secret Leakage to GUI** | Neither `SystemReport` nor `InstallerState` contain credential fields, JWTs, master keys, or passwords. Passwords are never returned over IPC. |
| **Arbitrary Shell Execution** | Wails bindings expose only `GetSystemReport` and `GetInstallerState`. No generic `ExecuteCommand`, `OpenFile`, or shell proxies exist. |
| **Concurrent Daemon Conflict** | `owns_instance_lock` is guaranteed false. The installer does not invoke `instance.Acquire()`. |
| **Local Storage Pollution** | Zero tokens or secrets are stored in `localStorage`, `sessionStorage`, or `IndexedDB`. |
| **Privilege Escalation** | GUI runs as standard unprivileged desktop user. No `sudo`, `pkexec`, or `osascript` executed. |

---

## 6. Verification & Test Evidence

### Go Test Suite
```bash
go test -v ./internal/installer ./cmd/geocam-edge-ui
```
- `TestGetSystemReportDoesNotExposeSecrets`: PASS
- `TestInstanceLockRemainsAvailable`: PASS
- `TestDeriveInstallerState`: PASS
- `TestConfigPresenceDetection`: PASS
- `TestSanitizeError`: PASS
- `TestAppBindingsReturnSafeData`: PASS

### Frontend Test Suite
```bash
npm --prefix cmd/geocam-edge-ui/frontend test
```
- `SystemReport structure conforms to non-sensitive contract`: PASS
- `InstallerState supports valid state codes and transitions`: PASS
- `Continue action policy in UX-1 is strictly non-mutating`: PASS
- `Blocked state disables progression`: PASS

### Local Production Build
```bash
cd cmd/geocam-edge-ui && wails build
```
- Built and packaged `geocam-edge-ui.app` for `darwin/arm64` in 6.8s.

---

## 7. Known Gaps & UX-2 Prerequisites

### Known Gaps
1. **Wizard Flow Inactive:** The wizard does not advance past the initial status screen (by design for UX-1).
2. **SaaS Short-Lived Claim API:** SaaS (`drko-dev/monitoreoia`) does not yet expose `POST /api/v1/edge/claim`.

### UX-2 Prerequisites
1. Introduce `ClaimRequest` and `ClaimResult` binding methods in `internal/installer`.
2. Implement UX-2 OTP/token input screen with countdown timer and input masking.
3. Establish `EnrollmentProvider` adapter supporting both temporary bridge and future OTP claim endpoint.
