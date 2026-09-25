# Edge service recovery runbook

Use this sequence when the Edge service is stopped, repeatedly restarting, or
ready as a process but not operational for its cameras.

Linux amd64/arm64 is the supported product target. macOS LaunchAgent recovery is
for development/validation only. Native Windows service installation is
disabled; use the foreground `geocam-edge run` command there.

## 1. Capture state without changing it

```text
geocam-edge config
geocam-edge identity
geocam-edge check
geocam-edge service status
geocam-edge service logs
geocam-edge saas check
```

Keep output local; do not paste credentials, enrollment tokens, Authorization
headers, or complete environment dumps into a ticket. Record timestamps, the
reported Edge/device identifiers, process and operational states, camera and
pipeline counts, and sanitized error categories.

## 2. Distinguish process failure from camera failure

- If service status is stopped, inspect service logs and then use
  `geocam-edge service start` after confirming the selected persistent config
  and existing identity are correct.
- If `/healthz` works but `geocam-edge check` reports `WAITING` or `DEGRADED`,
  the process is alive; investigate its operational reasons instead of killing
  it repeatedly.
- If the camera requires authentication, verify that SaaS authentication and
  camera-credential synchronization succeed before changing any local state.
- If SaaS authentication is rejected, stop before enrollment, re-enrollment,
  credential rotation, factory reset, or identity replacement. Obtain an
  authorized recovery token or operator direction for the existing Edge first.

## 3. Recover a wedged process

Use the OS service manager through `geocam-edge service restart`. Do not start a
second `geocam-edge run` against the same data directory: the OS-owned instance
lock will reject it. Do not delete `.geocam-edge.lock`; it is a persistent file
whose kernel lock, not its contents, indicates ownership, and the OS releases
that lock when the process exits.

If the service repeatedly exits, bounded supervisor/OS recovery delays are
intentional. Preserve logs and failure timestamps, correct the underlying
configuration or access issue, then start the service once. Do not disable
restart limits as a first response.

## 4. Verify recovery

After the service starts, verify all of the following:

1. `geocam-edge service status` reports running.
2. `geocam-edge check` reports process `READY` and operational `READY` for the
   configured camera workload.
3. Camera targets are present and pipeline counters advance over time; a
   reachable HTTP endpoint alone does not prove that video is flowing.
4. `geocam-edge saas check` authenticates the same existing Edge identity.

Record whether each check is software-tested, validated on this host, validated
with a physical camera, or still unvalidated. Never claim physical recovery
from a green process-health check alone.

## Protected state

Do not delete or rewrite `identity.json`, `credentials.json`, camera credential
stores, or the Edge data directory to clear a service problem. Uninstalling the
service preserves the data and logs. A factory reset is a separate destructive
operation requiring explicit operator intent and is not a service-recovery
step.
