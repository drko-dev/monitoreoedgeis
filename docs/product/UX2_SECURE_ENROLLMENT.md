# UX-2 — Secure Enrollment / Claim Wizard

**Status:** Implemented  
**Branch (SaaS):** `feature/ux2-edge-claim`  
**Branch (Edge):** `feature/ux2-secure-enrollment`  
**Milestone:** UX-2

## Overview

UX-2 implements the secure enrollment flow that allows a GEO CAM Edge device to register itself with GEO CAM SaaS using a one-time enrollment code — without storing or requiring SaaS admin credentials on the Edge machine.

## Architecture

```
 ┌──────────────────────┐          ┌────────────────────────────┐
 │   GEO CAM SaaS       │          │   GEO CAM Edge             │
 │                       │          │                            │
 │  Admin Panel          │          │  Wails Desktop App         │
 │  ┌─────────────────┐  │          │  ┌──────────────────────┐  │
 │  │ POST /api/v1/   │  │          │  │ EnrollmentWizard.tsx │  │
 │  │ edge/enrollment │  │          │  │ (React component)    │  │
 │  │ -codes          │  │          │  └──────────┬───────────┘  │
 │  │ → generates     │  │          │             │              │
 │  │   XXXX-XXXX     │  │          │             ▼              │
 │  │   Crockford B32 │  │          │  useEnrollment.ts          │
 │  └─────────────────┘  │          │  → validate locally        │
 │                       │          │  → call Go facade           │
 │  ┌─────────────────┐  │  HTTP    │             │              │
 │  │ POST /api/v1/   │◀─┼─────────┼─────────────┘              │
 │  │ edge/claim      │  │          │  ┌──────────────────────┐  │
 │  │ → normalize     │  │          │  │ enrollment.go        │  │
 │  │ → hash SHA-256  │──┼─────────┼─▶│ → SaaSEnrollmentProv │  │
 │  │ → match DB      │  │          │  │ → atomic credential  │  │
 │  │ → create device │  │          │  │   persistence        │  │
 │  │ → return edge_id│  │          │  │ → identity.json      │  │
 │  └─────────────────┘  │          │  └──────────────────────┘  │
 └──────────────────────┘          └────────────────────────────┘
```

## Enrollment Code Design

- **Format:** Crockford Base32 `XXXX-XXXX` (40 bits entropy)
- **Storage:** SHA-256 hash in `gateway_enrollments.token_hash`
- **Normalization:** Strips hyphens/spaces, uppercases, translates `O→0`, `I/L→1`
- **One-time use:** Code is consumed on successful claim
- **Anti-brute-force:** Max 5 attempts per code, then permanently locked

## SaaS Endpoints

### Admin: Generate Enrollment Code
```
POST /api/v1/edge/enrollment-codes
Authorization: Bearer <admin-token>

Response: { "code": "XXXX-XXXX", "enrollment_id": "..." }
```

### Public: Claim Device
```
POST /api/v1/edge/claim
Content-Type: application/json

{
  "code": "XXXX-XXXX",
  "edge_id": "uuid-v4",
  "credential_hash": "sha256-hex",
  "device_kind": "edge",
  "claim_request_id": "uuid-v4"
}

Response: { "device_id": "...", "organization_id": 1, "status": "active" }
```

### Idempotent Replay
If the same `claim_request_id` is sent again, the server returns the original result without side effects.

## Edge Implementation

### Go Backend (`internal/installer/enrollment.go`)

- `NormalizeEnrollmentCode()` / `ValidateEnrollmentCode()` — match SaaS normalization
- `EnrollmentProvider` interface — injectable for testing
- `SaaSEnrollmentProvider` — production HTTP client
- `Service.ClaimDevice()` — end-to-end flow:
  1. Validate code format locally
  2. Load or create identity (UUID v4)
  3. Generate credential (`edg_live_<base64>`)
  4. Hash credential (SHA-256)
  5. Call SaaS `/api/v1/edge/claim`
  6. Persist credential atomically (temp+rename+fsync)
  7. Reload and verify identity
  8. Transition installer state to `ENROLLED`

### Wails Binding (`cmd/geocam-edge-ui/app.go`)

- `App.ClaimDevice(req)` — exposed to React frontend via Wails runtime

### React Frontend

- `EnrollmentWizard.tsx` — code input with auto-formatting, device name, submit, error/success states
- `useEnrollment.ts` — custom hook managing enrollment lifecycle
- Integrated into `App.tsx` — shown when installer state is `NEEDS_ENROLLMENT` or `NEW`

## Error Handling

Every failure returns a structured `SafeError` with:
- `code` — machine-readable error type
- `safe_message` — user-displayable message (no secrets)
- `recoverable` — whether retry is possible

| Code | Meaning | Recoverable |
|------|---------|-------------|
| `INVALID_CODE` | Format validation failed | Yes |
| `EXPIRED_CODE` | Code past expiration | No |
| `ALREADY_USED` | Code already claimed | No |
| `RATE_LIMITED` | Too many attempts (5+ failures) | No |
| `NETWORK_ERROR` | Cannot reach SaaS | Yes |
| `SERVER_ERROR` | SaaS returned unexpected error | Yes |
| `EDGE_ID_CONFLICT` | Edge ID already registered | No |
| `PERSISTENCE_ERROR` | Failed to save credentials | Yes |

## Database Changes

Migration `081_edge_enrollment_codes.sql`:
- Adds `claim_request_id VARCHAR(128)` to `gateway_enrollments`
- Adds `attempt_count INTEGER DEFAULT 0` to `gateway_enrollments`
- Index on `claim_request_id`

## Test Coverage

### SaaS (`test_edge_claim_api.py` — 9 tests)
- Code creation and RBAC
- Claim success and idempotent replay
- Crockford normalization edge cases
- Anti-enumeration (wrong code returns 401)
- Edge ID conflict (409)
- Brute-force rate limiting (5 attempts)
- Audit trail contains no secrets

### Edge (`enrollment_test.go` — 3 test functions, 13+ sub-cases)
- `TestCrockfordNormalization` — 8 format/lookalike cases
- `TestClaimDeviceSuccessAndAtomicPersistence` — full flow with mock server
- `TestClaimDeviceErrors` — invalid format, 401, 409, 429

### Frontend
- TypeScript compilation: clean (`tsc --noEmit`)
- Vite production build: clean (39 modules)

## Security Considerations

1. **No admin credentials on Edge** — only a one-time code travels to the device
2. **Code is hashed** — only SHA-256 hash stored in DB, raw code never persisted
3. **Credential never leaves Edge** — only the SHA-256 hash is sent to SaaS
4. **Anti-brute-force** — server locks code after 5 failed attempts
5. **Atomic persistence** — credential written via temp+rename+fsync (0600 perms)
6. **SafeError sanitization** — no internal details leak to UI

## What's NOT in UX-2

- Cloud / Hybrid / Full Edge mode selector (→ UX-3)
- Camera onboarding (→ UX-3+)
- DVR/NVR support (→ separate milestone)
- Service installation (→ UX-3+)
- Multiplataform release (→ UX-3+)
- Production deploy
