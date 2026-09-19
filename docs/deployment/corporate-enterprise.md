# GEO CAM Edge — Corporate/Enterprise Network (Hito R, R7–R9)

Scope: how the appliance behaves on a segmented corporate network (multiple
CCTV VLANs, a firewall/proxy between the Edge and the internet, and the
architectural boundary for a future multi-Edge HA setup). Everything below
is derived from the current code (`internal/discovery`, `internal/rtsp`,
`internal/transport`, `internal/config`) — no new infrastructure, no new
network primitives, no HA. Real gaps are stated as gaps, not implemented
around.

## R7 — Multiple CCTV segments

The Edge does two structurally different things with cameras, and they
behave differently on a segmented network:

### A. Access to a camera already known by IP/URL

`internal/rtsp.Dial` (and the ONVIF SOAP client in
`internal/discovery/onvif/soap.go`) use a plain `net.Dialer`/`http.Client`
against whatever host/URL they are given — there is no interface pinning,
no subnet check, no source-IP binding. **This already works across
segments today**, exactly as far as the host's own routing table reaches:
if the Edge's OS can route to a camera's VLAN (a router/L3 switch has a
route between the segments), unicast RTSP and ONVIF SOAP calls to that
camera's known IP work with zero code changes. This is a routing/network
configuration question, not a code limitation.

### B. Automatic discovery (WS-Discovery)

`internal/discovery/wsdiscovery` sends WS-Discovery Probes to the standard
multicast group `239.255.255.250:3702/UDP`, once per local network
interface (`internal/discovery.SelectInterfaces` already iterates every
active, non-virtual interface with a private IPv4 — so a **multi-homed**
Edge with one NIC/VLAN sub-interface per segment already probes each
segment it is directly attached to, independently).

**Gap, stated explicitly**: multicast UDP does not cross a router or an
L3-only VLAN boundary unless the network itself is configured for
multicast routing/IGMP relay (PIM-SM, an IGMP proxy, etc.) — this is
standard multicast behavior, not something this code can or should work
around. If the Edge has no interface on a given CCTV segment, it cannot
auto-discover cameras on that segment. **Do not assume WS-Discovery
crosses VLANs/routers.**

### What was deliberately NOT added

- No CIDR/subnet scanner.
- No nmap-like active probing.
- No brute-force host enumeration.
- No broadcast/multicast forwarding or relay logic in the agent itself.

These would each reintroduce exactly the kind of network behavior (active
scanning, forged multicast relay) that a corporate network/security team
has good reason to reject from an appliance. They are out of scope for
this hito and were not added.

### Alternative when discovery can't reach a segment

If a site's CCTV segments are not multicast-reachable from one Edge and
policy does not allow enabling multicast routing between them, the clean
alternative — **documented here as an option, not a universal
requirement** — is one Edge/gateway instance per segment (or per site
zone), each auto-discovering its own local segment and enrolling
independently with the SaaS. Nothing in the current architecture prevents
running multiple Edge instances against the same SaaS tenant; this is not
new code, it is the existing one-Edge-per-appliance model applied more
than once.

## R8 — Firewall matrix

Minimal, exact traffic matrix. No IP/CIDR is invented below — only what
the code actually opens/dials, with the port left as "operator-configured"
wherever the code does not hardcode one.

| Direction | Protocol | Destination | Purpose |
|---|---|---|---|
| OUTBOUND | TCP/HTTPS (443 by default; `GEOCAM_SAAS_URL` sets the actual host:port) | SaaS (`GEOCAM_SAAS_URL`) | Enrollment, heartbeat, frame/event upload (`internal/transport.Client`, shared by `internal/heartbeat` and `internal/cloudsink`) |
| OUTBOUND | TCP (port from the camera's configured RTSP URL, conventionally 554) | Each configured camera | RTSP session (`internal/rtsp.Dial`) |
| OUTBOUND | TCP/HTTP (port from the camera's ONVIF service address, device-provided) | Each ONVIF-capable camera | ONVIF SOAP device/media queries (`internal/discovery/onvif`) |
| OUTBOUND | UDP 3702 to `239.255.255.250` (multicast) | Local segment(s) the Edge has an interface on | WS-Discovery Probe (`internal/discovery/wsdiscovery`) |
| LOCAL/INBOUND | TCP, bound to `127.0.0.1:8091` by default (`GEOCAM_HEALTH_ADDR`) | loopback only | `/healthz`, `/readyz`, `/status` (`internal/health`) |

**LOCAL/INBOUND note**: the health/admin surface binds to `127.0.0.1` by
default (`internal/config.DefaultHealthAddr`) — it is not reachable from
the network at all unless an operator explicitly overrides
`GEOCAM_HEALTH_ADDR` to a non-loopback address. In the stock
configuration, **no inbound firewall rule is required** for this surface.

No IP addresses, CIDRs, or specific rule numbers are prescribed here —
those are site-specific and belong to each deployment's own firewall
config, not to this repo. **This hito does not modify any firewall
automatically** — no `iptables`/`nftables` automation was added, and none
was requested.

## R8 — Corporate HTTP/HTTPS proxy

**Audit result: it already works, no code change was needed.**

`internal/transport.Client` (the only outbound HTTP client used for
Edge→SaaS traffic — enrollment, heartbeat, and cloudsink frame/event
upload all share this one client) constructs its `http.Client` without
ever setting `Transport`:

```go
httpClient: &http.Client{Timeout: timeout}
```

An `http.Client` with a nil `Transport` falls back to
`http.DefaultTransport`, whose `Proxy` field is `http.ProxyFromEnvironment`
— Go's standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` support. This means
every Edge→SaaS call already respects the standard proxy environment
variables, with zero custom code and zero custom proxy format.

This was previously **untested**, so a targeted test was added:
`internal/transport/proxy_test.go` (`TestClientRespectsStandardProxyEnv`),
verifying against the real `http.ProxyFromEnvironment` function (not a
reimplementation):
- `HTTPS_PROXY` is honored for the SaaS host.
- `NO_PROXY` bypasses the proxy for a matching host.
- `internal/transport.Client` has no logging path at all (verified by
  inspection — there is nothing to leak a proxy URL's embedded
  credentials into, so there was nothing to test there beyond confirming
  that absence).

**LAN clients are correctly excluded from the corporate proxy.** ONVIF
SOAP (`internal/discovery/onvif/soap.go`) builds its own
`&http.Transport{DisableKeepAlives: true}` with `Proxy` left unset (`nil`)
— it deliberately does **not** go through `HTTP_PROXY`/`HTTPS_PROXY`. This
is correct as-is: ONVIF talks directly to LAN camera IPs, which a
corporate HTTP proxy would either reject or (worse) silently break by
routing through a host that cannot reach the camera segment. This was not
changed — it already does the right thing. RTSP (`internal/rtsp.Dial`) is
a raw TCP dial, not an HTTP client, so an HTTP/HTTPS proxy concept does
not apply to it at all.

No TLS verification was disabled, no MITM certificate handling was added
or changed.

## R9 — HA boundary (future architecture, not implemented)

**FUTURE ARCHITECTURE DOCUMENTED / NOT IMPLEMENTED.**

Today: **one Edge process is the single active owner of its assigned set
of cameras.** There is no code anywhere in this repo for detecting,
coordinating with, or failing over to a second Edge instance.

Explicitly, for any future HA design:

- **Cloning `identity.json`/`credentials.json` onto a second node is NOT a
  HA mechanism.** It produces two processes authenticating as the same
  Edge identity to the SaaS, not a coordinated pair.
- **Two Edge processes pointed at the same camera set can duplicate
  work and events** — both would independently pull RTSP, run detection,
  and upload events/frames, with no de-duplication anywhere in the
  pipeline. This is a correctness problem, not just a resource-waste one.
- **Local state — the offline/evidence buffer, the local event backlog —
  needs an explicit strategy before any active/passive split is possible.**
  Today this state lives on one node's local disk
  (`GEOCAM_DATA_DIR`); a failover story needs to say what happens to
  in-flight buffered data that never made it to a shared location.
- **A real future HA design must address ownership, lease, fencing, and
  failover** — i.e., a mechanism for exactly one node to hold the right to
  process a given camera set at a time, a way to detect and safely revoke
  that right (fencing) so a partitioned old-active can't keep writing, and
  an explicit failover trigger. None of that exists today.

Deliberately **not** done in this hito, per its own scope:

- No cluster design.
- No consensus protocol.
- No etcd (or any other coordination store).
- No leader election.

This section exists so a future HA effort starts from an accurate map of
what already violates HA assumptions (identity, local state, double
processing), not from a blank page.
