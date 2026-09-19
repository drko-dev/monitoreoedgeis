# GEO CAM Edge — Corporate Networking & Infrastructure Guide (R4–R6)

This document specifies the network architecture, traffic flows, and operational
constraints for deploying `geocam-edge` within corporate environments. It covers
site-to-site VPNs (**R4**), routed subnets (**R5**), and VLAN segmentation (**R6**).

Everything documented here reflects the **actual behavior of the codebase**
(audited in `internal/discovery`, `internal/rtsp`, `internal/transport`, and
`cmd/geocam-edge`). Where limitations exist (notably cross-subnet multicast
discovery), they are called out plainly rather than glossed over.

---

## 1. Architectural Principles

1. **Edge is an IP Consumer, Not a VPN Appliance**:
   - `geocam-edge` is an application daemon, **not** a VPN client, router, or
     firewall manager.
   - It **does not integrate** VPN SDKs (e.g. WireGuard in-process, OpenVPN client
     libraries, or IPsec stacks).
   - It **does not manage** VPN credentials, PSKs, X.509 client certificates for
     tunnels, or peer lists.
   - All network encapsulation, tunnels, and routing **must be terminated by the
     customer's infrastructure** (edge firewall, corporate router, gateway, or
     host-level OS network stack).

2. **Private IP Enforcement on Discovered ONVIF XAddrs**:
   - Discovered ONVIF XAddr destinations are restricted to allowed private IPv4
     ranges (RFC 1918: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`;
     RFC 3927: `169.254.0.0/16`) by `ValidateXAddr` (`internal/discovery/security.go`).
   - Public ONVIF XAddr discovered targets, loopback, and cloud metadata endpoints
     (`169.254.169.254`) are rejected fail-closed to eliminate SSRF vectors.
   - Note: This enforcement applies specifically to discovered ONVIF XAddr URLs
     via `ValidateXAddr`; it is not a global packet-filter policy across arbitrary
     camera targets. In corporate architectures, cameras must reside on private
     subnets and must not be exposed with public IP addresses.

3. **Standard Outbound Egress**:
   - The Edge connects **outbound-only** to the SaaS control plane over standard
     HTTPS (TCP 443). No inbound ports are required on WAN firewalls.

---

## 2. R4 — Site-to-Site VPN & Gateway Architecture

When the Edge host and CCTV cameras reside in physically separate locations (e.g.
Edge in a central data center or branch office, cameras at another branch), traffic
is routed over an encrypted tunnel terminated at customer gateways:

```
┌────────────────────────────────────────────────────────────────────────┐
│                              SaaS Cloud                                │
│                       (https://saas.geocam.io)                         │
└───────────────────────────────────▲────────────────────────────────────┘
                                    │ HTTPS (TCP 443 Outbound)
                                    │ TLS (Standard Verification)
     ═══════════════════════════════╪════════════════════════════════════
       Customer Corporate Network   │
     ═══════════════════════════════╪════════════════════════════════════
                                    │
                       ┌────────────┴────────────┐
                       │ Corporate Edge Firewall │
                       │    / Gateway / NAT      │
                       └──────▲────────────▲─────┘
                              │            │
            Site-to-Site VPN  │            │ Internal LAN / Routing
         (IPsec / WireGuard / │            │
           Tailscale router)  │            │
                              │            │
            ┌─────────────────┴─┐       ┌──┴──────────────────────┐
            │ Remote Branch     │       │ Local Management /      │
            │ CCTV Subnet       │       │ Server LAN              │
            │ (e.g. 10.200.0/24)│       │ (e.g. 10.100.0/24)      │
            │                   │       │                         │
            │   [CCTV Cam 01]   │       │   ┌───────────────────┐ │
            │   [CCTV Cam 02]   │       │   │  GEO CAM Edge     │ │
            │   [NVR / DVR]     │       │   │  (Linux Host/VM)  │ │
            └───────────────────┘       │   └───────────────────┘ │
                                        └─────────────────────────┘
```

### Supported VPN Topologies

| Topology | Description | Termination Point | GEO CAM Impact |
|---|---|---|---|
| **Site-to-Site Hardware VPN** | IPsec or OpenVPN tunnel between hardware firewalls/routers (pfSense, Fortinet, Cisco, MikroTik). | Network Routers / Firewalls | **Zero**. Transparent IP routing via kernel default/static routes. |
| **Subnet Router (e.g. Tailscale / ZeroTier)** | A dedicated Linux node or gateway advertises CCTV subnets into an overlay mesh. | Subnet Router Gateway | **Zero**. Traffic to CCTV subnets is routed via the subnet router IP. |
| **Host-Level VPN Client** | Linux host runs `wg-quick`, StrongSwan, or OpenVPN as a systemd service. | Host OS Network Stack | **Transparent**. Agent uses the resulting interface (`wg0`, `tun0`) based on kernel routing table. |

---

## 3. R5 — Subnet Routing: Unicast RTSP vs. Multicast Discovery

Corporate CCTV deployments commonly place IP cameras on a dedicated, isolated subnet
(e.g., `10.100.2.0/24`) separate from server/management subnets (e.g., `10.100.1.0/24`):

```
┌────────────────────────────────────────────────────────────────────────┐
│                   Corporate Core Router / L3 Switch                    │
│                      (Default Gateway / Routing)                       │
└──────────────▲──────────────────────────────────────────▲──────────────┘
               │                                          │
      Subnet A │ (10.100.1.0/24)                 Subnet B │ (10.100.2.0/24)
               │                                          │
    ┌──────────┴──────────┐                    ┌──────────┴──────────┐
    │ GEO CAM Edge Host   │                    │ CCTV Camera Subnet  │
    │ IP: 10.100.1.50     │                    │ (Isolated L2)       │
    │ GW: 10.100.1.1      │                    │ [10.100.2.10:554]   │
    └─────────────────────┘                    │ [10.100.2.11:554]   │
                                               └─────────────────────┘
```

### Code Audit: What Works vs. What Does Not

```
┌────────────────────────────────────────────────────────────────────────┐
│ ROUTED RTSP TRANSPORT CAPABILITY:                   SUPPORTED          │
│ CROSS-SUBNET TARGET PROVISIONING / AUTO-DISCOVERY:  CURRENT GAP /      │
│                                                     NOT IMPLEMENTED    │
└────────────────────────────────────────────────────────────────────────┘
```

1. **RTSP TCP Transport Capability (SUPPORTED)**:
   - **RTSP Streaming**: Handled by `internal/rtsp.DialContext(ctx, "tcp", addr)`.
     Standard TCP unicast connects seamlessly across any IP-routed hop (inter-VLAN
     routing, L3 switch, or site-to-site VPN) whenever an IP:port destination is
     configured.
   - **ONVIF Metadata**: Handled by `internal/discovery/onvif` via standard HTTP/SOAP
     unicast POST requests to the camera's IP and port.

2. **Cross-Subnet Provisioning and Auto-Discovery Limitation (CURRENT GAP / NOT IMPLEMENTED)**:
   - **Automatic WS-Discovery**: Relies on UDP multicast to `239.255.255.250:3702`
     sent over local network interfaces. Standard IP routers and firewalls do not forward
     multicast across subnet boundaries without dedicated multicast routing or relay
     proxies. GEO CAM does not implement multicast routing or relay; automatic discovery
     is structurally confined to the local L2 broadcast domain.
   - **Remote Config Boundary**: In the current architecture (`internal/remoteconfig`),
     Remote Config only tunes processing parameters (`target_fps`, `output_width`,
     `output_height`, `hybrid_rois`) for known cameras (`candidate_key`). Furthermore,
     `DisallowedKeys` explicitly forbids injecting network target identifiers (`rtsp_url`,
     `url`, `path`, etc.).
   - **Gap statement**: The control plane cannot dynamically provision or announce
     a previously unknown camera residing in an unattached remote subnet via Remote Config.
     Cross-subnet target provisioning is not implemented in this milestone.
   - **Alternative**: To access cameras in other VLANs/subnets, operators can assign
     the Edge host an interface in that L2 broadcast domain (see VLAN Topology B below).

---

## 4. R6 — VLANs: Access Ports vs. 802.1Q Trunks

`geocam-edge` **never configures VLANs**. It does not create 802.1Q tags, does not
invoke `ip link add type vlan`, and does not manipulate NetworkManager or netplan.
All VLAN interfaces are provisioned and managed by customer network administrators.

### Topology A: Access Port (Routed VLAN)

```
[GEO CAM Edge Host] ── (Untagged eth0) ──► [Switch Access Port (VLAN 10)]
                                                    │
                                                    ▼
                                           [L3 Core Routing]
                                                    │
                                                    ▼
                                           [CCTV Subnet (VLAN 20)]
```
- **Host Configuration**: Single physical interface (`eth0`) with one untagged IP.
- **Routing**: Inter-VLAN routing is handled by the upstream corporate switch/router.
- **Behavior**: Unicast RTSP works across the router. WS-Discovery scans VLAN 10 only.

### Topology B: 802.1Q Trunk with Host Subinterfaces

```
[GEO CAM Edge Host]
  ├── eth0.10 (Management / SaaS egress)  -> IP: 10.10.0.50/24 (Gateway: 10.10.0.1)
  └── eth0.20 (CCTV Direct Access)        -> IP: 10.20.0.50/24 (No Default Gateway)
       │
 (802.1Q Trunk)
       │
       ▼
[Managed Switch (Trunk Port: VLANs 10, 20)]
  ├── VLAN 10: Corporate Network (Internet access for SaaS)
  └── VLAN 20: Isolated CCTV Network (IP cameras / NVRs)
```

- **Host Configuration**: Managed by customer sysadmin via OS network configuration
  (e.g., `/etc/netplan/*.yaml` or `/etc/network/interfaces`):
  ```sh
  # Example Linux manual creation (persisted via OS network tools, not GEO CAM):
  sudo ip link add link eth0 name eth0.20 type vlan id 20
  sudo ip addr add 10.20.0.50/24 dev eth0.20
  sudo ip link set dev eth0.20 up
  ```
- **Discovery Behavior**:
  - `internal/discovery/netinterfaces.go` ignores virtual tunnel interfaces (`tun`,
    `tap`, `wg`, `tailscale`), but **does not ignore standard Linux VLAN subinterfaces**
    (`eth0.20`, `enp3s0.100`, etc.).
  - Because `eth0.20` has `FlagUp`, `FlagMulticast`, and a private IPv4 address,
    the discovery scanner will include it in auto-private scans.
  - To restrict discovery exclusively to the CCTV VLAN and avoid scanning other
    interfaces, configure `/etc/geocam-edge/geocam-edge.env`:
    ```ini
    GEOCAM_DISCOVERY_INTERFACES=eth0.20
    ```

---

## 5. Port and Protocol Matrix

This table summarizes the network ports and protocols used by the edge daemon in
standard deployments. Destination ports for SaaS and camera streams reflect standard
defaults and may vary based on configuration.

| Purpose | Protocol | Port / Address | Direction | Destination | Encryption | Code Reference |
|---|---|---|---|---|---|---|
| **SaaS Control & Telemetry** | HTTPS (TCP) | 443 (or custom SaaS URL port) | Outbound | SaaS Cloud Base URL | HTTPS / TLS (Standard verification, no InsecureSkipVerify) | `internal/transport/client.go` |
| **RTSP Camera Video** | RTSP / TCP | 554 (or camera RTSP port) | Outbound | IP Cameras / NVRs | Plaintext / Unencrypted | `internal/rtsp/client.go` |
| **ONVIF Device Metadata** | HTTP / SOAP | Port provided by validated XAddr (default 80 HTTP / 443 HTTPS) | Outbound | IP Cameras | HTTP or HTTPS (SOAP XML) | `internal/discovery/onvif/soap.go`, `internal/discovery/security.go` |
| **WS-Discovery Probes** | UDP Multicast | `239.255.255.250:3702` | Outbound | Local L2 Broadcast Domain | Plaintext (Multicast) | `internal/discovery/wsdiscovery/multicast.go` |
| **Local Health & Diagnostics** | HTTP (TCP) | `127.0.0.1:8091` (default) | Loopback | Localhost | Plaintext (Localhost) | `internal/health/health.go`, `internal/config/config.go` |

> [!IMPORTANT]
> **Local Health Binding**: By default, the health surface binds only to loopback
> (`127.0.0.1:8091`). Do not expose `GEOCAM_HEALTH_ADDR` externally unless explicitly
> required and protected by host/network policy.
>
> **Inbound Ports**: In default operation, GEO CAM does not listen on external network
> interfaces. No inbound port forwarding (DNAT) is required.
>
> **No In-Daemon VPN Ports**: VPN ports (e.g. UDP 51820 for WireGuard, UDP 500/4500 for IPsec)
> belong entirely to customer edge routers, firewalls, or host OS tunnels.

---

## 6. Preflight & Connectivity Diagnostic Runbook

To verify network readiness before starting the daemon in a corporate environment:

### Step 1: Verify Host Network Interfaces and VLANs
Check that the OS has the required IP addresses and VLAN interfaces active:
```sh
ip -br addr show
# Expected: eth0 (management) and optional eth0.20 (CCTV VLAN) with 'UP' status
```

### Step 2: Verify Routing to SaaS and CCTV Subnet
Confirm the kernel routing table directs traffic properly:
```sh
# Check route to SaaS:
ip route get 8.8.8.8

# Check route to a remote CCTV camera (verifies inter-VLAN / VPN routing):
ip route get 10.200.0.10
# Expected: routed via default gateway, VLAN subinterface, or VPN tunnel gateway
```

### Step 3: Test Unicast Reachability to Camera RTSP
Verify TCP port 554 is reachable through firewalls and routers:
```sh
nc -zv -w 3 10.200.0.10 554
# or: curl -v rtsp://10.200.0.10:554
```

### Step 4: Verify SaaS Control Plane Egress (Native CLI)
Verify DNS resolution, TLS handshake, and device credential authentication:
```sh
sudo geocam-edge saas check
# Expected: "OK: authenticated as edge_id=... against SaaS"
```

### Step 5: Test Discovery on Specified Interface (Native CLI)
Test ONVIF WS-Discovery on the designated CCTV interface without starting the full daemon:
```sh
sudo geocam-edge discovery scan --interface eth0.20 --timeout 5s
# or for auto-detected interfaces:
sudo geocam-edge discovery scan
```

### Step 6: Verify Local Health Surface
```sh
curl -s http://127.0.0.1:8091/readyz
# or:
geocam-edge check
```
