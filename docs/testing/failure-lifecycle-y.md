# Hito Y — connectivity and camera resilience (Y3–Y5)

Scope: SaaS offline (Y3), general Internet loss (Y4), and camera offline
(Y5). This matrix closes the real resilience gap left by Hito W without
adding a global Internet detector, a new queue, or a second retry mechanism.

## Behaviour matrix

| Failure | While it lasts | Persistence | Backoff | Recovery | Limitation |
| --- | --- | --- | --- | --- | --- |
| SaaS connection refused, timeout, DNS/transport error | Heartbeat/control/upload callers report a transient transport class; local processing continues | Identity, enrollment credentials, durable event/evidence records and cloud frame buffer are retained | Existing bounded exponential backoff; `Retry-After` is honoured where supplied | Existing loops resume automatically and clear transient status after success | No global Internet state is inferred from one failed SaaS request |
| SaaS 5xx | Degraded heartbeat/replay status; no reenrollment | Same as above | Same bounded transient backoff | Automatic on the next successful request | 5xx is not treated as credential loss |
| General Internet loss | Each network-dependent component fails through its own transport boundary: heartbeat/control, frame/event/evidence upload, discovery/ONVIF requests, and OTA checks when invoked | Local queues and camera configuration remain local; no identity rewrite | Component-specific existing backoff; no retry storm or duplicate worker | Components reconnect/replay through their existing loops | There is intentionally no synthetic “Internet offline” detector |
| Camera TCP refused | Camera stream `degraded`; supervisor remains active | Camera target and credentials remain configured | RTSP supervisor exponential reconnect backoff | Reaches `online` when the camera returns | `offline` remains the stopped-supervisor state |
| RTSP EOF/peer close | `degraded`, reconnect count increments, no timeout count | Same as above | Existing reconnect backoff | Automatic reconnect | No packet is fabricated during the gap |
| RTSP silence / packet timeout | `degraded`, timeout and stall counters increment | Same as above | Existing reconnect backoff | Automatic reconnect after packets resume | Silence is distinct from TCP refusal |
| RTSP authentication rejection | `auth_failed`, sanitized 401 error; credentials are not logged or rewritten | Camera credentials remain unchanged | Existing bounded reconnect backoff, not a tight loop | `Manager.SetTargets` with corrected credentials replaces the supervisor and returns it to `online` | Automatic recovery requires the credential provider to deliver an updated target |
| ONVIF unavailable vs invalid credentials | Existing credential test classifies `UNREACHABLE` separately from `INVALID` | Camera credential store remains unchanged | Caller-controlled discovery cadence | Later discovery can recover without reenrollment | ONVIF and RTSP are independent paths when one is available and the other is not |

## Evidence

- SaaS outage and recovery: `internal/heartbeat/saas_outage_test.go`,
  `internal/agent/failure_lifecycle_test.go`, and
  `internal/edgebacklog/saas_outage_test.go`.
- Camera loss and lifecycle: `internal/rtsp/failure_lifecycle_test.go`.
- New Y5 regression: `TestY5_AuthFailureIsDistinctAndRecoversAfterCredentialUpdate`.
- ONVIF distinction: `internal/cameratest/credentials_failure_test.go` and
  `internal/discovery/onvif/wssecurity_test.go`.

The simulators bind only to loopback and use `httptest`, `t.TempDir`, and
test-local RTSP servers. No real camera or Internet is used in CI.
