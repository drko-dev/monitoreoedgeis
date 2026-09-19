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

| Directive | Why it is safe |
|---|---|
| `CapabilityBoundingSet=` (empty) | Confirmed: the Go agent does plain TCP/HTTP networking (`net.Dial`, `http.Client`) and the ffmpeg subprocess does software-only decode — no `hwaccel`/`vaapi`/`v4l2`/`/dev/dri`/`/dev/video*` reference anywhere in this repo (`internal/processing`, `deploy/appliance/scripts/build-ffmpeg-static.sh`). Neither needs any Linux capability (no raw sockets, no low-port bind — `GEOCAM_HEALTH_ADDR` defaults to `127.0.0.1:8091`, above 1024). This holds for the CUDA vision-worker profile too: the NVIDIA driver's userspace ioctl interface on `/dev/nvidia*` is gated by device-node permissions, not by a Linux capability — dropping the capability set does not affect it. |

Covered by `deploy/appliance/appliance_test.go`'s
`TestSystemdUnitTemplateStructure`.

### `PrivateDevices` — evaluated, deliberately NOT enabled globally

An earlier draft of this hito added `PrivateDevices=true`, reasoning that
"nothing in this repo references `/dev`." That reasoning was **wrong**
for the architecture this repo already implements, and the directive was
removed before this PR closed:

- Full Edge's vision worker (`deploy/vision-worker/backend.py`,
  `internal/vision/worker.go`) supports `GEOCAM_EDGE_YOLO_DEVICE=cpu` /
  `cuda` / `auto` (`internal/config.Config.EdgeYOLODevice`, Hito K/K5).
  `backend.py`'s `YOLOBackend` passes `device=self.device` straight into
  Ultralytics/PyTorch.
- **The CUDA/PyTorch runtime accesses host accelerator device nodes**
  (`/dev/nvidia0`, `/dev/nvidiactl`, `/dev/nvidia-uvm`, …) to talk to the
  GPU — this happens inside PyTorch's own native/CUDA driver layer, not
  in any Go or Python source this repo controls, which is exactly why
  grepping this repo for `/dev/nvidia` or `hwaccel` finds nothing even
  though the runtime dependency is real. Absence of a literal path string
  in this codebase is not evidence of absence of a device dependency.
- `PrivateDevices=true` masks `/dev` down to a handful of always-present
  pseudo-devices (`null`/`zero`/`random`/`urandom`) — it would have
  **broken GPU access for the `cuda` profile** while going completely
  unnoticed by every test in this repo, since none of them run against
  real accelerator hardware.
- **This unit is shared across every processing mode** (`cloud`,
  `hybrid`, `edge`) — there is one `geocam-edge.service.in`, not a
  separate CPU/GPU variant — so a global `PrivateDevices=true` cannot
  assume CPU-only just because that happens to be the default.

CPU-only deployments probably could run under `PrivateDevices=true`
without issue, but validating that split (and building the profile-aware
unit selection it would require) is real design work this hito does not
do. **No `DeviceAllow=` allowlist and no NVIDIA/Intel/NPU device list was
invented here either** — that would need real accelerator hardware to
verify against, which this dev sandbox does not have.
`deploy/appliance/appliance_test.go` now asserts the *absence* of
`PrivateDevices=true` from the template, specifically to prevent this
from being silently reintroduced.

### What else was deliberately NOT added

Full systemd hardening has more knobs
(`ProtectKernelTunables`, `ProtectKernelModules`, `ProtectControlGroups`,
`RestrictNamespaces`, `MemoryDenyWriteExecute`, `SystemCallFilter`, …).
None of those were added in this hito: this dev sandbox cannot exercise
them against real Raspberry Pi/industrial-appliance/GPU hardware, and the
task's own instruction is explicit — **do not guess hardware
compatibility**. Any future device-access or namespace-restriction policy
must be profile-aware (distinguish the CPU-only and CUDA processing
modes) and validated against real accelerator hardware before being
enabled — not designed or built in this hito.

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
