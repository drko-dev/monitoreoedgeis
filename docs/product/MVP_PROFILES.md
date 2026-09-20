# GEO CAM Edge — Product Profiles (Hito Z)

## Scope of this document

Hito Z does not build a new system. It packages capabilities that already
exist — and that were already implemented/tested/documented in Hito Q
(residential appliance), Hito R (corporate install), Hito S (security), Hito
T (OTA), Hito X (performance/capacity evidence) and Hito Y (resilience) —
into operable product profiles: **residential (Z1)**, **corporate (Z2)** and
**multi-site (Z4)**. Every claim below is classified `IMPLEMENTED` /
`TESTED` / `VALIDATED` / `NOT_VALIDATED`. Nothing here duplicates the
detailed evidence already recorded in the milestones cited; this document
only cross-references it under a product lens.

Camera counts in this document are **product targets**, never a certified
hardware capacity. Hito X measured synthetic scale (1/5/10/25/50 cameras,
loopback, no real network/SaaS) and explicitly does not certify what any
specific physical hardware model supports (`docs/PROJECT_STATUS.md`, Hito X
section: "no commercial camera-capacity limit is certified from synthetic
loopback runs").

---

## Z1 — MVP Residencial

| Aspect | Definition | Status |
| --- | --- | --- |
| Topology | Single Edge instance per residence, acting as Cloud/Hybrid gateway. No Edge/Gateway required beyond the appliance itself — the Edge daemon *is* the gateway. | IMPLEMENTED (Hito Q) |
| Camera count target | **Product target: 1–10 cameras.** Not a certified capacity — see scope note above. | NOT_VALIDATED as a hardware limit; synthetic scale up to 50 cameras exercised (Hito X) |
| Discovery | ONVIF WS-Discovery (`internal/discovery`), automatic on service start, reused as-is — no new scanner. | IMPLEMENTED / TESTED (Hito E, reused by Q7) |
| Camera credentials | `internal/cameracreds`: local AES-256-GCM cache, per-device master key, SaaS sync. | IMPLEMENTED / TESTED (Hito F) |
| RTSP | `internal/rtsp` unicast pull, auth-failure classified distinctly from a generic dial failure, recovers via credential update without restart. | IMPLEMENTED / TESTED / VALIDATED (Hito G, Y5) |
| Processing modes allowed | `cloud` and `hybrid`. `edge` (local YOLO) is **not part of this profile** — no Vision Worker hardware sizing exists for a residential target. | IMPLEMENTED (mode enum); residential restriction is a product decision documented here, not a code gate |
| Health | Local `/healthz`/`/readyz`/`/status`, `127.0.0.1` only. | IMPLEMENTED / TESTED (Hito B) |
| OTA | Signed (Ed25519) + checksummed update, credential-less download, systemd-based rollback on failed health check. | IMPLEMENTED / TESTED / MERGED (Hito T1–T7) |
| Without Internet | The local Edge process and camera/video pipeline keep running. `internal/heartbeat` does not queue heartbeats — it retries with bounded exponential backoff and jitter, reports the module `DEGRADED`, and resumes sending on recovery; local health stays independent of SaaS reachability. Recoverable Cloud/Hybrid frame uploads may enter `internal/cloudsink.Buffer`'s existing bounded disk-backed spool and are replayed FIFO on reconnect; once that bounded spool is full, additional frames are dropped per its existing policy — this is not a "no loss during an arbitrarily long outage" guarantee. Full-Edge events/evidence use the separate `internal/edgebacklog` queue, not part of this Cloud/Hybrid residential profile. Heavy Cloud inference does not continue while SaaS/Internet is unreachable. | VALIDATED (Y3, Y4 — within the limits of the simulated transports used, no real ISP outage) |
| If a camera drops | RTSP supervisor retries with backoff, distinguishes `auth_failed` from `degraded`, recovers without restart or duplicate supervisors. | VALIDATED (Y5, against RTSP simulators — not real-camera field conditions) |
| Initial install | `package.sh` + `install.sh` + systemd unit + `bootstrap.sh`, zero-touch enrollment via a seed token file, non-root service user. | IMPLEMENTED / TESTED (Hito Q4, Q6) — real first-boot hardware validation NOT_VALIDATED |
| Update | Same OTA path as above; `/readyz`-gated activation. | IMPLEMENTED / TESTED (Hito T5–T7) |
| Recovery | Restart (Y1: VALIDATED via reused Hito W tests), abrupt power loss (Y2: PARTIALLY_VALIDATED — fsync-before-rename + ENOSPC/EDQUOT classification, validated via deterministic abrupt-termination proxies, not a real power cut), corrupt local state (Y8: VALIDATED — fail-closed, never silently regenerated), factory reset (Q9: IMPLEMENTED / TESTED, real hardware reset NOT_VALIDATED). | see individual entries |

**Explicit non-claims for Z1:** no RAM/storage minimum is established for
any specific board (Q1); no physical ARM64/AMD64 residential hardware was
validated (Q2/Q3); no real power-loss or real disk-exhaustion test was run
(Y2/Y6 use deterministic fault injection, not physical conditions); no
non-technical-user usability study was performed (Q10).

---

## Z2 — MVP Corporativo

Builds on Z1. Only the deltas are described here — everything in Z1 that is
not contradicted below still applies.

| Aspect | Definition | Status |
| --- | --- | --- |
| Multiple cameras | Same discovery/credentials/RTSP stack as Z1, scaled to the site's real camera count. No separate corporate code path. | IMPLEMENTED (shared with Z1) |
| DVR/NVR | Not a distinct integration in this repository: an existing DVR/NVR is addressed like any other ONVIF/RTSP source if it exposes those interfaces. No DVR-specific protocol/driver exists. | NOT_VALIDATED — no DVR/NVR device was tested |
| Private networks / VPN site-to-site / subnet routing | **Existing connectivity rule, unchanged for Z2:** the VPN tunnel must terminate at the gateway/router/firewall/subnet-router/host layer — never inside the Edge daemon or inside a camera, unless a camera has explicit vendor VPN support (not assumed). The Edge requires no VPN keys, certs, tunnels or crypto agents of its own; it consumes standard routed IP. RTSP (TCP 554 unicast) and ONVIF SOAP natively cross routed L3 boundaries. | DONE (documented architecture, Hito R4/R5) |
| Cross-subnet camera provisioning | **Explicit gap, not solved by Z2**: WS-Discovery is UDP multicast and does not cross routers without external multicast relay; Remote Config cannot inject new camera network targets (`DisallowedKeys` blocks it by design). A camera on a routed subnet must be provisioned with a target already known to the Edge; automatic cross-subnet discovery is not implemented. Documented alternative (not mandatory): one Edge per segment. | CURRENT GAP / NOT IMPLEMENTED (Hito R5/R7, unchanged) |
| VLANs | OS-managed 802.1Q (dedicated port or trunk subinterface, e.g. `eth0.20`); Edge does not manage tags itself. Interface selection via `GEOCAM_DISCOVERY_INTERFACES`. | DONE (documented, Hito R6) |
| Isolation by site | Each Edge instance is a single tenant/site scope: `internal/credentials.Credentials.TenantID`/`SiteID` are learned only from an authenticated `GET /me` call after enrollment, never asserted by the Edge itself. Local events (`internal/fulledge`) are stamped with this same tenant/site, not a locally-invented one. | IMPLEMENTED / TESTED |
| Health | Same `/healthz`/`/readyz`/`/status` as Z1; corporate deployments consume it via the existing SaaS Edge-list/health surface (Hito U), not a new corporate-only endpoint. | IMPLEMENTED (Edge side); SaaS-side aggregation is out of this repo's scope |
| Remote operation | OTA (T1–T7), remote config (`internal/remoteconfig`, scoped and allowlisted — cannot inject arbitrary keys or new network targets), control commands via `internal/control` (fixed allowlist of 4 command types, no shell, no `os/exec`). | IMPLEMENTED / TESTED |
| Offline behavior | Same as Z1 (Y3/Y4). Corporate networks add proxy support: `internal/transport.Client` already honors standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` (Go's `ProxyFromEnvironment`) with no code change required — confirmed by `internal/transport/proxy_test.go`. | TESTED (Hito R8) |
| Evidence/events | Local event backlog (`internal/edgebacklog`) with bounded arena, quarantine for corrupt/rejected records, disk-full classification. | IMPLEMENTED / TESTED / VALIDATED (Y6, Y7 — deterministic fault injection, not a real full filesystem) |
| Logs/diagnostics | Structured `slog`, no secrets/tokens/credentials logged (enrollment, rotation, factory reset, control execution all covered). Not a tamper-evident audit trail — `internal/control.Ledger` is idempotency/retry-safety, explicitly not an audit log. | PARTIAL (Hito S11) |
| Secure configuration | TLS required by default for Edge→SaaS; device credential at rest is **not** hardware-key-backed (no TPM/HSM/KMS trust root exists) — documented gap, not solved by Z2. | PARTIAL (Hito S3/S4) |
| Firewall | Outbound matrix (Edge→SaaS, Edge→cameras, WS-Discovery multicast) and local-only health bind documented in `docs/deployment/corporate-enterprise.md`. No firewall automation. | DOCUMENTED (Hito R8) |

**Explicit non-claims for Z2:** no VPN product/vendor is required or bundled
by this repository — R4 documents an architecture, not a dependency; no
industrial appliance hardware, temperature range, IP rating or MTBF is
certified (R3); HA (active/passive Edge) is future architecture only, not
implemented (R9); DVR/NVR integration is untested.

---

## Z4 — Multi-site

### What already exists (audit result — no new control plane needed)

The Edge already carries everything a multi-site product needs at the
device level, without any new abstraction:

- **Site identity is Edge-scoped, not self-declared.** One running Edge
  instance = one `identity.json` (`edge_id`, permanent) = one enrollment =
  one `TenantID`/`SiteID` pair, resolved exclusively by the SaaS on
  `GET /me` after enrollment and cached in `credentials.json`
  (`internal/credentials.Credentials`). The Edge never invents or asserts a
  tenant/site value — this was true before Z and required no change.
- **What belongs to the Edge**: local processing, local health, local event
  backlog, local camera credentials/RTSP state, local OTA state. All scoped
  to *this* Edge's own `edge_id`/`TenantID`/`SiteID`, persisted only on this
  instance's disk.
- **What belongs to the SaaS**: cross-site aggregation, per-tenant/per-site
  permissions and views (Hito U1–U11: Edge list, health, cameras, processing
  mode, remote config, metrics, actions — all already scoped by
  tenant/site on the SaaS side), plan/quota enforcement (Hito V). This repo
  does not implement or duplicate that control plane.
- **Failure isolation between sites is structural, not a feature that had to
  be added**: each site's Edge is an independent OS process with its own
  identity, credential, camera list and local backlog. Nothing in this
  codebase makes one Edge's local processing depend on another Edge's
  state or reachability — there is no shared local state between Edge
  instances to begin with.
- **Per-site upgrade**: OTA (Hito T) is already a per-device pull model
  (`GEOCAM_EDGE` polls its own "update available" via heartbeat / OTA
  endpoint and decides locally when to fetch/verify/apply) — there is no
  fleet-wide push, so upgrading one site's Edge already has zero effect on
  any other site's Edge. Staged OTA rollout / canary / compatibility logic
  belongs to the SaaS control plane and is already merged there as part of
  Hito T8–T10 (`monitoreoia` PR #124/#125/#126). This Edge repository does
  not duplicate that fleet orchestration.
- **Per-site health**: the existing `/healthz`/`/readyz`/`/status` per Edge
  instance is exactly the unit Hito U's SaaS-side per-site health view
  already consumes; no new health surface was needed.

### Current limits (explicit, not solved by Z4)

- Cross-subnet/cross-site automatic camera discovery does not exist (same
  gap as R5/R7: WS-Discovery multicast does not cross routers).
- Multi-tenant permission logic, quotas and billing (Hito U/V) live entirely
  in `monitoreoia` (SaaS), not in this repository — auditing or changing
  them is out of Z4's scope.
- No Edge-side notion of "multiple sites managed by one running instance"
  exists or is proposed — the product model is **one Edge process per
  site**, which is the assumption every classification above relies on. A
  single Edge instance managing several physically distinct sites is
  explicitly out of scope and was not implemented.

### Conclusion

No Edge code change was required for Z4. The existing enrollment-derived
tenant/site contract, the per-instance identity model, and the already-
independent per-Edge lifecycle (OTA, health, backlog) already satisfy
multi-site isolation and per-site operability at the Edge level.

---

## Classification summary

| Claim | Status |
| --- | --- |
| Z1 residential profile is buildable from existing code | IMPLEMENTED / TESTED |
| Z1 residential physical hardware validated | NOT_VALIDATED |
| Z2 corporate profile deltas (VPN termination rule, proxy, VLAN, firewall matrix) | DOCUMENTED / TESTED where noted above |
| Z2 cross-subnet automatic camera provisioning | NOT IMPLEMENTED (known gap, unchanged by Z2) |
| Z4 multi-site Edge-side contract (site identity, isolation, per-site OTA/health) | IMPLEMENTED / TESTED (pre-existing, confirmed by this audit) |
| Z4 SaaS-side multi-tenant control plane | OUT OF SCOPE (separate repo, not audited here) |
| Any specific hardware supporting N cameras | NOT_VALIDATED (Hito X is synthetic loopback evidence only) |
