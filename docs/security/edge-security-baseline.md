# GEO CAM Edge Security Baseline (Hito S1–S4)

This baseline records what is implemented, what is partial, and what requires a
future decision. It does not claim security certification.

## S1 — Threat model

**IMPLEMENTED / DOCUMENTED**

- Assets and trust boundaries are documented in
  [`threat-model.md`](threat-model.md).
- Concrete threats cover credential theft, enrollment replay, revocation,
  stolen disks, malicious cameras, ONVIF SSRF, command replay, configuration
  and evidence tampering, update tampering, MITM, duplicate identity and log
  leakage.
- Each threat has its current mitigation and an explicit gap/owner.

## S2 — Unique secrets

**IMPLEMENTED**

- **UNIQUE PER-DEVICE DEVICE CREDENTIAL: YES.** `GenerateCredential` uses
  32 bytes from `crypto/rand`; the SaaS receives only
  `sha256(device_credential)` during enrollment.
- **UNIQUE LOCAL CAMERA MASTER KEY: YES.** `camera_master.key` is generated
  locally from 32 random bytes and is not derived from the device credential.
- **ENROLLMENT TOKEN ≠ DEVICE CREDENTIAL: YES.** The enrollment token is a
  SaaS bootstrap secret; the Edge generates its operational credential locally.
- Identity is a random UUIDv4 identifier, not a secret.
- No global shared production secret, default credential, hardcoded production
  secret or deterministic credential derivation was found in the audited
  credential paths.
- Factory reset removes device/camera secret state, including
  `camera_credentials.json` and `camera_master.key`.

## S3 — TLS and transport policy

**IMPLEMENTED**

- `internal/transport.Client` is the shared Edge-to-SaaS HTTP client.
- Enrollment, heartbeat, frames, local events, remote config, control and
  camera-credential synchronization use this transport path.
- HTTPS is required by default. `GEOCAM_ALLOW_INSECURE_HTTP` is an explicit
  development-only escape for `http://`; it does not weaken HTTPS certificate
  verification.
- No `InsecureSkipVerify`, certificate pinning, private CA or mTLS is claimed.
- Go's normal hostname/certificate verification and standard proxy environment
  behavior are retained.
- ONVIF/RTSP traffic is a separate CCTV-LAN boundary; SaaS TLS does not claim
  to protect it.

## S4 — Local data protection

| Data | Classification | Current protection | Status |
|---|---|---|---|
| `identity.json` | Non-secret identifier plus metadata | Data directory `0700`, file written atomically with `0600` | IMPLEMENTED |
| `credentials.json` | Device credential and enrollment metadata | Data directory `0700`, atomic file write with `0600` | PARTIAL — credential is not encrypted at rest |
| `camera_credentials.json` | Camera usernames/passwords | AES-256-GCM with fresh nonce per value, atomic `0600` file | IMPLEMENTED, subject to key protection |
| `camera_master.key` | Local AES key | 32 random bytes, atomic `0600` file | PARTIAL — key is stored beside ciphertext |
| `remote_config_state.json` | Applied configuration and lifecycle state | Atomic `0600` file under `0700` data directory | IMPLEMENTED for local permissions/integrity |
| Control ledger | Command state/outcomes | Atomic `0600` file under `0700` data directory | IMPLEMENTED for local permissions/integrity |
| Events/evidence/buffers | Local operational data | Bounded stores, atomic writes and restrictive directory/file modes | PARTIAL — no immutable external notarization |
| Release artifacts | Executable/update artifacts | Versioned releases, architecture checks, checksum verification and rollback | PARTIAL — no signed-release verification |

**DEVICE CREDENTIAL AT-REST ENCRYPTION: NOT ESTABLISHED / REQUIRES
KEY-MANAGEMENT DECISION.** The repository does not invent a TPM, HSM, KMS,
Vault, OS keychain or new master secret merely to make this table appear
complete. Encrypting `credentials.json` without a separate trust root would
not provide meaningful stolen-disk protection.

## Explicit exclusions

- No HSM, TPM, KMS, Vault, private PKI, mTLS or hardware root of trust is
  implemented or implied.
- No claim is made that Unix permissions protect against root, a compromised
  host, physical disk theft or a malicious operator.
- No claim is made that TLS protects RTSP/ONVIF traffic on the CCTV LAN.
