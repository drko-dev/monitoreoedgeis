# Device identity lifecycle audit (Hito S5–S8)

This is an audit of the existing Edge/SaaS lifecycle. It records effective
behavior and boundaries; it does not claim push revocation, hardware-backed
identity or bearer-token theft prevention.

## S5 — Revocation

- Edge-authenticated endpoints route through the shared SaaS
  `authenticate_edge_device` policy. This includes `/edge/me`, self-rotation,
  heartbeat, events, frames, local events/evidence, control and remote config;
  gateway discovery/transport also uses the same dependency.
- Only `active` devices authenticate. `suspended` and `revoked` devices are
  rejected on the next request; there is no invented real-time push channel.
- A revoked device cannot self-rotate because authentication fails before the
  rotation operation.
- A normal or old enrollment token cannot reactivate a revoked device.
  Reactivation requires the explicit admin allow-reenrollment flow, which
  creates a token pre-bound to the same device and consumes it atomically.
- Security-sensitive status changes, enrollment claims, reenrollment grants,
  and rotations call the existing `log_audit` system without secret fields.

## S6 — Rotation

- Edge self-rotation authenticates with the current credential and submits only
  the SHA-256 hash of a locally generated new credential.
- SaaS stores the previous hash only for the existing bounded grace window
  (five minutes in the current implementation). After expiry, the old key is
  rejected.
- Local Edge credential persistence is atomic; a failed save does not replace
  the working credential.
- The existing `rotation_id` makes a retry with the same ID and hash a no-op.
  Reusing the same ID with a different hash is rejected with `409` and does not
  change either stored hash.
- Admin rotation continues to expose the new raw key only in its designed
  one-time response. Raw keys/hashes are not written to audit details.
- **Mechanism:** IMPLEMENTED / TESTED. **Automatic rotation schedule:** NOT
  DEFINED; no schedule is invented here.

## S7 — Secure enrollment

- The Edge generates its device credential locally; SaaS receives only its
  SHA-256 hash.
- Enrollment request bodies are bounded and Pydantic models forbid extra fields.
- Enrollment tokens are stored hashed, have a real expiration, are revocable,
  and are consumed under a database row lock. Concurrent claims cannot create
  multiple devices; the same-device retry is idempotent only when its identity
  and hash match.
- Raw tokens are returned only at the existing designed creation/issue moment;
  they are not recoverable from public API reads and are not logged. Bootstrap
  avoids passing the token as a process argument.
- Explicit reenrollment is the only reactivation path for a revoked device.
  No QR or new enrollment protocol was added.

## S8 — Replay protection and limits

| Stateful operation | Existing protection | Classification |
|---|---|---|
| Enrollment claim | Hashed one-time token, expiry, row lock, same-device idempotent retry | IMPLEMENTED |
| Self credential rotation | `rotation_id`, current-credential auth, bounded previous-key grace; conflicting ID rejected | IMPLEMENTED for the current rotation record |
| Control command | Durable `command_id` ledger prevents re-execution after restart/retry | IMPLEMENTED |
| Event ingest | Event UUID/idempotency and transactional conflict handling | IMPLEMENTED where event UUID is supplied |
| Remote config | Monotonic version, durable state and explicit acknowledgement | IMPLEMENTED for version/conflict handling |

The rotation record currently retains the last `rotation_id`, not an unlimited
history. A replay of an old operation after later rotations remains a future
protocol-hardening consideration if the threat model requires durable replay
memory across the entire credential lifetime.

Idempotency is not anti-theft. A captured bearer credential could be reused
while valid; current mitigations are HTTPS, revocation and rotation.

## Audit-secret rules

Audit details may include action, device identifier, organization scope,
rotation identifier and safe result metadata. They must not include raw
credentials/tokens, full hashes, passwords or camera secrets.
