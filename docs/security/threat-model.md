# GEO CAM Edge Security Threat Model (Hito S1)

This threat model describes the security properties actually present in the
Edge repository. It is a bounded engineering model, not a claim of complete
security or certification.

## Assets

- Device identity (`identity.json`, `edge_id`).
- Device credential (`credentials.json`).
- Enrollment token during bootstrap/enrollment.
- Camera credentials (`camera_credentials.json`) and its local master key
  (`camera_master.key`).
- Remote configuration and control-command state.
- Local evidence, events and offline buffers.
- Release/package artifacts and the active release pointer.
- SaaS transport sessions and API payloads.
- RTSP/ONVIF CCTV-LAN traffic.

The UUID-like device identity is an identifier, not a secret. Device
credentials, enrollment tokens, camera passwords and local keys are secrets.

## Trust boundaries

1. **Edge host** — the Go daemon, local data directory, systemd and local users.
2. **SaaS** — enrollment, authenticated Edge APIs, control and remote config.
3. **Internet/WAN** — the network between Edge and SaaS.
4. **CCTV LAN** — cameras, RTSP and ONVIF discovery/management traffic.
5. **Local filesystem** — permissions and persistence for identity, secrets,
   state, buffers and evidence.
6. **Update/package supply chain** — build machine, artifact, checksum and
   install/update scripts.
7. **Operator/admin boundary** — users who can install, configure, enroll,
   update or access the host.

TLS on the SaaS boundary does not encrypt or authenticate RTSP/ONVIF traffic on
the CCTV LAN. Those are separate trust decisions.

## Threats and current treatment

| Threat | Existing mitigation | Remaining gap / owner |
|---|---|---|
| Device-credential theft | Credential is generated locally with `crypto/rand`; only its SHA-256 hash is sent during enrollment; file and data-directory permissions are restrictive. | `credentials.json` still contains the credential in plaintext at rest. Key management for encrypting it is not established. Owner: future security/key-management decision. |
| Enrollment-token replay | SaaS enrollment tokens are short-lived/one-use in the existing enrollment contract; revoke/reissue flows exist; bootstrap removes an ephemeral token file best-effort. | A token can still be stolen before claim if the host, seed media or operator channel is compromised. No hardware-bound enrollment root exists. Owner: SaaS/operational process. |
| Revoked device | Authenticated SaaS calls share the transport client and receive authorization failures; local credential rotation and re-enrollment paths exist. | A device that is offline cannot receive a remote revocation immediately. Owner: future offline/revocation policy. |
| Stolen local disk | Camera passwords are encrypted with AES-256-GCM; identity, credential and state files use restrictive modes; factory reset removes device/camera secret state. | `camera_master.key` is stored beside its ciphertext, and the device credential is not encrypted. This is not protection against a privileged disk thief. Owner: key-management decision. |
| Malicious or compromised camera | ONVIF XAddr validation is fail-closed: HTTP(S) only, no userinfo, private IPv4 literals only, no loopback or cloud metadata endpoints, bounded sizes. | A trusted private-network camera can still be malicious or provide hostile media. Owner: camera/network isolation and future hardening. |
| ONVIF SSRF | Discovery validates announced XAddrs and rejects public, loopback and cloud-metadata destinations. | The CCTV LAN remains an operator trust boundary; this is not a general network firewall. Owner: network segmentation. |
| Control-command replay | Control state uses a durable ledger/idempotency-oriented flow and reports command outcomes; transport authentication is shared. | Full replay/fencing guarantees depend on the SaaS command contract and are not a cryptographic signature scheme. Owner: future control-plane protocol review. |
| Remote-config tampering | Strict field validation, allowlisted configuration, atomic state writes and rollback/readiness handling. | A privileged local user can modify local files or the running host. No TPM/HSM-backed integrity root exists. Owner: future host-integrity decision. |
| Evidence/event tampering | Atomic writes, bounded stores and SHA-256 content metadata are used for local artifacts. | Local hashes are integrity metadata, not an external trusted notarization or immutable audit log. Owner: future evidence/audit design. |
| Update/package tampering | Architecture checks, mandatory artifact checksum verification in `update.sh`, versioned releases and readiness rollback. | No signed-release or verified supply-chain policy is implemented. A compromised checksum/distribution channel remains a gap. Owner: future release-signing milestone. |
| SaaS network MITM | Edge SaaS clients require HTTPS by default; normal Go hostname/certificate verification is retained; insecure HTTP is an explicit development escape only. | HTTP can be enabled intentionally for development. No private CA, pinning or mTLS is implemented. Owner: deployment policy, not an invented transport feature. |
| Duplicate Edge identity | Identity is generated with random UUIDv4 and persisted atomically; factory reset deletes it. | Cloning a data directory can duplicate identity and credentials. Owner: provisioning process and future identity/attestation policy. |
| Secret leakage in logs | CLI reports redact enrollment tokens and credentials; enrollment tests assert plaintext secrets are not emitted. | Log-review coverage is not a formal whole-program proof. Owner: future security test/audit expansion. |

## Security posture

The repository has meaningful local and transport controls, but it does not
provide hardware-backed key storage, signed updates, mTLS, or immutable audit
storage. Those omissions are explicit gaps, not silently assumed guarantees.
