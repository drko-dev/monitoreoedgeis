# GEO CAM Edge — Least Privilege (Hito S, S10)

Scope: audit of `deploy/appliance/systemd/geocam-edge.service.in`,
`install.sh`, filesystem permissions, the service account, subprocesses,
and network needs. Every privilege the running service has is documented
below with why it exists — no capability was added for convenience.

## Runtime does not require root

Confirmed: `install.sh` creates and the systemd unit runs as a dedicated
non-root service account (`User=@GEOCAM_SERVICE_USER@`,
`Group=@GEOCAM_SERVICE_GROUP@`). Privileged operations (`useradd`,
`chown` to the system user, `systemctl`) happen once, at install/update
time, run by whoever installs the appliance — never by the running
`geocam-edge.service` process itself.

## systemd hardening already present (Hito P, unchanged here)

| Directive | Effect |
|---|---|
| `NoNewPrivileges=true` | The process (and anything it `exec`s, including the `ffmpeg` subprocess) can never gain privileges beyond what it starts with — no `setuid` escalation path. |
| `ProtectSystem=strict` | The entire filesystem is read-only for this process except the paths explicitly listed in `ReadWritePaths`. |
| `ProtectHome=true` | No access to `/home`, `/root`, `/run/user`. Irrelevant to this workload's function, cheap to keep. |
| `ReadWritePaths=@GEOCAM_DATA_DIR_PLACEHOLDER@` | The **only** writable path is `GEOCAM_DATA_DIR`. |
| `PrivateTmp=true` | A private `/tmp`, not shared with other services on the host. |

**Key finding, worth stating explicitly**: `install.sh` does
`chown -R "$GEOCAM_SERVICE_USER:$GEOCAM_SERVICE_GROUP" "$DATA_DIR" "$PREFIX"`
— the service user owns `$PREFIX` too (where `releases/`, the `current`
symlink, and the `geocam-edge` binary itself live), because `update.sh`
needs to write there when installing a new release. **This Unix-level
ownership does not translate into write access for the *running*
service**: `ProtectSystem=strict` + the `ReadWritePaths` allowlist above
enforce a mount-namespace-level read-only view of everything outside
`GEOCAM_DATA_DIR`, regardless of file ownership. A compromised
`geocam-edge.service` process cannot rewrite its own binary or release
directory for persistence — the systemd sandbox blocks it even though
the Unix permission bits alone would not.

## Hardening added in this hito

Audited for actual, verified compatibility (not guessed) before adding:

| Directive | Why it is safe |
|---|---|
| `CapabilityBoundingSet=` (empty) | Confirmed: the Go agent does plain TCP/HTTP networking (`net.Dial`, `http.Client`) and the ffmpeg subprocess does software-only decode — no `hwaccel`/`vaapi`/`v4l2`/`/dev/dri`/`/dev/video*` reference anywhere in this repo (`internal/processing`, `deploy/appliance/scripts/build-ffmpeg-static.sh`). Neither needs any Linux capability (no raw sockets, no low-port bind — `GEOCAM_HEALTH_ADDR` defaults to `127.0.0.1:8091`, above 1024). |
| `PrivateDevices=true` | Same finding: no code path opens a physical device node. `PrivateDevices` still allows the always-present pseudo-devices (`/dev/null`, `/dev/zero`, `/dev/random`, `/dev/urandom`), which is all any Go process needs. **Thermal reads are unaffected**: `internal/platform`'s temperature reading is a plain file read under `/sys/class/thermal/`, a different filesystem tree from `/dev` — `PrivateDevices` does not touch `/sys`. |

Both changes are covered by
`deploy/appliance/appliance_test.go`'s `TestSystemdUnitTemplateStructure`.

### What was deliberately NOT added

Full systemd hardening has more knobs
(`ProtectKernelTunables`, `ProtectKernelModules`, `ProtectControlGroups`,
`RestrictNamespaces`, `MemoryDenyWriteExecute`, `SystemCallFilter`, …).
None of those were added in this hito: this dev sandbox cannot exercise
them against real Raspberry Pi/industrial-appliance hardware (thermal
sysfs paths, USB/serial peripherals a future hardware profile might need,
etc.), and the task's own instruction is explicit — **do not guess
hardware compatibility**. `CapabilityBoundingSet=`/`PrivateDevices=true`
were added because this audit could concretely verify, by reading every
call site, that nothing in this repo needs a capability or a device node.
The remaining directives would need the same level of verification
against real hardware before being added, which is future work, not
guesswork done here.

## Control plane: allowlisted, no free-form exec

Audited: `internal/control/module.go`. Command dispatch is a fixed `switch`
over `cmd.CommandType` with exactly four cases
(`request_status`, `rediscovery`, `reload_config`, `restart_video_pipeline`
— the last one always returns `UNSUPPORTED`) plus a `default` that fails
closed with `UNKNOWN_COMMAND`. **There is no `os/exec`, shell invocation,
or free-form command execution anywhere in `internal/control` or
`internal/remoteconfig`** — confirmed by inspection, not merely by
convention. A malicious or malformed `command_type` from the SaaS cannot
execute arbitrary code; it can only fail with `UNKNOWN_COMMAND`.

## Filesystem permissions (install.sh)

| Path | Mode | Owner |
|---|---|---|
| Config dir | `0750` | `root:GEOCAM_SERVICE_GROUP` |
| `GEOCAM_DATA_DIR` | `0700` | service user |
| Env file (secrets) | `0640` | `root:GEOCAM_SERVICE_GROUP` |
| Release binary/scripts | `0755` | service user |

No `chmod 777`, no world-readable secret file, found anywhere in
`install.sh`/`lib.sh`. The env file (which can carry
`GEOCAM_SAAS_URL`/proxy config, never a raw credential — credentials live
only in `credentials.json` under `GEOCAM_DATA_DIR`, `0700`) is not
world-readable.
