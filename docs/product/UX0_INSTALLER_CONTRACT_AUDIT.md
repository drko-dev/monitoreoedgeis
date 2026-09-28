# UX-0: Edge Installer UI/UX Backend Contract Audit & Architectural Specification

> **Target Version:** GEO CAM Edge Installer v1.0  
> **Status:** AUDITED & APPROVED FOR UX-1 — **UX0_STATUS = COMPLETE** (see Final UX Closure Update at the end of this document; the SaaS enrollment gap this document originally flagged as blocking is resolved)
> **Scope:** Definitive technical bridge between the planned Wails v2 + React desktop interface and the existing Go backend services.  
> **Reference Runbook:** [`docs/runbooks/EDGE_INSTALLER_UI_UX.md`](file:///Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/ux0-installer-contract-audit/docs/runbooks/EDGE_INSTALLER_UI_UX.md)

---

## 1. Executive Summary

This document defines the architectural contract between the proposed Wails v2 desktop user interface and the existing GEO CAM Edge Go codebase. It resolves the core design question:

> *"If we build a Wails + React desktop installer tomorrow, which real Go functions does each screen invoke, which adapter facades are required, which capabilities are currently missing from the backend, and how do we prevent architectural anti-patterns such as concurrent file lock contention or secret leakage?"*

### Key Findings
1. **Core Domain Completeness:** Approximately 70% of the underlying business logic required for installation already exists in mature Go packages (`internal/discovery`, `internal/onviftest`, `internal/rtsptest`, `internal/cameracreds`, `internal/credentials`, `internal/identity`, `internal/config`, and `internal/health`).
2. **Missing Architectural Facade:** The existing code is exposed primarily via CLI subcommands (`cmd/geocam-edge/*.go`) or background daemon subsystems. It lacks a cohesive, structured, in-process Application Service layer (`internal/installer`) with typed request/response contracts, timeout handling, and progress streaming suitable for desktop GUI bindings.
3. **Primary External Blocker (SaaS Enrollment Gap) — RESOLVED.** Current enrollment (`internal/credentials.Enroll`) requires long-lived SaaS administrator credentials (`admin_user`, `admin_password`) transmitted directly to SaaS `POST /api/v1/edge/enroll`. This violates the security boundary for field technician tools. A short-lived, single-use claim token workflow (`POST /api/v1/edge/claim`) must be introduced on SaaS or emulated safely. *Update (Final UX Closure): this is implemented. The Edge side (`internal/installer.Service.ClaimDevice`, short-lived Crockford enrollment code, zero-knowledge device credential) has existed since UX-2. The SaaS side (`POST /api/v1/edge/claim` + `POST/GET /api/v1/edge/enrollment-codes`) is implemented and functionally validated end-to-end (real HTTP, real Postgres, real Go installer) on `monitoreoia` branch `feature/ux2-claim-closure` — not yet on `main`. No admin password is ever transmitted by the Edge for this flow.*
4. **Process Concurrency & Lock Contention:** The daemon (`geocam-edge run`) enforces single-instance mutual exclusion via `internal/instance.Acquire(dataDir)`. A pure in-process Wails application cannot inspect or control a running daemon if it attempts to re-acquire the same file lock. Consequently, a **Hybrid Architecture** is mandated: an in-process Facade during pre-installation/configuration, transitioning to an authenticated local loopback HTTP client (`127.0.0.1:8091`) once the background service is running.
5. **DVR/NVR Multi-channel Isolation:** Multi-channel DVR/NVR onboarding remains categorized as `SEPARATE_MILESTONE` / `NOT_VALIDATED` due to unaddressed identity collapse risks and channel multiplexing constraints. The UI must explicitly disable or hide multi-channel DVR/NVR options in Installer v1.

---

## 2. Current Architecture of the Repository

The repository `drko-dev/monitoreoedgeis` is organized into a single entrypoint CLI and modular domain packages under `internal/`:

```text
cmd/geocam-edge/
├── main.go             # Root CLI entrypoint (cobra/flag parsing)
├── run.go              # Main daemon execution (agent loop, pipelines)
├── enroll.go           # CLI enrollment (interactive admin login)
├── discovery.go        # ONVIF/WS-Discovery CLI scanner
├── check.go            # Local sanity checks (ffmpeg, ports, config)
├── credential.go       # Camera credential store management
├── config.go           # Configuration inspection and validation
└── service.go          # OS service management (Windows/Linux)

internal/
├── agent/              # Pipeline supervisor and target reconciliation
├── cameracreds/        # Encrypted camera credential storage (AES-256-GCM)
├── cameratest/         # Camera target verification runner
├── cloudsink/          # Cloud processing mode (frame/media push to SaaS)
├── config/             # Environment variable and profile loader
├── control/            # SaaS remote control command receiver and ledger
├── credentials/        # Edge device token and certificate persistence
├── discovery/          # WS-Discovery multicast and ONVIF probe
├── edgebacklog/        # Offline SQLite event/evidence buffer
├── factoryreset/       # Secure wipe and factory reset procedures
├── fulledge/           # Local vision worker proxy (loopback HTTP :8090)
├── health/             # Loopback diagnostic HTTP server (:8091)
├── heartbeat/          # Periodic SaaS telemetry reporting
├── hybrid/             # Hybrid mode (local decode + candidate motion gating)
├── identity/           # Edge device identity metadata (identity.json)
├── instance/           # File-based single-instance mutual exclusion lock
├── onviftest/          # ONVIF authentication and profile extraction
├── ota/                # Over-The-Air firmware/binary update agent
├── platform/           # OS-specific paths and platform helpers
├── processing/         # Frame pipeline, pipeline workers, stats
├── remoteconfig/       # SaaS remote configuration synchronization
├── rtsp/               # RTSP client and ffmpeg/GStreamer ingest
├── rtsptest/           # RTSP probe, stream validation, codec/fps detector
├── service/            # Windows Service controller (kardianos/service)
├── systemd/            # Linux systemd unit generator and supervisor
└── transport/          # SaaS HTTP/gRPC client with retry and mTLS
```

---

## 3. Exhaustive UI/Backend Contract & Gap Matrix (Actions A to Z)

The following matrix maps every technician action across the installation and commissioning workflow to existing Go code, required authentication, side effects, classification, and implementation gaps.

### Classification Taxonomy
- `REUSE_DIRECT`: Package and function exist, signature is directly usable or requires trivial wrapping.
- `REUSE_WITH_FACADE`: Underlying logic exists, but requires a structured Facade to handle concurrency, context timeouts, data conversion, or progress events.
- `MISSING_EDGE_SERVICE`: Logic does not exist in `internal/`; must be created in Edge Go codebase.
- `MISSING_BACKEND`: Requires changes or endpoints in SaaS API (`drko-dev/monitoreoia`).
- `UNSAFE_FOR_GUI`: Action as currently implemented violates UI security or concurrency rules (e.g. requires raw root, leaks passwords, or blocks the GUI thread).
- `SEPARATE_MILESTONE`: Intentionally deferred; must be hidden/disabled in v1 GUI.

| Action | Wizard Screen / Area | Existing Go Package / File | Existing Function / Method | Required Auth | Side Effects on Disk / Network | Classification | Gaps / Technical Notes |
|---|---|---|---|---|---|---|---|
| **A: Pre-flight System Check** | Step 1: Welcome & Prerequisites | `internal/platform`, `internal/rtsptest` | `platform.DefaultDataDir()`, check `exec.LookPath("ffmpeg")` | Local OS user | Read CPU/RAM/Disk stats, verify write permissions on DataDir | `REUSE_WITH_FACADE` | Needs unified `SystemReport` returning OS, arch, memory, disk space, and external binaries (`ffmpeg`, `ffprobe`). |
| **B: Read Current Device State** | Step 1: Welcome | `internal/identity`, `internal/credentials` | `identity.Load()`, `credentials.Load()` | Local OS user | Read `identity.json`, `credentials.json` | `REUSE_DIRECT` | Returns whether device is already enrolled, device ID, and tenant/site metadata. |
| **C: Enroll with Short-Lived Code** | Step 2: SaaS Enrollment | `internal/installer` (`Service.ClaimDevice`) | `ClaimDevice(ctx, ClaimRequest)` | Short-lived Crockford claim code | Network request to SaaS `POST /api/v1/edge/claim`, writes `credentials.json`, `identity.json` via `credentials.Save`/`identity.Load` | `IMPLEMENTED` (was `MISSING_BACKEND`) | Resolved: SaaS `POST /api/v1/edge/claim` + admin `POST/GET /api/v1/edge/enrollment-codes` exist on `monitoreoia` `feature/ux2-claim-closure`, functionally validated end-to-end. Not yet on SaaS `main`. |
| **D: Configure Device Identity** | Step 2: SaaS Enrollment | `internal/identity` | `identity.Save(deviceIdentity)` | Local OS user | Writes `identity.json` | `REUSE_DIRECT` | Allows operator to set human-readable Edge name and site tag if not assigned by SaaS claim response. |
| **E: Select Processing Mode** | Step 3: Mode & Profile Selection | `internal/config` | `config.Load()`, validation logic | None | Modifies in-memory config struct | `REUSE_WITH_FACADE` | UI presents Cloud, Hybrid, Full Edge. Must map to `GEOCAM_PROCESSING_MODE` and profile requirements. |
| **F: Validate Mode Hardware Compatibility** | Step 3: Mode & Profile Selection | `internal/fulledge`, `internal/platform` | Custom inspection logic | None | Probes GPU (CUDA/DirectML/Metal), RAM (>4GB for Full Edge) | `MISSING_EDGE_SERVICE` | Full Edge requires local YOLO worker. Must check RAM and Python/model runtime availability before allowing selection. |
| **G: Scan Local Network Subnets** | Step 4: Camera Discovery | `net` (std lib), `internal/discovery` | `net.Interfaces()` | Local network access | Queries OS network interfaces and active IPv4 CIDR blocks | `REUSE_WITH_FACADE` | Needed so technician can select which NIC/VLAN to broadcast ONVIF discovery probes onto. |
| **H: Trigger ONVIF / WS-Discovery** | Step 4: Camera Discovery | `internal/discovery/wsdiscovery` | `wsdiscovery.Probe(ctx, timeout)` | Multicast network (UDP 3702) | Broadcasts WS-Discovery probe, listens for XML responses | `REUSE_WITH_FACADE` | Needs progress callback / event streaming to push discovered devices to React UI asynchronously. |
| **I: Enumerate Discovered Devices** | Step 4: Camera Discovery | `internal/discovery/onvif` | `onvif.GetDeviceInformation()` | None (pre-auth) | Queries basic device metadata (XAddrs, manufacturer) | `REUSE_WITH_FACADE` | Merges multicast responses, de-duplicates MAC/IP addresses, and returns UI view models. |
| **J: Manual Camera IP Entry** | Step 4: Camera Discovery (Manual) | `internal/onviftest` | `url.Parse()`, basic TCP dial | Local network access | TCP syn to RTSP/ONVIF ports (554, 80, 8080, 8899) | `REUSE_DIRECT` | Fallback for cameras on different VLANs or with WS-Discovery disabled. |
| **K: Test ONVIF Authentication** | Step 5: Camera Authentication | `internal/onviftest` | `onviftest.TestAuth(ctx, endpoint, user, pass)` | Camera admin / operator | ONVIF SOAP `GetCapabilities`, `GetProfiles` | `REUSE_WITH_FACADE` | Must return structured diagnostic: auth success, available profiles, PTZ capability, error code without leaking password. |
| **L: Extract Stream Profiles & URIs** | Step 5: Camera Authentication | `internal/onviftest` | `onviftest.GetStreamURI(ctx, profileToken)` | Camera credentials | ONVIF SOAP `GetStreamUri` | `REUSE_DIRECT` | Extracts main/sub-stream RTSP URLs (e.g. `rtsp://ip:554/stream1`). |
| **M: Probe RTSP Stream & Codec** | Step 5: Camera Stream Validation | `internal/rtsptest` | `rtsptest.Probe(ctx, rtspURL, user, pass)` | Camera RTSP auth | Runs `ffprobe` / lightweight RTSP client, decodes 1 frame | `REUSE_WITH_FACADE` | Returns video codec (`h264`, `hevc`), resolution (`1920x1080`), FPS (`15`), and decoding latency. |
| **N: Encrypt & Persist Camera Credentials** | Step 5: Camera Credential Storage | `internal/cameracreds` | `cameracreds.Store.Set(targetID, creds)` | Local OS user | Writes encrypted `camera_credentials.json` via AES-256-GCM | `REUSE_DIRECT` | Uses `camera_master.key`. Passwords must be zeroed in memory immediately after encryption. |
| **O: Assign Cameras as Edge Targets** | Step 5: Camera Target Assignment | `internal/agent`, `internal/cameratest` | Target struct creation | Local OS user | Stages camera target configurations in memory/plan | `REUSE_WITH_FACADE` | Associates camera ID, display name, RTSP URI, stream profile, and credential reference. |
| **P: Configure Storage & Retention** | Step 6: Storage & Backlog Paths | `internal/edgebacklog`, `internal/platform` | `edgebacklog.Open(dbPath)`, disk space check | Local filesystem write | Validates path existence, permissions, free space threshold | `REUSE_WITH_FACADE` | Sets local buffer size, retention days, and alerts if available disk space is under 10 GB. |
| **Q: Generate Configuration Plan** | Step 7: Review & Confirmation | `internal/config` | Proposed `ConfigService.Plan()` | None | Pure in-memory calculation | `MISSING_EDGE_SERVICE` | Compares current disk configuration with staged changes; outputs atomic diff for user review. |
| **R: Atomically Apply Configuration** | Step 7: Review & Confirmation | `internal/config`, `internal/platform` | Proposed `ConfigService.Apply()` | Local filesystem write | Writes `.env` / config files via atomic rename (`.tmp` -> `.json`) | `MISSING_EDGE_SERVICE` | Ensures no partial or corrupted configuration files on crash or power loss. |
| **S: Register & Start OS Service** | Step 8: Service Commissioning | `internal/service`, `internal/systemd` | `service.Install()`, `service.Start()` | Elevated (Admin/Root) | Registers Windows Service / systemd unit, starts daemon | `UNSAFE_FOR_GUI` | Requires OS elevation. GUI must invoke an elevated helper or trigger UAC/polkit prompt. |
| **T: Probe Local Daemon Health** | Step 9: Live Commissioning Checks | `internal/health` | HTTP `GET http://127.0.0.1:8091/healthz` | Loopback client | Reads JSON health status from running daemon | `REUSE_DIRECT` | Verifies daemon is active, running event loop, and listening on loopback. |
| **U: Validate SaaS Link & Heartbeat** | Step 9: Live Commissioning Checks | `internal/heartbeat`, `internal/transport` | `GET http://127.0.0.1:8091/status/saas` | Daemon internal auth | Checks last successful heartbeat timestamp and mTLS handshake | `REUSE_WITH_FACADE` | Health endpoint must expose SaaS connection status to loopback client. |
| **V: Validate Camera Media Ingestion** | Step 9: Live Commissioning Checks | `internal/processing`, `internal/rtsp` | `GET http://127.0.0.1:8091/status/cameras` | Daemon internal auth | Verifies frame counter > 0, ingest FPS > 0, no decode drop | `REUSE_WITH_FACADE` | Exposes per-camera pipeline metrics: FPS, frame drops, connection uptime. |
| **W: Validate Processing Mode Pipeline** | Step 9: Live Commissioning Checks | `internal/cloudsink`, `internal/fulledge` | `GET http://127.0.0.1:8091/status/pipeline` | Daemon internal auth | Cloud: verifies upload HTTP 200; Full Edge: verifies worker :8090 | `REUSE_WITH_FACADE` | Confirms inference is actually occurring according to selected mode before completing wizard. |
| **X: Render Commissioning Report** | Step 10: Completion | Frontend presentation | N/A | None | Exports commissioning receipt (`commissioning_report.json`) | `REUSE_WITH_FACADE` | Summarizes device ID, SaaS site, cameras online, processing mode, and software version. |
| **Y: Dashboard Diagnostics & Metrics** | Post-Install: Status Screen | `internal/health`, `internal/control` | `GET http://127.0.0.1:8091/metrics`, `/status` | Loopback token | Reads continuous metrics, uptime, camera health | `REUSE_WITH_FACADE` | Provides read-only dashboard for day-to-day monitoring without re-opening installer. |
| **Z: Factory Reset / Decommission** | Settings / Advanced Troubleshooting | `internal/factoryreset` | `factoryreset.Execute(wipeData, wipeAudit)` | Local OS admin | Removes credentials, keys, config, SQLite logs | `REUSE_DIRECT` | Destructive. Requires explicit confirmation dialog and confirmation typing in GUI. |

---

## 4. Proposed Go Installer Facade (`internal/installer`)

To prevent the React UI from depending directly on internal packages or CLI commands, a dedicated facade package `internal/installer` will be introduced in milestone UX-1.

### Architectural Component Diagram
```text
+-------------------------------------------------------------+
|                     Wails v2 Desktop App                    |
|  +-------------------------------------------------------+  |
|  |             React + TypeScript Frontend               |  |
|  +-------------------------------------------------------+  |
|                             | Wails JS/Go Bridge             |
|                             v                               |
|  +-------------------------------------------------------+  |
|  |             internal/installer.Facade                 |  |
|  |  (Context timeout management, validation, events)    |  |
|  +-------------------------------------------------------+  |
|         |                     |                    |        |
|         v                     v                    v        |
|  +-------------+      +---------------+      +-----------+  |
|  | Discovery   |      | Credentials   |      | Config    |  |
|  | & ONVIF/RTSP|      | & Identity    |      | Service   |  |
|  +-------------+      +---------------+      +-----------+  |
+-------------------------------------------------------------+
```

### Go Interface and Contract Definitions

```go
package installer

import (
	"context"
	"time"
)

// Facade defines the unified contract exposed directly to Wails bindings.
type Facade interface {
	// Preflight & System
	GetSystemReport(ctx context.Context) (*SystemReport, error)
	GetInstallerState(ctx context.Context) (*InstallerState, error)
	SaveInstallerStep(ctx context.Context, step InstallerStep) error

	// Enrollment & Identity
	CheckEnrollment(ctx context.Context) (*EnrollmentStatus, error)
	ClaimDevice(ctx context.Context, req ClaimRequest) (*ClaimResult, error)
	UpdateIdentity(ctx context.Context, req UpdateIdentityRequest) error

	// Discovery & Probing
	ListNetworkInterfaces(ctx context.Context) ([]NetworkInterface, error)
	StartDiscovery(ctx context.Context, iface string, timeoutSec int) (<-chan DiscoveredCamera, error)
	TestONVIFAuth(ctx context.Context, req ONVIFAuthRequest) (*ONVIFAuthResult, error)
	ProbeRTSPStream(ctx context.Context, req RTSPProbeRequest) (*RTSPProbeResult, error)

	// Camera Configuration
	SaveCameraTarget(ctx context.Context, target StagedCameraTarget) error
	RemoveCameraTarget(ctx context.Context, targetID string) error
	ListStagedCameras(ctx context.Context) ([]StagedCameraTarget, error)

	// Mode & Persistence
	GetModeCapabilities(ctx context.Context, mode string) (*ModeCapabilityReport, error)
	PlanConfiguration(ctx context.Context, req ConfigurationPlanRequest) (*ConfigurationPlan, error)
	ApplyConfiguration(ctx context.Context, planID string) (*ApplyResult, error)

	// Service & Commissioning
	InstallAndStartService(ctx context.Context) (*ServiceOperationResult, error)
	VerifyCommissioning(ctx context.Context, timeout time.Duration) (<-chan CommissioningCheckUpdate, error)
	GetDashboardStatus(ctx context.Context) (*DashboardStatus, error)

	// Maintenance
	ExecuteFactoryReset(ctx context.Context, confirmPhrase string) error
}

// Data Transfer Objects (DTOs)

type SystemReport struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	TotalRAMMB    uint64 `json:"total_ram_mb"`
	FreeRAMMB     uint64 `json:"free_ram_mb"`
	DiskPath      string `json:"disk_path"`
	FreeDiskGB    uint64 `json:"free_disk_gb"`
	FFmpegPresent bool   `json:"ffmpeg_present"`
	FFprobePath   string `json:"ffprobe_path"`
	HasGPUSupport bool   `json:"has_gpu_support"`
	GPUInfo       string `json:"gpu_info,omitempty"`
}

type ClaimRequest struct {
	SaaSURL   string `json:"saas_url"`
	ClaimCode string `json:"claim_code"`
	SiteID    string `json:"site_id,omitempty"`
	EdgeName  string `json:"edge_name"`
}

type ClaimResult struct {
	Success        bool   `json:"success"`
	DeviceID       string `json:"device_id"`
	OrganizationID string `json:"organization_id"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

type DiscoveredCamera struct {
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	MACAddress   string `json:"mac_address"`
	ONVIFURL     string `json:"onvif_url"`
	IsConfigured bool   `json:"is_configured"`
}

type ONVIFAuthRequest struct {
	EndpointURL string `json:"endpoint_url"`
	Username    string `json:"username"`
	Password    string `json:"password"` // Transferred in memory only, zeroed after use
}

type ONVIFAuthResult struct {
	Authenticated bool     `json:"authenticated"`
	Profiles      []string `json:"profiles"`
	StreamURI     string   `json:"stream_uri,omitempty"`
	ErrorMessage  string   `json:"error_message,omitempty"`
}

type RTSPProbeRequest struct {
	RTSPURL  string `json:"rtsp_url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type RTSPProbeResult struct {
	Success      bool    `json:"success"`
	Codec        string  `json:"codec"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	FPS          float64 `json:"fps"`
	ErrorMessage string  `json:"error_message,omitempty"`
}

type StagedCameraTarget struct {
	TargetID    string `json:"target_id"`
	DisplayName string `json:"display_name"`
	IP          string `json:"ip"`
	RTSPURL     string `json:"rtsp_url"`
	Username    string `json:"username"`
	Password    string `json:"password"` // Zeroed after persistence
	ProfileName string `json:"profile_name"`
}

type ConfigurationPlanRequest struct {
	ProcessingMode string               `json:"processing_mode"` // "cloud", "hybrid", "edge"
	DataDirectory  string               `json:"data_directory"`
	Cameras        []StagedCameraTarget `json:"cameras"`
}

type ConfigurationPlan struct {
	PlanID       string   `json:"plan_id"`
	DiffSummary  []string `json:"diff_summary"`
	RequiresRoot bool     `json:"requires_root"`
	Valid        bool     `json:"valid"`
	Errors       []string `json:"errors,omitempty"`
}

type CommissioningCheckUpdate struct {
	CheckName string `json:"check_name"` // e.g., "Daemon Health", "SaaS Heartbeat", "RTSP Feed 1"
	Status    string `json:"status"`     // "PENDING", "PASS", "ACTION_REQUIRED", "BLOCKED"
	Details   string `json:"details"`
}
```

---

## 5. Architectural Decision: Wails Bindings vs. Local HTTP

A critical technical decision is whether the Wails desktop interface should communicate with the Edge system via **Direct In-Process Go Bindings** or via a **Local Loopback HTTP API**.

### Comparative Evaluation

| Architectural Property | In-Process Wails Bindings | Local Loopback HTTP API (`127.0.0.1:8091`) |
|---|---|---|
| **Execution Context** | Runs inside the user's desktop application process. | Runs inside the background OS service (`geocam-edge run`). |
| **Availability on Clean Machine** | Available immediately upon launching the installer (zero daemon required). | Not available until the service is installed, configured, and started. |
| **Single-Instance Lock Conflict** | Cannot run concurrently with `geocam-edge run` if both call `instance.Acquire()`. | No file lock conflict; daemon owns the lock, client connects over TCP socket. |
| **OS Privileges** | Runs under the logged-in desktop user's standard permissions. | Background daemon can run under `SYSTEM` (Windows) or `geocam` user (Linux). |
| **Port & Firewall Conflicts** | None. No ports opened. | Requires binding a loopback port (default `8091`), which could conflict or be blocked by strict endpoint policies. |
| **Authentication & Protection** | Memory boundary enforced by OS process isolation. | Requires ephemeral localhost bearer token or Unix Domain Socket permissions to prevent local privilege escalation. |
| **Post-Install Lifecycle** | Dies when the technician closes the GUI window. | Daemon remains continuously active in background; GUI can reconnect anytime. |

### Architectural Decision: The Hybrid Model

Neither pure bindings nor pure HTTP satisfies all requirements. Therefore, GEO CAM Edge adopts a **Phased Hybrid Architecture**:

```text
+-----------------------------------------------------------------------------------+
| PHASE 1: INSTALLATION & CONFIGURATION (Daemon NOT running)                         |
|                                                                                   |
|  [Wails Desktop GUI] ---> (In-Process Go Facade: internal/installer)             |
|                                     |                                             |
|                                     v                                             |
|                           [Direct Disk & Hardware]                                |
|                           - Preflight system checks                               |
|                           - ONVIF Discovery & RTSP probing                        |
|                           - Credential encryption (camera_master.key)             |
|                           - ConfigService.Apply()                                 |
+-----------------------------------------------------------------------------------+
                                      |
                                      v (Elevated Service Installation)
+-----------------------------------------------------------------------------------+
| PHASE 2: COMMISSIONING & DASHBOARD (Daemon IS running as OS Service)              |
|                                                                                   |
|  [Wails Desktop GUI]                                                              |
|          |                                                                        |
|          | HTTP REST / SSE (Loopback 127.0.0.1:8091 + Local Ephemeral Token)      |
|          v                                                                        |
|  [geocam-edge background daemon] (Holds instance.Acquire lock)                    |
|          |--> internal/health (Health & Status endpoints)                         |
|          |--> internal/rtsp (Active video ingest)                                 |
|          |--> internal/processing (Pipeline metrics)                              |
+-----------------------------------------------------------------------------------+
```

1. **Pre-Commissioning (Installer Mode):** Wails uses direct in-process bindings (`internal/installer`). The daemon is not running, so there is no `instance.Acquire` lock conflict. The facade performs discovery, probe, credential encryption, and writes configuration files.
2. **Service Transition:** The facade invokes the platform service manager to register and start the daemon.
3. **Post-Commissioning (Monitor / Dashboard Mode):** Wails switches its communication channel to the local loopback HTTP server (`http://127.0.0.1:8091`), authenticated via a short-lived loopback secret written by the daemon to `dataDir/loopback_auth.token` (0600). This allows the GUI to be opened and closed at any time without restarting or disturbing the daemon.

---

## 6. Installer State Machine

To satisfy Requirement 4.8 (*"The installer must be resumable where practical; a failure near the end must not force the user to repeat successful discovery/enrollment unnecessarily"*), the installation process is modeled as a deterministic state machine persisted to disk in `installer_state.json`.

### State Diagram

```mermaid
stateDiagram-v2
    [*] --> UNINITIALIZED
    UNINITIALIZED --> PREFLIGHT_CHECK: App Launch
    PREFLIGHT_CHECK --> ENROLLMENT_REQUIRED: System OK & Not Enrolled
    PREFLIGHT_CHECK --> MODE_SELECTION: Already Enrolled
    PREFLIGHT_CHECK --> BLOCKED: Insufficient Resources / Missing Binaries

    ENROLLMENT_REQUIRED --> ENROLLING: Submit Claim Code
    ENROLLING --> MODE_SELECTION: SaaS Claim Succeeded
    ENROLLING --> ENROLLMENT_REQUIRED: Invalid Token / Network Error

    MODE_SELECTION --> CAMERA_DISCOVERY: Mode Selected & Validated
    MODE_SELECTION --> BLOCKED: Hardware Incompatible for Mode

    CAMERA_DISCOVERY --> CAMERA_CONFIG: Cameras Discovered / Entered
    CAMERA_CONFIG --> CAMERA_TESTING: Test Credentials & RTSP
    CAMERA_TESTING --> CAMERA_CONFIG: Test Failed (Retryable)
    CAMERA_TESTING --> STORAGE_CONFIG: All Cameras Validated

    STORAGE_CONFIG --> CONFIG_REVIEW: Paths & Retention Set
    CONFIG_REVIEW --> APPLYING_CONFIG: Confirm Configuration
    APPLYING_CONFIG --> SERVICE_INSTALL: Config Written Atomically

    SERVICE_INSTALL --> COMMISSIONING_VERIFICATION: Service Started
    SERVICE_INSTALL --> FAILED_RETRYABLE: Elevation Denied / Service Error

    COMMISSIONING_VERIFICATION --> COMPLETED: All Runtime Checks Pass
    COMMISSIONING_VERIFICATION --> FAILED_RETRYABLE: Ingestion / SaaS Offline

    COMPLETED --> DASHBOARD: Open Dashboard
    DASHBOARD --> [*]
```

### State Persistence Schema (`installer_state.json`)

```json
{
  "version": 1,
  "current_state": "CAMERA_CONFIG",
  "updated_at": "2026-09-27T23:15:30Z",
  "device_id": "edge_dev_01j9a8b7c6d5e4f3",
  "site_id": "site_ar_cordoba_central",
  "selected_mode": "hybrid",
  "staged_cameras": [
    {
      "target_id": "cam_front_gate",
      "display_name": "Front Gate Tapo C200",
      "ip": "192.168.1.150",
      "onvif_validated": true,
      "rtsp_validated": true,
      "codec": "h264",
      "fps": 15.0
    }
  ],
  "commissioning_checks": {
    "daemon_health": "PASS",
    "saas_heartbeat": "PASS",
    "camera_streams": "PENDING"
  }
}
```

- **Idempotency & Resumption:** If the installer is closed during `CAMERA_CONFIG`, re-opening it detects `installer_state.json`, validates that device identity and credentials are intact, and restores the technician directly to Step 4 without re-prompting for SaaS enrollment.
- **State Invalidation:** Clicking "Start Over" or changing the Device Identity resets `installer_state.json` and transitions to `UNINITIALIZED`.

---

## 7. Mode and Profile Configuration Mapping

The installer UI must present clear, product-oriented choices while mapping deterministically to the low-level environment variables and components expected by GEO CAM Edge.

| Product Mode (UI) | Internal Env Var (`GEOCAM_PROCESSING_MODE`) | Internal Agent Profile (`GEOCAM_AGENT_PROFILE`) | Media Pipeline Active | Local Decode Active | Local YOLO Inference | Evidence Storage | SaaS Upload Behavior |
|---|---|---|---|---|---|---|---|
| **Cloud** | `cloud` | `gateway` | Yes | No (Pass-through) | No | Ephemeral RAM ring buffer | Full raw video/frames sent to SaaS for Cloud YOLO processing via `cloudsink`. |
| **Hybrid** | `hybrid` | `gateway` | Yes | Yes (Local keyframes) | Gating only (motion/candidate filter) | Local SQLite backlog for candidates | Only candidate frames and alert clips uploaded to SaaS Cloud YOLO. |
| **Full Edge** | `edge` | `workstation` or `server` | Yes | Yes | Yes (Full local YOLO via vision worker) | Full local event & evidence backlog (`edgebacklog`) | Inferences run locally; only metadata, events, and confirmed evidence sent to SaaS. |

### Technical Subsystem Dependencies for Full Edge
If the technician selects **Full Edge**, the installer must verify:
1. Python 3.10+ or embedded vision runtime exists on the target machine.
2. The vision worker script (`cloud_vision_worker.py`) is present.
3. The YOLO model artifact (`yolo11n.onnx` or `.pt`) is provisioned locally in `models/`.
4. Loopback port `8090` is free for IPC between `internal/fulledge` and the Python worker.
*If any dependency is missing, the UI must flag `ACTION_REQUIRED` or disable the Full Edge radio option with an explicit diagnostic.*

---

## 8. Configuration Persistence Design (`ConfigService`)

To eliminate manual `.env` file editing and prevent configuration corruption, an atomic configuration service will be established.

### Life-Cycle Pattern: Validate -> Plan -> Apply -> Rollback

```text
[Technician Settings] 
        |
        v
+-----------------------+      (Invalid)
| 1. Validate(cfg)      | -----------------> [Return Field Errors to UI]
+-----------------------+
        | (Valid)
        v
+-----------------------+
| 2. Plan(cfg)          | -----------------> [Display Diff Summary in UI]
+-----------------------+
        | (Technician Confirms)
        v
+-----------------------+
| 3. Apply(planID)      |
|    - Backup current   | (Error during write)
|    - Write .tmp files | -----------------> [Rollback to Backup & Report]
|    - Atomic rename    |
|    - fsync directory  |
+-----------------------+
        | (Success)
        v
[Configuration Locked on Disk]
```

### Safety Guarantees
1. **Atomic File Replacement:** Files are written to `<filename>.tmp` and replaced via `os.Rename` (which is atomic on POSIX and modern Windows NTFS).
2. **Pre-allocation Backup:** Prior to applying changes, existing configurations are snapshotted into `dataDir/backups/config_<timestamp>.tar.gz`.
3. **Verification Before Deletion:** Old backups are retained (up to 5 versions) to support manual or automated rollbacks if the daemon fails its initial boot test.

---

## 9. Enrollment Gap Analysis (`MISSING_BACKEND`)

### Current Implementation vs. Product Requirement

```text
CURRENT IMPLEMENTATION (Insecure for Field Tools):
Technician Laptop / GUI ---> Enters SaaS Admin Email & Password ---> POST /api/v1/edge/enroll
                                                                            |
                                                    SaaS Returns Device JWT + Identity
                                                                            |
                                                  * Admin credentials handled by GUI *
                                                  * Admin credentials risk leakage   *

REQUIRED FLOW (Secure Field Commissioning):
SaaS Web Console (Admin) ---> Generates 6-char Single-Use Claim Code (Valid for 15 mins)
                                       |
Technician enters Claim Code into GUI -+
        |
        v
Edge Installer Facade ---> POST /api/v1/edge/claim
                           Body: {
                             "claim_code": "K9X-42B",
                             "edge_name": "Warehouse-East",
                             "device_fingerprint": "SHA256(MAC+CPU+Motherboard)"
                           }
        |
        v
SaaS validates claim code, binds device to Tenant & Site, returns:
{
  "device_id": "edge_dev_01j9a8b",
  "device_token": "geocam_edg_...",
  "tenant_id": "ten_771",
  "site_id": "site_902"
}
        |
        v
Edge saves credentials.json (mode 0600) and discards claim code.
```

### Immediate Mitigation for UX-1 / UX-2
Until SaaS exposes `POST /api/v1/edge/claim`, the Edge `internal/installer` package will implement an adapter interface `EnrollmentProvider`:
- `ProductionClaimProvider`: Targets `POST /api/v1/edge/claim`.
- `LegacyAdminBridgeProvider`: Wraps `credentials.Enroll` in memory during development, immediately wiping admin credentials from RAM with `zeroBytes(password)` upon receiving the device token.

---

## 10. Camera Onboarding Contract

Onboarding follows a strict 5-stage validation pipeline before a camera is committed to persistent storage:

```mermaid
sequenceDiagram
    autonumber
    actor Tech as Technician (GUI)
    participant Facade as Installer Facade
    participant Disc as internal/discovery
    participant ONVIF as internal/onviftest
    participant RTSP as internal/rtsptest
    participant Store as internal/cameracreds

    Tech->>Facade: Start Discovery (Subnet / NIC)
    Facade->>Disc: wsdiscovery.Probe(timeout=5s)
    Disc-->>Facade: Discovered Camera IPs / XAddrs
    Facade-->>Tech: Render Camera List (Tapo, Hikvision, etc.)

    Tech->>Facade: Submit Credentials (user, pass)
    Facade->>ONVIF: TestAuth(XAddr, user, pass)
    ONVIF-->>Facade: Auth OK + Media Profiles ([Main, Sub])
    
    Facade->>ONVIF: GetStreamURI(MainProfile)
    ONVIF-->>Facade: rtsp://192.168.1.150:554/stream1

    Facade->>RTSP: Probe(URI, user, pass)
    Note over RTSP: Decodes test frame via ffprobe
    RTSP-->>Facade: Codec: H.264, 1920x1080, 15 FPS

    Facade->>Store: StoreEncrypted(TargetID, user, pass)
    Note over Store: Encrypts with camera_master.key (AES-GCM)
    Store-->>Facade: Stored (camera_credentials.json)

    Facade-->>Tech: Camera Validated (Ready for Assignment)
```

### Diagnostic Redaction Rule
At no point in this sequence may the RTSP URL containing inline credentials (`rtsp://user:pass@ip:port/stream`) be logged or emitted to the Wails frontend. All events must emit the sanitized URL (`rtsp://***:***@ip:port/stream`) and pass credentials out-of-band in encrypted memory structures.

---

## 11. Secrets Threat Model

| Secret Asset | Storage Location | In-Memory Lifetime | Protection Mechanism | Exposure Risk in GUI | Mitigation Strategy |
|---|---|---|---|---|---|
| **SaaS Administrator Password** | Never stored on disk | Transient (seconds during claim call) | Explicit byte zeroing (`for i := range b { b[i] = 0 }`) | Accidental logging in crash dumps or state files | Wiped immediately; excluded from `installer_state.json`. |
| **SaaS Claim Code (OTP)** | Transient memory only | Max 15 minutes or until claim | Discarded immediately after HTTP 200 response | Displayed in input field | Masked input field; cleared from component state upon submission. |
| **Edge Device JWT / Token** | `credentials.json` | Daemon process lifetime | File permissions `0600` owned by service user | Exposed in status APIs | Omitted from loopback API responses; UI only receives `is_authenticated: true`. |
| **Camera Master Encryption Key** | `camera_master.key` | Loaded on startup | 32-byte cryptographically random key; file mode `0400` / `0600` | Read by unauthorized local processes | Placed in OS protected credential vault or restricted service directory. |
| **Camera RTSP Passwords** | `camera_credentials.json` | Transient during stream connect | AES-256-GCM ciphertext + 12-byte random nonce | Printed in RTSP test logs | Redacted in all error strings (`rtsp://***:***@...`). Never returned over Wails IPC. |
| **Local Daemon Loopback Token** | `loopback_auth.token` | Service lifetime | Ephemeral 32-byte secret generated per service start, mode `0600` | Local port scanning by untrusted apps | Required in `Authorization: Bearer <token>` header for all `127.0.0.1:8091` calls. |

---

## 12. OS Privilege Model

The desktop installer application must adhere to the principle of least privilege:

```text
+---------------------------------------------------------------------------------+
| Standard User Context (Desktop GUI)                                             |
| - Runs under logged-in desktop user.                                            |
| - Preflight checks, ONVIF discovery, RTSP stream testing.                        |
| - Interacts with user for configuration choices.                                |
| - Writes user-scoped draft state.                                               |
+---------------------------------------------------------------------------------+
                                      |
                                      | Elevation Request (UAC / pkexec / authopen)
                                      v
+---------------------------------------------------------------------------------+
| Elevated Administrator Context (Service Helper / Installer Engine)             |
| - Windows: Kardianos Service install via UAC prompt.                            |
| - Linux: systemd unit write to /etc/systemd/system/geocam-edge.service via pkexec.|
| - macOS: LaunchDaemon install to /Library/LaunchDaemons via Authorization API.   |
| - Creates /var/lib/geocam or C:\ProgramData\GeoCam with strict ACLs (0700).     |
| - Starts background daemon service under dedicated non-interactive service user.|
+---------------------------------------------------------------------------------+
```

- **Windows:** The main GUI executable does not require `requireAdministrator` manifest execution. Only the "Install Service" button triggers a UAC elevation prompt via a small helper command (`geocam-edge service install`).
- **Linux:** If running under a standard desktop session, service registration uses `pkexec` or displays the exact `sudo` command required.
- **macOS:** Service creation uses macOS `AuthorizationExecuteWithPrivileges` or prompts the user via AppleScript standard dialogs for administrator privileges.

---

## 13. DVR/NVR Boundary (`SEPARATE_MILESTONE` / `NOT_VALIDATED`)

Multi-channel DVR/NVR support is deliberately excluded from the Installer v1 release.

### Technical Deficiencies Blocking DVR/NVR
1. **Target Identity Collapse:** Current Edge architecture assumes 1 Target ID = 1 IP / 1 Stream. When connecting to an 8-channel DVR at a single IP address (e.g. `192.168.1.200`), each channel requires distinct RTSP subpaths (`/ch01/0`, `/ch02/0`), independent ONVIF token handles, and separate supervisor pipelines.
2. **Resource Exhaustion:** Launching 8 or 16 ffmpeg decoders simultaneously on modest hardware causes unmetered CPU/RAM exhaustion without an admission controller.
3. **SaaS Entity Disconnect:** The SaaS data model currently reconciles Edge devices and cameras. It does not possess a first-class "NVR Channel" entity model.

### UI Requirement for v1
The DVR/NVR card or radio button in the GUI must be rendered with an inactive/disabled state and labeled:
> `DVR/NVR Multi-Channel: In Development (Separate Milestone).`

---

## 14. Prerequisites for Milestone UX-1

Before writing Wails v2 frontend code in milestone UX-1, the following foundational Go packages and mocks must be in place:

1. **`internal/installer` Package:** Create the package containing the `Facade` interface and DTO structs specified in Section 4.
2. **`ConfigService` Implementation:** Implement `Validate`, `Plan`, and `Apply` in `internal/config` or `internal/installer/config`.
3. **`LoopbackAuth` Middleware:** Add local bearer token generation and validation to `internal/health` server.
4. **Wails CLI Tooling:** Ensure `wails` CLI v2.8+ is installed on the developer workstation and CI build runners.

---

## 15. Prioritized Blockers

| Priority | Blocker ID | Description | Impact | Resolution Path |
|---|---|---|---|---|
| **P0** | `BLK-ENROLL-SaaS` — **RESOLVED** | SaaS lacks single-use short-lived claim token API (`POST /api/v1/edge/claim`). | Installer cannot meet security requirement 4.3 without storing admin password. | Implemented on `monitoreoia` `feature/ux2-claim-closure`: `POST /api/v1/edge/claim` + admin `enrollment-codes` API, functionally validated end-to-end. Merge to `main` pending. |
| **P0** | `BLK-LOCK-COLLISION` | `internal/instance.Acquire` blocks GUI from inspecting local status if running in same process. | GUI crashes or hangs if attempting to start pipelines while daemon is active. | Mandate Hybrid Architecture (Section 5): GUI connects via loopback HTTP once service is started. |
| **P1** | `BLK-ELEVATION-FLOW` | Cross-platform service installation lacks standard unprivileged-to-privileged escalation bridge. | Service registration fails silently on Linux/macOS when run as normal user. | Implement helper subcommand (`geocam-edge service install --elevated`) triggered via OS dialog. |
| **P2** | `BLK-FULL-EDGE-BUNDLE`| Full Edge mode requires external Python worker and ONNX models on local disk. | Full Edge fails to commission on machines without Python/models pre-installed. | Include pre-packaged standalone Python worker binary or flag Full Edge as requiring manual runtime setup. |

---

## 16. Recommended Implementation Order

To ensure rapid, defect-free execution without building unbacked UI screens, implementation must follow this strict sequence:

```text
+-----------------------------------------------------------------------+
| Step 1: UX-0 (THIS AUDIT)                                             |
| Complete contract audit, data models, and architectural boundaries.   |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 2: UX-1 (Wails Shell & Status Call)                              |
| - Initialize Wails v2 shell + React/Vite.                             |
| - Implement internal/installer Facade stub.                           |
| - GUI successfully reads real system status and Edge version.         |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 3: UX-2 (Enrollment Flow)                                        |
| - Implement short-lived claim code UI.                                |
| - Wire credentials.json persistence & identity validation.            |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 4: UX-3 (Mode & Profile Selection)                               |
| - Implement Cloud vs Hybrid vs Full Edge UI cards.                    |
| - Hardware preflight checks (RAM, GPU, disk).                         |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 5: UX-4 (Camera IP Onboarding)                                   |
| - WS-Discovery multicast scan & manual IP entry.                      |
| - ONVIF auth test & RTSP stream probe (ffprobe).                      |
| - AES-GCM credential encryption in camera_credentials.json.           |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 6: UX-5 (Commissioning & Diagnostics)                            |
| - Atomic ConfigService.Apply().                                       |
| - Elevated OS service registration & start.                           |
| - Real-time automated verification checks (Health, SaaS, RTSP).       |
| - Post-commissioning dashboard view.                                 |
+-----------------------------------------------------------------------+
                                   |
                                   v
+-----------------------------------------------------------------------+
| Step 7: UX-7 & UX-8 (Packaging, CI & Field Validation)               |
| - GitHub Actions multi-arch release pipeline (Win/Linux/macOS).       |
| - End-to-end field testing on physical hardware with IP cameras.      |
+-----------------------------------------------------------------------+
```

---

## Final UX Closure Update

`UX0_STATUS = COMPLETE`

This audit's P0 architectural blocker (`BLK-ENROLL-SaaS`) is resolved: the
claim contract this document specified (`POST /api/v1/edge/claim`) is
implemented on both sides and validated end-to-end (see
`docs/product/UX5_PHYSICAL_COMMISSIONING.md` and, in `monitoreoia`,
`docs/saas/21-edge-self-service-claim.md`). Everything else this document
predicted about the in-process facade / hybrid architecture / instance-lock
handling was confirmed correct by UX1–UX5's actual implementation — no
architectural rework was needed.
