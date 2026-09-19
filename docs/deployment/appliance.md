# GEO CAM Edge — Linux Appliance Deployment

This document covers installing, operating and upgrading `geocam-edge` as a
native systemd service on a Linux appliance (physical or VM), **without**
Docker or Kubernetes. It is the productive counterpart to the existing
`deploy/helm/geocam-edge` chart, which remains a **local K3s development
setup only** — see [K3s dev deploy vs. this appliance](#k3s-dev-deploy-vs-this-appliance)
for the differences.

Everything here reflects what the code in this repository actually does.
Where something has not been validated on a real machine, it is called out
explicitly under [What has NOT been validated](#what-has-not-been-validated) —
this document does not claim a real installation happened if it didn't.

## Requirements

- Linux, amd64 or arm64 (the only architectures `make build-linux` produces).
- systemd as the init system.
- A `geocam-edge-<version>-linux-<arch>.tar.gz` artifact, built with
  `deploy/appliance/scripts/package.sh` (wraps `make build-linux`).
- `bash`, `tar`, `curl` (used by `wait-ready.sh`/`check`), `sha256sum` or
  `shasum` (for artifact checksum verification — optional but recommended).
- No Docker or Kubernetes at runtime. Docker is used only as a **build-time**
  tool (on the machine producing the package), to extract a static ffmpeg
  binary by reusing the existing root `Dockerfile`'s `ffmpeg-build` stage —
  see [ffmpeg](#ffmpeg).

## Layout

| Path | Purpose | Touched by |
|---|---|---|
| `/opt/geocam-edge/releases/<version>/` | One immutable directory per installed version (`geocam-edge`, optionally `ffmpeg`, `scripts/`) | install/update, never deleted except by `uninstall.sh` |
| `/opt/geocam-edge/current` | Symlink to the active `releases/<version>` — this is what systemd actually runs | install/update/rollback |
| `/opt/geocam-edge/current/scripts/bootstrap.sh` | First-boot zero-touch enrollment and initialization script | install/package |
| `/opt/geocam-edge/.previous` | Records the release to restore on `rollback.sh` | install/update |
| `/etc/geocam-edge/geocam-edge.env` | Non-secret configuration (systemd `EnvironmentFile`) | created once on first install, **never overwritten** afterward |
| `/var/lib/geocam-edge` | `GEOCAM_DATA_DIR`: `identity.json`, `credentials.json`, offline buffer state | the running agent only — install/update/uninstall never touch its contents (see [Data safety](#data-safety)) |
| `/etc/systemd/system/geocam-edge.service` | Main daemon systemd unit | refreshed on every install (not operator-editable state) |
| `/etc/systemd/system/geocam-edge-bootstrap.service` | First-boot oneshot unit (runs `bootstrap.sh` before `geocam-edge.service`) | refreshed on every install |

The service runs as a dedicated, unprivileged system user (`geocam-edge` by
default, no login shell, no home directory contents) — the same
"non-root by default" principle the existing distroless/nonroot
`Dockerfile` already applies for the K3s deploy.

## Installation (clean)

```sh
tar xzf geocam-edge-<version>-linux-<arch>.tar.gz
cd geocam-edge-<version>-linux-<arch>
sudo GEOCAM_VERSION=<version> ./scripts/install.sh ./geocam-edge ./ffmpeg   # ffmpeg arg optional
```

This creates the layout above, writes a default
`/etc/geocam-edge/geocam-edge.env` (edit it before enrolling — at minimum set
`GEOCAM_SAAS_URL`), installs and `systemctl enable`s the unit, but **does
not start it** — enroll first:

```sh
sudo systemctl daemon-reload   # already done by install.sh; harmless to repeat
echo "$ENROLLMENT_TOKEN" | sudo -u geocam-edge /opt/geocam-edge/current/geocam-edge enroll
sudo systemctl start geocam-edge
sudo systemctl status geocam-edge
/opt/geocam-edge/current/geocam-edge check          # or: curl http://127.0.0.1:8091/status
sudo geocam-edge saas check
```

Re-running `install.sh` (e.g. to reinstall the same version, or as the first
step of a manual upgrade) is safe: it never overwrites
`geocam-edge.env`, `identity.json`, or `credentials.json`.

## Configuration

See `deploy/appliance/config/geocam-edge.env.example` for the full,
commented list of every `GEOCAM_*` variable `internal/config.Load()` actually
reads, with its real default and validation range from that code — not
invented "recommended production values" for the settings that are still
open decisions upstream (notably the Cloud offline buffer size and bandwidth
caps, and `GEOCAM_PROCESSING_MODE=hybrid|edge`, which parse successfully but
have no functional implementation yet beyond `cloud`).

`GEOCAM_ENROLLMENT_TOKEN` is deliberately **not** a line in this file: the
`enroll` subcommand reads it from stdin or a one-shot environment variable at
enrollment time only (see `cmd/geocam-edge/main.go`), so it is never written
to disk in plaintext as part of the appliance's persistent config.

## Operating

```sh
sudo systemctl start geocam-edge
sudo systemctl stop geocam-edge
sudo systemctl restart geocam-edge
sudo systemctl status geocam-edge
sudo journalctl -u geocam-edge -f          # logs — stdout/journald only, no separate log files
curl http://127.0.0.1:8091/healthz         # process alive
curl http://127.0.0.1:8091/readyz          # 200 only once READY
curl http://127.0.0.1:8091/status          # JSON snapshot, no secrets
/opt/geocam-edge/current/geocam-edge check # same as /status, exits non-zero if not READY
```

`geocam-edge run`'s shutdown listens for `SIGINT`/`SIGTERM` via
`signal.NotifyContext` (`cmd/geocam-edge/main.go`) and shuts down in-process;
the unit's `KillSignal=SIGTERM` and `TimeoutStopSec=30` rely on that.

### Recovery after reboot

`geocam-edge.service` is enabled (`WantedBy=multi-user.target`) so it starts
automatically after a reboot once `network-online.target` is reached — no
manual step needed, as long as enrollment already happened (identity and
credentials persist in `/var/lib/geocam-edge`, which survives reboots and
is untouched by any of these scripts).

## Upgrade

```sh
sudo ./scripts/update.sh /path/to/geocam-edge-<new-version>-linux-<arch>.tar.gz
```

`update.sh`:
1. Verifies the artifact's checksum, if a sibling `.sha256` file is present
   (always the case for artifacts from `package.sh`).
2. Verifies the artifact's architecture matches the host — rejects a
   mismatch before touching anything installed.
3. Installs the new version into its own `/opt/geocam-edge/releases/<version>`
   (old versions are kept, never deleted here).
4. Records the current release for rollback, then atomically repoints
   `current` at the new one.
5. Restarts the service and polls `/readyz` (via `wait-ready.sh`).
6. **If verification fails, automatically runs `rollback.sh`** and exits
   non-zero — an upgrade never leaves the appliance on a version that failed
   its own health check.

There is no remote/OTA distribution mechanism — `update.sh` only ever
activates an artifact already present on local disk. Building and shipping
that artifact to the appliance (scp, USB, config management, a future OTA
channel) is a separate, not-yet-built concern.

## Rollback

```sh
sudo ./scripts/rollback.sh
```

Restores `current` to the release recorded before the last `install.sh`/
`update.sh` run, restarts the service, and verifies `/readyz`. Never touches
`GEOCAM_DATA_DIR`, `geocam-edge.env`, identity, or credentials. Fails loudly
(non-zero exit) if there is nothing recorded to roll back to, or if the
recorded release no longer exists on disk.

Typical flow end-to-end:

```
install → enroll → start → status (READY) → update → verify (auto) → [rollback if needed]
```

## Data safety

`GEOCAM_DATA_DIR` (`/var/lib/geocam-edge` by default) holds `identity.json`
and `credentials.json` (both written atomically with `0600` permissions by
`internal/identity`/`internal/credentials`, in `0700` directories) plus any
offline-buffer state. **None of install.sh/update.sh/rollback.sh ever delete
or overwrite anything under this directory.** `uninstall.sh` doesn't either,
unless you pass `--purge` *and* explicitly confirm (typing `yes` at a
prompt, or setting `GEOCAM_UNINSTALL_PURGE_CONFIRM=yes` for a scripted,
deliberate wipe). This is enforced by automated tests, not just a comment —
see `deploy/appliance/appliance_test.go`.

## Uninstallation

```sh
sudo ./scripts/uninstall.sh            # removes binaries, releases, systemd unit;
                                        # leaves /var/lib/geocam-edge and
                                        # /etc/geocam-edge/geocam-edge.env intact
sudo ./scripts/uninstall.sh --purge    # ALSO deletes data + config, after confirmation
```

## ffmpeg

The video pipeline (`GEOCAM_VIDEO_PIPELINE_ENABLED=true`) shells out to
`ffmpeg` as a subprocess (never linked/cgo — Hito H). For the appliance,
`deploy/appliance/scripts/build-ffmpeg-static.sh <amd64|arm64>` produces a
standalone static `ffmpeg` binary by **reusing, unmodified**, the exact
`ffmpeg-build` stage already reviewed and pinned in the repo root
`Dockerfile` for the K3s image (same pinned ffmpeg source + SHA256, same
`--disable-everything`/LGPLv2.1+-only `./configure` flags — Hito H's
licensing decision is not reopened, just extracted instead of shipped inside
a container layer). Docker is required on the **build** machine only for
this one step; the appliance itself never runs Docker. `package.sh` bundles
the resulting binary into the tarball automatically if
`dist/ffmpeg-linux-<arch>` exists when it runs.

## Appliance Image Preparation & First Boot (Q4)

Rather than maintaining a heavy custom Linux distribution from scratch, the
production appliance image is defined as a minimal, reproducible provisioning
layer on top of standard base Linux distributions (Debian 12 minimal, Ubuntu
24.04 Server, or Raspberry Pi OS Lite 64-bit):

1. **Base OS Flashing**: Flash the standard distribution image to storage
   (eMMC, SD card, or NVMe SSD).
2. **Appliance Provisioning**:
   - Extract the pre-packaged release: `tar xzf geocam-edge-<version>-linux-<arch>.tar.gz`.
     > [!NOTE]
     > El appliance tarball completo (que incluye el binario `geocam-edge`,
     > `scripts/`, `systemd/`, `config/`, `VERSION` y `ARCH`) es el generado por
     > `deploy/appliance/scripts/package.sh`. No debe confundirse con el tarball de
     > GitHub Release P6 actual (`.github/workflows/release.yml`), que hoy empaqueta
     > únicamente el binario suelto.
   - Execute installation: `sudo GEOCAM_VERSION=<version> ./scripts/install.sh ./geocam-edge ./ffmpeg`.
   - This writes:
     - Versioned binaries and helper scripts in `/opt/geocam-edge/releases/<version>/`.
     - Active symlink at `/opt/geocam-edge/current`.
     - Systemd units: `/etc/systemd/system/geocam-edge.service` and `/etc/systemd/system/geocam-edge-bootstrap.service`.
     - Config template `/etc/geocam-edge/geocam-edge.env` with `GEOCAM_SAAS_URL` configured for the target environment.
3. **First-Boot Lifecycle (`bootstrap.sh`)**:
   - `geocam-edge-bootstrap.service` is a `Type=oneshot` systemd unit ordered `Before=geocam-edge.service`.
   - On first boot, systemd executes `/opt/geocam-edge/current/scripts/bootstrap.sh`.
   - Under systemd service context (`INVOCATION_ID`), `bootstrap.sh` claims enrollment if a seed token is found, removes the seed file best-effort, and exits cleanly (exit 0) **without** attempting to synchronously start `geocam-edge.service` or block on readiness. Systemd then proceeds to start `geocam-edge.service` naturally, respecting the `Before=` dependency.
   - If no token is discovered, the appliance remains cleanly installed and awaits out-of-band or manual enrollment.
   - On subsequent boots, `bootstrap.sh` detects existing credentials and exits immediately without side effects.

## Hardware, Ethernet & Power Specifications (Q5)

The residential appliance baseline is designed for unattended operation in home
or small office networks:

- **Network Connectivity**:
  - **Wired Ethernet is the primary supported path** (e.g. `eth0`, `enp*`) via DHCP.
  - *Rationale*: CCTV streaming pipelines require deterministic throughput and
    low jitter. Furthermore, local camera discovery relies on ONVIF WS-Discovery
    multicast (`239.255.255.250:3702` UDP), which is frequently filtered,
    dropped, or heavily delayed on residential Wi-Fi access points.
  - Wi-Fi is deliberately **not** the primary supported appliance path.
- **Power Specifications**:
  - **Alimentación según especificación del hardware seleccionado**: utilizar siempre
    una fuente de alimentación adecuada al fabricante y modelo implementado.
  - *Ejemplo específico de modelo (no requisito universal GEO CAM)*:
    - Una SBC tipo Raspberry Pi 5 típicamente requiere una fuente oficial USB-C de
      5V / 5.0A (27W) para evitar throttling bajo carga de decodificación o inferencia local.
    - Un mini-PC x86_64 (e.g. Intel N100 / AMD Ryzen embedded) típicamente utiliza una
      fuente externa de 12V–19V DC según especificación del fabricante de la placa.
- **Power over Ethernet (PoE)**:
  - **No se asume ni requiere hardware PoE integrado en placa.**
  - PoE soportado **estrictamente mediante solución o adaptador externo compatible si corresponde**
    (e.g. un splitter Gigabit externo IEEE 802.3af/at que derive datos a RJ45 y alimentación
    al puerto USB-C o jack barril correspondiente).
  - No asumir circuitos PoE propietarios integrados en el software.

## Zero-Touch Enrollment (Q6)

Zero-touch enrollment allows an appliance to be claimed and bound to a SaaS
tenant without manual SSH access or shell commands on the physical unit:

```
[First Boot]
     │
     ▼
[Edge has no identity/credentials]
     │
     ▼
[bootstrap.sh checks for ephemeral token]
   ├── /boot/geocam-enroll.token (or /boot/firmware/geocam-enroll.token)
   ├── /etc/geocam-edge/enroll.token
   ├── GEOCAM_ENROLLMENT_TOKEN (environment / cloud-init)
   └── /media/*/geocam-enroll.token (USB seed drive)
     │
     ▼
[Found token?] ─── No ───► [Log "awaiting enrollment" and exit 0]
     │
    Yes
     ▼
[Pipe token to: `geocam-edge enroll` via stdin]
  (Existing CLI: hashes token with SHA-256, claims device credential from SaaS)
     │
     ▼
[Persist identity.json & credentials.json in /var/lib/geocam-edge]
  (Mode 0600, owned by geocam-edge service user)
     │
     ▼
[Delete ephemeral seed file (best-effort)] ◄── Never persist token in plaintext!
     │
     ▼
[Exit 0 ──► systemd starts geocam-edge.service respecting Before=]
```

Key guarantees:
- **No new enrollment protocol**: Reuses the exact existing `geocam-edge enroll`
  command and `POST /api/v1/edge/enroll` SaaS endpoint (SHA-256 token exchange).
  The token is supplied via stdin, never in command line arguments (`--token` was removed from bootstrap).
- **Ephemeral token handling**: If read from `/boot/geocam-enroll.token` or disk,
  the seed file must be treated as a secret while present, and is deleted
  best-effort (`shred -u` or `rm -f`) immediately after enrollment. In flash
  storage (eMMC, SD card) wear-leveling prevents cryptographically guaranteed
  physical erasure, so the seed file must be treated with appropriate confidentiality
  while it exists. The token is never written to logs, systemd units, or persistent configuration.
- **FAT32 Boot Partition Friendly**: Placing `geocam-enroll.token` on `/boot` or
  `/boot/firmware` allows a field technician or distributor to drop an enrollment
  token onto an SD card/USB drive from Windows, macOS, or Linux without ext4 tools.

## Camera Discovery Integration (Q7)

Once enrolled and started, the appliance discovers local cameras automatically
by **reusing the existing ONVIF WS-Discovery engine** (`internal/discovery`):

1. **Automatic Background Scan**:
   - `discovery.Module` is started by the daemon when credentials are present.
   - After `InitialScanDelay` (5 seconds), it sends WS-Discovery multicast probes
     across private Ethernet interfaces.
   - Discovered devices (ONVIF endpoints, RTSP URLs, hardware models) are saved
     to local inventory (`/var/lib/geocam-edge/discovery-inventory.json`).
   - If SaaS connectivity is active, pending discovery runs are claimed and
     reported via `ReportDiscoveryRun`.
2. **On-Demand Operator Verification**:
   - Immediate discovery scan can be triggered via CLI:
     ```sh
     sudo -u geocam-edge /opt/geocam-edge/current/geocam-edge discovery scan
     ```
   - Or during bootstrap using:
     ```sh
     sudo /opt/geocam-edge/current/scripts/bootstrap.sh --scan
     ```
3. **Zero Redundancy**:
   - No second network scanner or secondary protocol was created.
   - Discovery strictly reuses `internal/discovery/wsdiscovery` and
     `internal/transport/discovery.go`.

## K3s dev deploy vs. this appliance

| | `deploy/helm/geocam-edge` (existing) | `deploy/appliance/` (this) |
|---|---|---|
| Purpose | Local K3s development only | Productive install on bare Linux |
| Runtime | Docker/containerd image, K3s pod | Native systemd service |
| Process isolation | Container (distroless nonroot) | Dedicated unprivileged system user |
| Upgrade | `helm upgrade` | `update.sh` (checksum + arch validated, auto-rollback on failed health check) |
| ffmpeg | Baked into the OCI image | Static binary produced by the same build recipe, shipped alongside the binary |

Nothing under `deploy/helm/` was changed by this work.

## Troubleshooting

- **`systemctl status` shows `activating` forever / never `active`**: check
  `journalctl -u geocam-edge` for a configuration error from `config.Load()`
  (all validation errors are logged to stderr, which lands in journald).
- **`/readyz` returns 503**: the agent is up but not `READY` — usually means
  enrollment hasn't happened yet, or the SaaS heartbeat is failing. Run
  `geocam-edge saas check`.
- **`update.sh` says architecture mismatch**: the artifact was built for the
  wrong `GOARCH` — rebuild/repackage for the host's actual architecture
  (`uname -m`).
- **`rollback.sh` says "nothing to roll back to"**: no prior `current` target
  was ever recorded — this is expected right after a very first install.

## What has NOT been validated

This work was done in a sandbox with no Linux/systemd machine or VM and no
Docker daemon available (see the repository's own constraints for this
change). Concretely, **not executed for real**:

- `useradd`/`groupadd`/`systemctl enable|start|restart` against a real
  systemd — the install/update/rollback scripts detect that and skip those
  steps when not on a real Linux target (see `scripts/lib.sh`), so this
  entire path has only been exercised as "skipped, logged as skipped".
- `systemd-analyze verify` against the generated unit file (only a static
  content/structure check exists, in `appliance_test.go`).
- An actual running `geocam-edge` process receiving `SIGTERM` from systemd
  and shutting down within `TimeoutStopSec=30` — the code path
  (`signal.NotifyContext(SIGINT, SIGTERM)` in `cmd/geocam-edge/main.go`) was
  read and confirmed to exist, not observed running.
- `build-ffmpeg-static.sh` itself (no Docker daemon in this sandbox) — the
  Dockerfile stage it invokes is unchanged and was already part of the
  reviewed Hito H build.
- Nothing was installed on any real or virtual machine, and no production
  infrastructure was touched.

What **was** validated: `deploy/appliance/appliance_test.go` runs the actual
bash scripts (install/update/rollback/uninstall) against a throwaway staged
root, covering idempotency, data/config preservation, update→rollback
round-trips, rejection of bad architecture/checksum artifacts, and absence
of secrets in script output. Plus the full repo verification suite (`go test
./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`, `make
build-linux`), all passing.
