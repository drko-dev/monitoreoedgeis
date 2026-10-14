# H7 — Extended Offline Security Certification and Network Isolation Report

**Status:** IMPLEMENTED / VALIDATED LOCAL (Synthetic, Process-Level Sandbox, and Hermetic Go Pipeline)  
**Branch:** `test/full-edge-h7-extended-isolation`  
**Hard Gates:** `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`  
**Scope:** GEO CAM Edge network isolation, adversarial escape prevention, process-level containment, Unix socket confinement, DNS leak prevention, proxy environment sanitization, and synthetic RTSP video pipeline validation under strict offline isolation.

---

## 1. Technical Differentiation of Isolation Tiers

Security guarantees must be characterized by their precise enforcement boundary. Conflating process containment with host isolation or physical airgap creates false assurance.

| Isolation Tier | Enforcement Boundary | Primary Mechanisms | Protection Provided | Residual Threats & Limitations |
| :--- | :--- | :--- | :--- | :--- |
| **Tier 1: Process-Level Isolation** *(This Milestone)* | Spawned child process tree (`Worker`, Python, FFmpeg, Go subprocesses) | Hermetic environment filtering, `sandbox-exec` (macOS), `HermeticDialContext`, `ValidateHermeticAddress`, IPC Unix socket confinement | Prevents spawned binaries from inheriting host tokens, reading host paths, contacting external IPs, or using parent proxy variables. | Does **not** prevent connections if an unauthenticated proxy or relay is already running on `127.0.0.1`. Does not affect other host processes. Dependent on OS kernel support (e.g. deprecated `sandbox-exec` on macOS). |
| **Tier 2: Host-Level Network Isolation** | Appliance OS networking stack (all interfaces & host processes) | Packet filter / firewall rules (PF on macOS, nftables/iptables on Linux), private network namespaces, routing tables with no default gateway (`0.0.0.0/0`), interface down (`ip link set eth0 down`). | Blocks all egress packets from all processes on the appliance. Drops packets destined for WAN before leaving interface. | Does not protect if local camera subnet (LAN) contains compromised devices acting as rogue gateways or DNS poisoners unless L2/L3 switch isolation is configured. |
| **Tier 3: Complete Physical Airgap** | Physical hardware appliance and physical medium | Physical disconnection of WAN Ethernet cable, absence or physical disabling of Wi-Fi / Bluetooth / LTE/5G hardware, point-to-point isolated camera switch without router/uplink. | Structural zero-IP transmission to external networks; immune to software-layer firewall misconfigurations or zero-day sandbox escapes. | Physical access attacks, rogue USB devices, maintenance/update supply chain contamination. |

---

## 2. Preexisting H7B Harness Audit

An audit of the initial H7B harness (`internal/vision/hermetic.go`, `deploy/installer/h7b-egress-cert.py`, `mac-h7b-loopback-egress.sb`) revealed solid foundational concepts alongside significant false-assurance windows:

1. **Absence of Go-Level Network Dialing Confinement:**
   H7B relied exclusively on the external `/usr/bin/sandbox-exec` tool on Darwin. Within Go code (`internal/vision`), standard network dials were not validated or intercepted, leaving Linux CI and non-sandboxed executions exposed if a component attempted outbound connections.
2. **Incomplete Proxy Variable Coverage:**
   The H7B environment allowlist dropped `HTTP_PROXY` and `HTTPS_PROXY`, but did not explicitly detect or reject `ALL_PROXY`, `FTP_PROXY`, `NO_PROXY`, lowercase variants (`http_proxy`, `https_proxy`, `all_proxy`, `no_proxy`), or certificate redirection variables (`CURL_CA_BUNDLE`, `SSL_CERT_FILE`).
3. **DNS Exfiltration / Resolution Blindspot:**
   Hostnames provided in camera URLs or dependencies would trigger standard `getaddrinfo` resolution via UDP port 53. If a system resolver pointed to external DNS servers (e.g. `8.8.8.8`), queries leaked outside the appliance even if TCP data payload connections were subsequently blocked.
4. **Unix Domain Socket IPC Escape Vector:**
   `Worker.validateHermeticConfig()` thoroughly validated `RuntimePackRoot`, `WorkerCmd`, `WorkerArgs`, and environment directories, but **completely omitted validation of `Config.SocketPath`**. An adversarial or compromised configuration could set `SocketPath` to an arbitrary path (e.g., `/var/run/docker.sock`, `/tmp/tunnel.sock`, or a symlink escaping the isolated state directory), allowing privilege escalation or relay tunneling.
5. **Lack of Integrated Synthetic Pipeline Proof:**
   The harness tested process spawning and basic socket pinging, but did not execute an integrated video pipeline (synthetic loopback RTSP -> decoder -> worker sink) under verified isolation constraints.

---

## 3. Implemented Security Hardening & Defenses

### 3.1. Go Runtime Hermetic Address & Dialer Enforcement
Implemented in `internal/vision/hermetic.go`:
- `ValidateHermeticAddress(addr string) error`: Validates any network target or URI. Rejects any non-loopback IP with `ErrHermeticNetworkEscape`. Rejects any non-localhost hostname (which would trigger external DNS resolution) with `ErrHermeticDNSForbidden`. Permitted targets are strictly `127.0.0.1`, `localhost`, `::1`, and loopback RTSP URIs.
- `HermeticDialContext(ctx, network, addr) (net.Conn, error)`: Intercepts network dials at the Go runtime layer. Enforces that TCP dials pass `ValidateHermeticAddress` and Unix dials pass socket path validation before opening a kernel socket.
- `HermeticResolver() *net.Resolver`: Pure Go resolver configured to refuse queries to non-loopback DNS servers, failing closed on domain lookup attempts.

### 3.2. Strict Proxy Detection and Sanitization
- `DetectForbiddenProxyEnv(entries []string) []string`: Inspects environment variables for `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `FTP_PROXY`, `NO_PROXY`, their lowercase counterparts, `CURL_CA_BUNDLE`, and `SSL_CERT_FILE`.
- `parseHermeticEnvironment(entries []string)`: Immediately aborts with `ErrHermeticProxyForbidden` (wrapping `ErrHermeticRuntimePackInvalid`) if any proxy variable is present in the worker's replacement environment.
- `Worker.newExecCommand()`: Strictly replaces `cmd.Env` with `w.cfg.ProcessEnv`, guaranteeing that parent host proxy variables (`HTTP_PROXY`, `ALL_PROXY`, `DYLD_INSERT_LIBRARIES`, etc.) are completely omitted from child processes.
- `BuildRuntimePackWorkerLaunchSpec`: Verified in `internal/installer` to generate clean launch specs immune to host proxy variables.

### 3.3. Unix Domain Socket IPC Confinement
Implemented in `validateHermeticSocketPath(socketPath, stateRoot string) error`:
- Absolute path enforcement (`filepath.IsAbs`).
- Symlink rejection (`ModeSymlink` inspection via `os.Lstat`).
- Containment within the worker's private state root (`HOME`). Traversal using `..` or symlinks escaping `stateRoot` is rejected with `ErrHermeticSocketEscape`.
- Insecure permissions check: Directories with world-writable bits (`Perm()&0o002 != 0`) are rejected.
- Directory auto-provisioning via `ensureSocketDir` creates parent directories with mode `0700` (owner only).

### 3.4. Video Pipeline Validation under Strict Hermetic Isolation
- Verified that synthetic RTSP servers (`rtsptest.Simulator`) running on `127.0.0.1` successfully feed H.264 video frames through `processing.Manager` and `vision.Sink` to the worker process.
- Verified that registering an external RTSP target (`rtsp://203.0.113.1:554/live` or `rtsp://camera.corp.net/live`) is blocked fail-closed before any network packets are transmitted.

---

## 4. Adversarial Testing Matrix

All tests execute with `-race` enabled and pass deterministically without touching external networks, host firewall rules, or physical cameras.

| Test ID | Adversarial Vector / Scenario | Package & Function | Expected Behavior | Observed Result | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **ADV-01** | Child process attempts outbound TCP to RFC 5737 (`203.0.113.1:80`) | `internal/vision.TestHermeticChildProcessRejectsExternalEgress` | Hermetic dialer intercepts dial and returns `ErrHermeticNetworkEscape` | Exit code 0 (dial rejected, no outbound traffic) | **PASS** |
| **ADV-02** | Child process attempts outbound UDP to RFC 5737 (`198.51.100.1:9`) | `internal/vision.TestHermeticChildProcessRejectsExternalEgress` | Blocked before packet transmission | Exit code 0 (egress failed closed) | **PASS** |
| **ADV-03** | Child process attempts IPv6 egress to RFC 3849 (`[2001:db8::1]:9`) | `internal/vision.TestHermeticChildProcessRejectsExternalEgress` | Hermetic dialer rejects non-loopback IPv6 | Exit code 0 (egress denied) | **PASS** |
| **ADV-04** | Grandchild process spawned to bypass process confinement | `internal/vision.TestHermeticChildProcessRejectsExternalEgress` | Grandchild inherits hermetic policy and fails closed | Exit code 0 (grandchild blocked) | **PASS** |
| **ADV-05** | macOS `sandbox-exec` kernel enforcement against raw sockets | `internal/vision.TestHermeticChildProcessRejectsExternalEgress` | Kernel returns `EPERM` (errno 1) for outbound IP; loopback succeeds | `EPERM=1` observed for external TCP/IPv6; loopback allowed | **PASS** |
| **ADV-06** | Injection of uppercase proxy variables (`HTTP_PROXY`, `ALL_PROXY`, etc.) | `internal/vision.TestHermeticWorkerRejectsProxyEnvironmentVariables` | `DetectForbiddenProxyEnv` catches keys; `validateHermeticConfig` returns `ErrHermeticProxyForbidden` | Rejected with `ErrHermeticProxyForbidden` | **PASS** |
| **ADV-07** | Injection of lowercase proxy variables (`http_proxy`, `all_proxy`, etc.) | `internal/vision.TestHermeticWorkerRejectsProxyEnvironmentVariables` | Case-insensitive match triggers rejection | Rejected with `ErrHermeticProxyForbidden` | **PASS** |
| **ADV-08** | Parent process environment contains proxy and DYLD injection tokens | `internal/vision.TestHermeticWorkerRejectsProxyEnvironmentVariables` | Replacement `cmd.Env` does not inherit parent host variables | Zero leakage into child `cmd.Env` | **PASS** |
| **ADV-09** | External hostname lookup (`pool.ntp.org`, `saas.geocam.io`, `exfil.invalid`) | `internal/vision.TestHermeticDNSLeakPrevention` | `ValidateHermeticAddress` rejects hostnames requiring external DNS | Rejected with `ErrHermeticDNSForbidden` | **PASS** |
| **ADV-10** | External public DNS server IP targets (`8.8.8.8:53`, `1.1.1.1:53`, `9.9.9.9:53`) | `internal/vision.TestHermeticDNSLeakPrevention` | Non-loopback IP validation fails before DNS query is dispatched | Rejected with `ErrHermeticNetworkEscape` | **PASS** |
| **ADV-11** | Hermetic DNS resolver queries arbitrary test domain | `internal/vision.TestHermeticDNSLeakPrevention` | Resolver dialer blocks query to external nameserver | Fails closed without query dispatch | **PASS** |
| **ADV-12** | Socket path outside state directory (`/tmp/evasion.sock`) | `internal/vision.TestHermeticUnixSocketSecurity` | Confinement check flags socket escaping `HOME` | Rejected with `ErrHermeticSocketEscape` | **PASS** |
| **ADV-13** | Socket path traversal via `..` (`$HOME/../escape.sock`) | `internal/vision.TestHermeticUnixSocketSecurity` | Directory traversal detected | Rejected with `ErrHermeticSocketEscape` | **PASS** |
| **ADV-14** | Socket path is a symlink pointing to external target | `internal/vision.TestHermeticUnixSocketSecurity` | `os.Lstat` detects `ModeSymlink` and aborts | Rejected with `ErrHermeticSocketEscape` | **PASS** |
| **ADV-15** | Socket directory has world-writable permissions (`0777`) | `internal/vision.TestHermeticUnixSocketSecurity` | World-writable bit check triggers failure | Rejected with `ErrHermeticSocketEscape` | **PASS** |
| **ADV-16** | Valid socket inside `$HOME/run/vision-worker.sock` | `internal/vision.TestHermeticUnixSocketSecurity` | Valid containment and permissions accepted | Validation passes cleanly | **PASS** |
| **ADV-17** | End-to-end synthetic RTSP video pipeline on `127.0.0.1` | `internal/vision.TestHermeticVideoPipelineWithSyntheticRTSP` | Loopback stream decodes and yields inference detection | Detection received (`ClassIDPerson`, conf > 0.9) | **PASS** |
| **ADV-18** | End-to-end video pipeline with non-loopback RTSP target | `internal/vision.TestHermeticVideoPipelineWithSyntheticRTSP` | External RTSP URI rejected by hermetic validation | Rejected with `ErrHermeticNetworkEscape` | **PASS** |
| **ADV-19** | Installer runtime launch spec under host proxy contamination | `internal/installer.TestBuildRuntimePackWorkerLaunchSpecAdversarialIsolation` | Launch contract environment excludes all proxy and dynamic linker variables | All forbidden variables filtered | **PASS** |
| **ADV-20** | Python sandbox policy unit tests | `deploy/installer/test-h7b-egress-cert.py` | 9 policy integrity and environment exclusion tests | 9 tests passed in 0.005s | **PASS** |

---

## 5. Verification Commands and Local Results

The following test suites were executed on the worktree:

```bash
# 1. Format and static analysis
gofmt -s -w internal/vision internal/installer
go vet ./internal/vision/... ./internal/installer/... ./internal/offlinecert/...
# Result: 0 errors, 0 warnings

# 2. Race-detector execution on vision package (including all adversarial tests)
go test -race ./internal/vision/...
# Result: ok  github.com/drko-dev/monitoreoedgeis/internal/vision  12.060s

# 3. Race-detector execution on installer and offlinecert packages
go test -race ./internal/installer/... ./internal/offlinecert/...
# Result: ok  github.com/drko-dev/monitoreoedgeis/internal/installer   75.253s
#         ok  github.com/drko-dev/monitoreoedgeis/internal/offlinecert  2.274s

# 4. Python sandbox policy unit tests
python3 deploy/installer/test-h7b-egress-cert.py
# Result: Ran 9 tests in 0.005s — OK
```

---

## 6. Remaining Limitations and Hard NO-GO Gates

While the process-level defenses and adversarial tests are verified and passing locally:

1. **Localhost Relay Risk:** Process-level isolation permits `127.0.0.1` for local RTSP video simulation. If an unauthorized local proxy or tunnel is listening on loopback, a malicious payload inside the worker could attempt loopback evasion. Full airgap certification requires Tier 2 (host firewall / namespace isolation) and Tier 3 (physical airgap).
2. **Platform Sandbox Deprecation:** macOS `/usr/bin/sandbox-exec` is deprecated by Apple. Long-term production enforcement on macOS requires Endpoint Security framework or App Sandbox entitlements.
3. **Release Packaging & Model Rights:** No production model distribution has been authorized.
   - `RUNTIME_READY=false`
   - `INFERENCE_READY=false`
   - `MODEL_REDISTRIBUTION_APPROVED=false`
4. **H8 Clean-Machine Verification:** Integrated packaging must still be validated on a clean macOS / Linux machine without developer toolchains or Homebrew installed.
