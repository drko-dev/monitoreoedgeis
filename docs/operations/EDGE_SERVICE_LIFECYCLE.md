# Edge background service lifecycle

This guide covers the local OS service wrapper for the standalone Edge agent.
It does not change the SaaS lifecycle or the Linux appliance package contract.

## Before installing

1. Configure the existing Edge in `geocam-edge`'s persistent operator config
   file. The default is `<OS user config directory>/geocam-edge/edge.env`; set
   `GEOCAM_CONFIG_FILE` to select another path.
2. Persist at least `GEOCAM_DATA_DIR`, `GEOCAM_SAAS_URL`,
   `GEOCAM_PROCESSING_MODE=cloud`, and
   `GEOCAM_VIDEO_PIPELINE_ENABLED=true`. Other non-secret `GEOCAM_*` settings
   are allowed by the config parser. Enrollment tokens and device/camera
   credentials are not supported in this file.
3. Run `geocam-edge config`, `geocam-edge identity`, and `geocam-edge saas
   check`. The existing `identity.json` and matching enrolled credential must
   authenticate successfully. The installer will not create an identity,
   enroll a device, or change credentials.
4. Run `geocam-edge service install`, then `geocam-edge service start` and
   `geocam-edge service status`.

Shell environment variables override values from the persistent config file.
For service installation, the required effective values must also match the
persisted file so a later login or reboot cannot silently select another data
directory, SaaS endpoint, or processing profile.

## Platform behavior

| Platform | Service manager | Start behavior | Recovery |
| --- | --- | --- | --- |
| Linux appliance | Existing `geocam-edge.service` systemd unit | `install` enables the package-installed unit; use `start` separately | Existing `Restart=on-failure`, `WatchdogSec`, and start-rate limits |
| macOS | Per-user LaunchAgent | Runs at user login; `start` loads and kicks the agent | launchd restarts unexpected exits with throttling; an in-process bounded supervisor retries startup failures |
| Windows | Windows Service Control Manager | Automatic service registration; use `start` to start immediately | SCM recovery actions restart failures with increasing delays and a bounded recovery window; an in-process bounded supervisor retries startup failures |

The macOS and Windows service entrypoints acquire an OS lock scoped to the
canonical data directory. Only one process may operate on that directory at a
time; another data directory can run independently. The OS releases the lock
after a crash, so the persistent lock file is not a stale-process blocker.

The in-process supervisor uses increasing delays and pauses for five minutes
after five consecutive failed starts. A failed camera, stream, or SaaS request
does not restart the whole agent: their existing component-level reconnect and
backoff loops remain responsible for recovery. The service watchdog checks only
the local process liveness surface, not camera or SaaS availability.

## Health signals

- `/healthz` reports whether the process can answer its liveness probe.
- `/readyz` reports agent process readiness.
- `/operationalz` reports whether configured camera/video targets are usable.
- `/status` includes camera-target and pipeline counts/reasons.
- `geocam-edge check` reports both process and operational state and exits
  unsuccessfully when the process is not READY or operational state is
  `WAITING`/`DEGRADED`.

An offline camera is not, by itself, a reason to restart an otherwise healthy
agent. Use `geocam-edge check` and `geocam-edge service logs` to distinguish a
process failure from unavailable camera credentials, discovery, or streams.

## Lifecycle commands

```text
geocam-edge service install
geocam-edge service start
geocam-edge service status
geocam-edge service logs
geocam-edge service restart
geocam-edge service stop
geocam-edge service uninstall
```

Uninstall removes a macOS LaunchAgent or Windows service registration. On Linux
it disables the package-owned systemd unit without removing its unit file.
None of these actions deletes the data directory, identity, credentials,
persistent config, or logs. For recovery steps, see
`docs/operations/EDGE_RECOVERY_RUNBOOK.md`.

## Validation boundary

Unit tests and cross-compilation do not prove behavior under a real LaunchAgent,
Windows SCM, or systemd PID 1. Record live reboot, recovery-action, and hardware
results in `docs/product/PHYSICAL_VALIDATION_REGISTER.md`; do not mark them
validated based on compilation alone.
