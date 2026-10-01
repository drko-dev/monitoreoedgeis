# DVR-1 — Hikvision DVR/NVR Multichannel Integration

Status: **IMPLEMENTED; AUTOMATED_TESTS_PASS; PUSHED; CODE_PASS_WITHHELD**.
Physical compatibility with the customer recorder remains
**PENDING_PHYSICAL_VALIDATION**.

- Repository: `drko-dev/monitoreoedgeis`
- Branch: `feature/dvr-1-hikvision`
- Verified base: `b13aa4012b0220b3d67caaeb4ebd16af495168c3`
- Target recorder: Hikvision AcuSense `iDS-7208HUHI-M1/FA`
- Reported firmware: `V4.75.011`, build `240620`
- Customer infrastructure access: not available for this milestone
- Production, SaaS, Mobile, DVR configuration changes: out of scope

## 1. Verified repository state

The DVR-1 branch was created from the current `origin/main`, not from the
local checkout that contained the independent S6A work.

`feature/edge-s6a-auto-credential-rotation` and PR #112 were inspected and
left untouched. At the start of DVR-1, PR #112 was open, mergeable, and green
at `6e9cf5b8d8575d7ad99c693a3038fab94fd22dea`; relative to the current main it
was 20 commits behind and one commit ahead. DVR-1 does not depend on or copy
that change.

The GitHub CI run for the verified DVR-1 base completed successfully. Phase B
has since added implementation changes on this branch; see the implementation
record below for fresh verification. The branch still needs implementation
commits pushed and verified before it meets the `CODE_PASS` synchronized-state
condition.

## 2. Existing architecture that must be reused

The repository already implements the generic multichannel model. DVR-1 must
extend it rather than add a parallel recorder subsystem.

| Requested responsibility | Existing implementation to reuse | DVR-1 action |
|---|---|---|
| Recorder discovery | `internal/discovery.Engine`, WS-Discovery, private-XAddr validation, bounded enrichment workers | Add manual seed endpoints and an optional recorder enrichment seam |
| ONVIF identification | `internal/discovery/onvif.Client`, `GetDeviceInformation`, `GetCapabilities` | Keep as the primary identification path |
| Channel inventory | `DiscoveredDevice.VideoSources` | Repair profile-to-source association; add explicit availability |
| Channel identity | `ChannelCandidateKey(deviceStableIdentity, sourceToken)` | Preserve opaque, non-IP logical-camera identity |
| Profile resolution | ONVIF `GetProfiles` and `GetStreamUri`; `selectProfile` | Associate profiles with their actual source; add a Hikvision fallback only when needed |
| Credential lookup | `internal/cameracreds.Provider` | Resolve once by physical recorder identity and share only in memory across its channels |
| RTSP probe/client | `internal/rtsp`, `internal/rtsptest` | Reuse; do not create a second RTSP stack |
| Runtime stream ownership | `internal/rtsp.Manager` and `Supervisor` | Reuse one supervisor per logical channel |
| Local physical-device inventory | `internal/discovery.Inventory` | Reuse its stable upsert/TTL behavior |
| Logical camera expansion | `internal/agent.buildCameraTargets` and installer onboarding candidates | Extend only for disabled-channel filtering and corrected profiles |
| Rediscovery | inventory upsert + candidate keys + target reconciler | Preserve idempotency; no new registry package |
| SaaS synchronization | existing discovery reporting and onboarding contracts | No endpoint or SaaS repository changes |

This means the runbook names map to existing components as follows:

- `RecorderDiscovery`: `discovery.Engine` plus manual seed endpoints.
- `ChannelNormalizer`: `VideoSource`, `MediaProfile`, and one small grouping
  function in discovery.
- `StreamResolver`: ONVIF URI resolution first, optional Hikvision convention
  second.
- `StreamProbe`: existing RTSP client/simulator.
- `EdgeCameraRegistry`: existing `Inventory`, composite candidate keys,
  target reconciler, and RTSP manager. A second registry would duplicate
  ownership and is rejected.

## 3. Defects and gaps found during Phase A

### 3.1 Critical existing ONVIF association defect

The ONVIF parser correctly captures each profile's
`VideoSourceConfiguration.SourceToken` in
`onvif.MediaProfile.VideoSourceToken`. `discovery.Engine`, however, drops that
field while mapping profiles and assigns every returned profile to
`VideoSources[0]`.

Consequences:

- a recorder can expose multiple channels while only the first gets profiles;
- later channels are emitted as logical cameras without usable streams;
- current builder tests pass because they construct already-normalized
  fixtures and do not exercise the engine mapping path.

DVR-1 must fix this shared normalization point before adding vendor fallback
logic. The repair benefits generic ONVIF multichannel recorders and does not
change single-camera behavior.

### 3.2 No explicit channel availability

`VideoSource` has no representation for enabled, disabled, or unknown. DVR-1
will add a tri-state availability value:

- `unknown`: ONVIF or vendor data did not make the configuration state clear;
- `enabled`: the device explicitly reports at least one enabled stream for the
  channel;
- `disabled`: the device explicitly reports the channel's streams disabled.

Availability is configuration state, not live connectivity. RTSP negotiation
and frame reception remain separate runtime facts.

### 3.3 No manual device seed

Discovery currently depends on multicast WS-Discovery. DVR-1 will add an
optional, non-secret list of explicit Hikvision recorder base URLs through the
existing environment/persistent-config mechanism. Manual seeds pass through
the same private-address validation, enrichment, inventory, and deduplication
path as multicast candidates.

The configuration contains no username, password, RTSP URL, or token.
Credentials continue to come from `cameracreds.Provider`.

### 3.4 No vendor adapter or ISAPI fallback

There is no Hikvision/ISAPI implementation today. ONVIF remains the default
for every manufacturer. A Hikvision adapter will be optional and selected only
when the manufacturer is identified as Hikvision or an endpoint is explicitly
configured as Hikvision.

### 3.5 No physical evidence

The generic multichannel feature has simulator/unit coverage but no validated
result against the reported recorder and firmware. No design or simulation
may change this status.

## 4. Architecture decision

### 4.1 Discovery and enrichment order

For every candidate:

1. Validate the endpoint as a private HTTP/HTTPS address.
2. Reuse ONVIF for device information, capabilities, video sources, profiles,
   and stream URIs.
3. Group every ONVIF profile by its `VideoSourceToken`; never assign all
   profiles to the first source.
4. If the device is Hikvision and ONVIF data is incomplete, or explicit
   availability is required, invoke the optional read-only Hikvision adapter.
5. Merge only fields for which the protocols provide a deterministic join.
6. Store the physical recorder once in `Inventory` and expose each normalized
   channel through the existing composite candidate-key flow.

An ISAPI failure does not invalidate complete ONVIF data. An ONVIF failure does
not cause endpoint guessing for an unconfigured vendor.

### 4.2 Minimal adapter contract

The generic discovery package will own a narrow interface; the Hikvision
implementation will live outside it and be injected by agent/installer wiring.

```go
type RecorderAdapter interface {
	Vendor() string
	Discover(ctx context.Context, request RecorderRequest) (RecorderSnapshot, error)
}
```

`RecorderRequest` carries the already-validated endpoint plus the resolved
credential for the duration of the call. `RecorderSnapshot` can contribute
identity metadata and normalized channels, but cannot mutate inventory,
credentials, RTSP supervisors, or SaaS state.

This is deliberately the only new vendor extension point. There is no factory,
plugin loader, global registry, or universal Hikvision dependency. A future
manufacturer can implement the same read-only contract and be selected by its
vendor key.

### 4.3 Hikvision adapter scope

The adapter is read-only:

- identify the device using data returned by ONVIF and documented ISAPI
  identity/capability responses when available;
- enumerate documented streaming-channel entries;
- preserve device-provided stream IDs and non-consecutive channel numbers;
- group stream IDs by physical channel number;
- expose main (`...01`) and sub (`...02`) profiles;
- preserve other stream types as unknown rather than mislabel them;
- return explicit enabled/disabled state only when reported by the device.

No PUT, POST, DELETE, activation, reboot, user-management, certificate, or DVR
configuration endpoint is allowed in DVR-1.

### 4.4 Channel identity and normalization

The normalizer will keep two concepts distinct:

- `SourceToken`: the existing opaque logical-camera identity used by
  `ChannelCandidateKey`;
- vendor channel/stream identifiers: metadata used to associate ISAPI and
  RTSP results.

ONVIF `SourceToken` remains authoritative when present. ISAPI-only channels
receive a namespaced token derived from the exact recorder channel number,
not from list position. Display indexes remain cosmetic and never become
identity.

ONVIF and ISAPI records are merged only when at least one of these proves the
join:

1. exact source/channel identifier equality; or
2. an ONVIF-provided RTSP URI contains the same documented Hikvision stream
   ID.

Order-based joins are forbidden. If the join is ambiguous, both the raw
evidence and limitation are reported without fabricating a match.

Rediscovery with the same physical identity and source tokens updates the
existing inventory entry and reconciles the same logical targets. Disabled
channels remain visible in inventory/onboarding but do not create RTSP
supervisors.

### 4.5 Stream resolution

Resolution priority:

1. sanitized URI returned by ONVIF `GetStreamUri`;
2. sanitized URI or stream ID returned by documented ISAPI inventory;
3. a URL constructed from the documented Hikvision convention only when the
   adapter has an actual discovered stream ID.

The Hikvision RTSP convention is:

`rtsp://<host>[:port]/ISAPI/Streaming/channels/<stream-id>`

where `stream-id = channel-number * 100 + stream-type`, stream type `1` is
main and `2` is sub.

A constructed URL is marked as constructed evidence. It is not called
discovered, connected, or validated until the existing RTSP client completes
authentication/negotiation and receives media. Credentials are passed
separately to `rtsp.Dial`; they are never embedded in stored/logged URLs.

### 4.6 RTSP probe and runtime lifecycle

No new RTSP client is introduced.

- onboarding/validation reuses the existing credential-test path;
- automated tests reuse `internal/rtsptest`;
- runtime uses `rtsp.Manager` and one `Supervisor` per logical channel;
- dial and packet timeouts use existing configuration;
- cancellation and supervisor removal close sessions and goroutines;
- disabled channels are filtered before target creation;
- a failed channel does not collapse sibling channels on the same recorder.

The existing RTSP digest implementation and the ISAPI HTTP client need the
same digest calculation. DVR-1 will extract only the protocol-neutral digest
challenge/response calculation into a small internal helper used by both
callers. The HTTP adapter performs GET-only authentication with one bounded
challenge retry; it does not add a dependency.

## 5. Security constraints

- Manual endpoints must be private IPv4 HTTP/HTTPS addresses and pass the
  existing fail-closed validator.
- HTTP redirects are rejected to prevent an SSRF hop.
- HTTPS uses normal certificate validation; `InsecureSkipVerify` is forbidden.
- The HTTP client has a total timeout, bounded response size, and closes every
  response body.
- ISAPI uses only GET requests and never enables the service or changes DVR
  state.
- Authentication errors are classified without including usernames,
  passwords, authorization headers, response bodies containing secrets, or
  credential-bearing URLs.
- Camera credentials remain encrypted at rest in the existing store and are
  resolved by physical recorder candidate key.
- Tenant/site isolation stays in the existing SaaS credential assignment and
  onboarding contracts; DVR-1 introduces no cross-tenant lookup.
- Discovery concurrency remains bounded by the existing worker pool. Vendor
  enrichment runs inside that bound; it does not start a second scanner.
- XML parsing is streaming/bounded and rejects malformed or oversized
  responses.

## 6. Planned implementation slices

1. **Repair generic normalization**
   - group ONVIF profiles by `VideoSourceToken` in anonymous and authenticated
     enrichment;
   - preserve single-source fallback only when the mapping is unambiguous;
   - add channel availability and disabled-target filtering.
2. **Manual recorder seeds**
   - add non-secret Hikvision endpoint configuration;
   - feed validated seeds through the existing dedup/enrichment/inventory path.
3. **Hikvision read-only adapter**
   - bounded HTTP client with Basic/Digest challenge support;
   - documented identity/capability and streaming-channel GETs;
   - parse, group, normalize, and merge without order assumptions.
4. **Stream fallback and probe reuse**
   - construct only from discovered Hikvision stream IDs;
   - reuse RTSP parser/client/simulator and existing lifecycle.
5. **Agent/installer wiring and diagnostics**
   - inject the optional adapter;
   - keep existing inventory, onboarding, credential, and target reconciliation
     ownership;
   - expose safe failure reasons without secrets.

Each slice must have focused tests before the next slice is committed.

## 7. Test design

| Required case | Verification |
|---|---|
| One-channel recorder | ONVIF/ISAPI fixture produces one logical source and target |
| Multiple channels | Exact source-token grouping produces one target per channel |
| Disabled channels | Inventory retains the channel; target builder omits it with a safe reason |
| Non-consecutive IDs | IDs such as 1, 3, and 8 remain 1, 3, and 8; no renumbering |
| ONVIF unavailable | Explicit Hikvision seed can use ISAPI; failure is isolated and classified |
| ISAPI unavailable | Complete ONVIF data continues to work; no universal vendor dependency |
| Authentication rejected | 401/403/Digest rejection returns a safe typed error and no secret |
| Malformed response | Bounded parser rejects invalid/truncated XML without partial mutation |
| Rediscovery | Repeated snapshot updates the same physical and logical identities |
| Profile selection | Main/sub selection uses source-local profiles and deterministic roles |
| RTSP timeout/disconnect | Existing simulator proves timeout, peer close, retry, and cancellation |
| Resource cleanup | Response bodies, RTSP sessions, supervisors, timers, and goroutines close |
| Camera regression | Existing single-camera ONVIF and target-builder tests stay green |

Verification levels must remain explicit:

- unit: parsers, normalization, identity, selection, error classification;
- simulator integration: HTTP fixtures plus the existing RTSP simulator;
- repository regression: `go test ./...`, `go vet ./...`, and race tests for
  changed packages;
- physical homologation: pending customer access.

## 8. Documented vendor contracts

Only contracts supported by manufacturer documentation are eligible:

- Hikvision product datasheet for `iDS-7208HUHI-M1/FA`: 8 analog inputs,
  additional IP-channel capability, main/sub streams, and ONVIF listed among
  supported protocols:
  <https://www.hikvision.com/content/dam/hikvision/products/S000000001/S000000132/S000000133/S000000821/OFR000174/M000018391/Data_Sheet/Datasheet-of-iDS-7208HUHI-M1_FA_V4.71.140_20230210.pdf>
- Hikvision NVR manual: ISAPI and ONVIF are independently enabled services;
  the adapter must tolerate either being unavailable and must never enable
  them:
  <https://assets.hikvision.com/prd/public/all/doc/m000000601/UD39811B_Network-Video-Recorder_User-Manual_V5.04.000_20241120.PDF>
- Hikvision ISAPI service documentation: HTTP-based service that may require
  explicit device-side enablement:
  <https://enpinfo.hikvision.com/hkwsen/unzip/20230410194813_20373_doc/GUID-32DED0BA-F218-4F6D-BC9F-525FFAB4BDE0.html>
- Hikvision RTSP contract: `/ISAPI/Streaming/channels/<ID>` and documented
  channel/stream-number formula:
  <https://enpinfo.hikvision.com/unzip/20201110210551_77443_doc/GUID-515FF2B5-5E01-4F03-8B81-4CA5BD621965.html>
- Hikvision channel inventory example: `GET /ISAPI/Streaming/channels` returns
  stream IDs, channel names, enabled state, codec, and resolution:
  <https://international-robot.hikvision.com/upload/web/1476067342641247/20220902/57811662081257893.pdf>
- ONVIF conformance is firmware-specific and must be verified for the exact
  customer build:
  <https://www.onvif.org/conformant-products/>

The public datasheet revision does not match the reported customer firmware.
Exact endpoint availability and response shape therefore remain physical
validation items. Model-family documentation is evidence for implementation,
not proof of homologation.

## 9. Physical homologation procedure

When authorized SSH/desktop access to the customer environment becomes
available:

1. Record Windows edition/build, WSL2 distribution/build if used, NIC routes,
   and Edge binary version without collecting passwords.
2. Confirm the recorder model, serial redaction policy, firmware, ONVIF state,
   ISAPI state, HTTP/HTTPS port, and RTSP port from authorized settings.
3. Run explicit-endpoint discovery from the customer LAN; do not scan external
   networks.
4. Verify manufacturer/model/firmware identification and compare ONVIF versus
   ISAPI channel inventories.
5. Record every device-provided channel and stream ID, including disabled and
   non-consecutive channels.
6. Verify main/sub resolution, codec, and sanitized URI origin for each
   authorized channel.
7. Probe RTSP with operator-entered credentials kept outside logs and shell
   history; confirm authentication, SDP negotiation, codec, and frame receipt.
8. Confirm independently that disabled channels do not create supervisors and
   enabled-but-offline channels remain distinct from disabled channels.
9. Interrupt network access and the recorder session; verify timeout, cleanup,
   bounded retry, and recovery without duplicate logical cameras.
10. Inspect logs/status for passwords, authorization headers, credential-bearing
    URLs, and cross-organization identifiers.
11. Repeat a discovery cycle and compare candidate keys to prove idempotency.
12. Record results as physical evidence; do not modify recorder configuration.

## 10. Acceptance boundary

DVR-1 may reach `CODE_PASS` only after implementation, simulator negotiation,
all required tests, vet, race checks, regression tests, clean Git state, and a
verified branch push.

Even with `CODE_PASS`, the target recorder remains:

`PHYSICAL_VALIDATION = PENDING`

until the physical procedure above is completed against the reported model and
firmware with authorized credentials.

## 11. Phase B implementation record

### Implemented

- Fixed ONVIF profile association by preserving
  `VideoSourceConfiguration.SourceToken`. Untagged fallback is retained only
  when one source makes the association unambiguous.
- Added tri-state channel and stream availability. Disabled channels remain
  discoverable but do not produce runtime camera targets; the installer
  projection now includes device-reported channel number and availability.
- Added `GEOCAM_HIKVISION_ENDPOINTS`, a comma-separated list of at most 16
  credential-free HTTP(S) base URLs. The existing fail-closed private endpoint
  validator still gates outbound discovery; credentials never belong in this
  setting.
- Added `internal/hikvision` as an optional GET-only ISAPI adapter for
  `/ISAPI/Streaming/channels`. It groups the device-reported stream IDs by
  `id / 100`, preserves non-consecutive channel identifiers, stream IDs,
  names, codec/resolution, and enabled status, and rejects duplicate IDs,
  malformed XML, and responses over 1 MiB.
- Added a deterministic Hikvision RTSP URI resolver for an ISAPI-discovered
  stream ID. These URIs are explicitly marked `hikvision_constructed`; URI
  construction is not evidence of RTSP reachability.
- Extracted the existing MD5 Digest challenge helper into
  `internal/digestauth` for shared RTSP/HTTP use. Digest supports the existing
  MD5 subset; HTTP Basic challenge retry is allowed only over HTTPS. TLS
  certificate verification remains at Go's default.
- Wired the optional adapter into agent and installer discovery. Enrichment
  reuses the existing physical-device credential resolver and inventory. An
  authentication rejection is surfaced as `AuthRequired` without logging
  credentials. Channel/profile metadata is merged only by exact identifiers
  or a matching discovered Hikvision stream ID in an ONVIF URI.

### Contract and explicit limitations

The implementation uses the documented channel-list GET and RTSP URL
convention referenced above. It does **not** call a separate ISAPI identity or
capability endpoint: automatic adapter selection requires ONVIF to identify
the manufacturer as Hikvision; a manual endpoint is explicitly treated as
Hikvision. ONVIF remains the identification path, not a new undocumented
ISAPI probe.

The adapter returns the configured channel snapshot. Automated simulator
integration now carries an ISAPI-discovered stream ID through the Hikvision
URI resolver, substitutes only the simulator's ephemeral local host/port,
parses that URI through the existing RTSP target parser, completes RTSP
DESCRIBE/SETUP/PLAY negotiation, checks the negotiated H.264 codec, and reads
a non-empty RTP packet. The RTSP simulator does not emulate the Hikvision
device, firmware, HTTP/ISAPI response behavior, or its actual RTSP server; it
only proves that the adapter's discovered stream ID/path can be consumed by
the existing client and that the simulator negotiates and supplies media.
Physical support for the exact iDS model/firmware, enabled HTTP/ISAPI state,
and actual stream IDs remains pending authorized customer access.

### Verification and final closure

The focused package command passed after the relevant implementation changes,
followed by the complete repository suite:

```text
go test ./internal/config ./internal/digestauth ./internal/hikvision ./internal/rtsp ./internal/discovery ./internal/discovery/onvif ./internal/agent ./internal/installer
go test ./...
go vet ./...
go test -race ./internal/digestauth ./internal/hikvision ./internal/discovery ./internal/agent ./internal/installer ./internal/rtsp
```

The full Go test suite, `go vet ./...`, race tests for digest auth, Hikvision,
discovery, agent, installer, and RTSP, the focused adapter-to-RTSP simulator
test, and `git diff --check` all passed after the audit fixes. Regression
coverage now includes whole stream-ID association, ONVIF URI precedence,
manual/WS-Discovery identity coalescing, and adapter-to-RTSP simulator data
receipt. No customer recorder was contacted. Credential identity remains unresolved:
channel-scoped runtime credentials are keyed by composite channel identity,
while authenticated ONVIF/ISAPI enrichment and rediscovery resolve only the
physical recorder identity. The intended bootstrap/rediscovery contract must
be reconciled before CODE_PASS. Audit fixes were committed as
`8da6beda599094386ca8f2a54e99c15eb43d4ae2` and pushed to
`feature/dvr-1-hikvision`; the branch is clean and synchronized. No SaaS,
Mobile, infrastructure, S6A, PR, merge, deployment, or production work was
performed.

### Final status

`CODE_STATUS = NOT_CODE_PASS`

`PHYSICAL_VALIDATION = PENDING_PHYSICAL_VALIDATION`

Implemented code and previous automated checks are distinguished from the
pending fresh final checks. Synthetic HTTP and RTSP simulator integration does
not claim physical recorder compatibility or customer homologation. CODE_PASS
is withheld until credential resolution works consistently across discovery,
rediscovery, and runtime, all final checks pass, and the verified commit is
pushed to this branch.
