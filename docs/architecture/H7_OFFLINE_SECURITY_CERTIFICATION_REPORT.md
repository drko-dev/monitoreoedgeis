# H7 — Extended offline controls: scope, negative tests, and RTSP dialer integration

**Status: H7 TIER-1 LAB VALIDATED / FULL APPLICATION & AIRGAP NOT CERTIFIED.**
Issue #156 approved for review publication, PR separate from H7B #163 (already merged). Readiness flags stay `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`.

## Enforcement boundaries

| Boundary | Current evidence | Limit |
| --- | --- | --- |
| **Tier 1 — H7B sandboxed subprocess tree** | Kernel sandbox-exec denies tested IPv4/IPv6 outbound TCP/UDP with `EPERM`, allows localhost and child/grandchild inheritance, synthetic RTSP loopback E2E | Does not prevent local loopback proxy/relay, untested transports, unsandboxed helpers, host-wide traffic |
| **Tier 1 — Go hermetic functions** | `ValidateHermeticAddress`, `HermeticDialContext`, strict proxy env parsing, Unix Worker socket path checks and synthetic adversarial tests with `-race` | Normal application network paths are not automatically bound to this dialer |
| **Tier 1 — Opt-in real RTSP transport** | `rtsp.DialWithDialContext` and `rtsp.Config.DialContext` allow laboratory explicit injection of `vision.HermeticDialContext`. Real RTSP DESCRIBE/SETUP/PLAY loopback handshake passes; external documentation IP rejected before dial | Production `rtsp.Dial` defaults to normal networking to retain **legitimate LAN camera support**. An app explicitly using offline-only mode must inject this dialer in its own camera manager configuration |
| **Tier 2 — Host network** | **NOT CERTIFIED** | No PF/nftables changes or host-wide traffic capture; we did not alter host firewall |
| **Tier 3 — Physical airgap** | **NOT CERTIFIED** | Requires real isolated test host/physical inspection |

**Important:** An air-gapped camera appliance normally needs access to cameras on a designated LAN subnet. The H7B loopback-only sandbox is appropriate for local laboratory RTSP fixtures but is **not a general-purpose production network policy**. Do not apply it to normal camera connections or assume all application sockets use an opted-in dialer.

## Code & tests

- New `internal/vision/hermetic.go` utility validates network destinations before dials, rejects external IPs and hostnames needing external DNS, filters proxies, sanitizes worker environment and validates Worker Unix socket confinement.
- `internal/rtsp/client.go` adds an optional dial hook; `internal/rtsp/types.go` adds `DialContext` to the manager configuration; `internal/rtsp/supervisor.go` uses it for the live handshake **only when provided**, otherwise retains the existing `net.Dialer` for LAN cameras.
- `TestH7HermeticDialIsConnectedToRealRTSPTransport` runs a real loopback RTSP simulator handshake using that hook and confirms `203.0.113.1:554` is rejected; this eliminates the earlier gap where the constrained dialer was only unit-tested independently.
- Mac sandbox test now **fails** if Python TCP/IPv6 probe does not return kernel-denied `EPERM`; previously it only logged unexpected results. A successful outbound UDP write is treated as **escape**, not as a successful denial. Standalone H7B boundary continues testing additional TCP/UDP/IPv4/IPv6 cases.
- Existing synthetic vision worker tests verify rejection of proxy injection, external DNS requests and insecure/out-of-root Worker socket paths. The synthetic pipeline uses test worker/fake detections, **not** real YOLO weights or a full installed Wails package.
- Local targeted `go test -race -count=1 ./internal/vision ./internal/rtsp` and H7B Python tests pass. No real cameras or host network configuration were touched.

## Hard NO-GO for Full Edge release

1. Explicitly select camera LAN allowlist/routing policy for product use, not a blanket localhost restriction; integrate the policy into the full application's camera manager and supervise all external helper processes.
2. Validate loopback proxy relay bypass, Unix domain sockets, DNS, XPC services and any Wails child process on the **installed** app and target macOS; H7B `sandbox-exec` remains deprecated.
3. Test Tier 2 host isolation with an authorized isolated rig; certify Tier 3 only by independent physical evidence where required.
4. Obtain signed externally trusted release pack and legal model redistribution authorization; physical H8 macOS14 compatibility remains blocked.

**No merge, deploy, release, firewall changes or model redistribution. H7 is process/laboratory safety evidence only.**
