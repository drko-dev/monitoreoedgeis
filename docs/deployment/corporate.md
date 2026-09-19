# GEO CAM Edge — Corporate Deployment Profiles (Hito R)

This document defines the supported **software deployment profiles** for Hito R:

1. a standard Linux VM;
2. an existing physical Linux server;
3. a dedicated industrial Linux appliance.

All three profiles reuse the Hito P/Q native appliance path. There is no second
installer and no Docker/Kubernetes runtime requirement.

## What is actually supported by the repository

- Target operating system: Linux with `systemd`.
- Target architectures: `amd64` and `arm64` only. This is enforced by the
  build targets and checked by `install.sh` before activation.
- Runtime artifact: the per-architecture tarball produced by
  `deploy/appliance/scripts/package.sh`.
- Persistent state: `GEOCAM_DATA_DIR` (default `/var/lib/geocam-edge`). It is
  outside the versioned release tree and is preserved by install, update and
  rollback.
- Service lifecycle: the existing `geocam-edge.service` systemd unit.
- Health: local `/healthz`, `/readyz` and `/status` endpoints. The unit starts
  after `network-online.target` and restarts on failure.
- Upgrade safety: the existing versioned release layout, checksum validation,
  atomic `current` symlink switch and readiness-based rollback.
- Enrollment: the existing bootstrap/enrollment flow. Enrollment credentials
  are not placed in the persistent environment file.

These are code/install-path facts. They do not constitute validation on a real
corporate target.

## Common preflight

Run these checks on the target before installation. They inspect local facts and
do not change networking, firewall rules, disks or unrelated services:

```sh
set -eu
[ "$(uname -s)" = Linux ]
case "$(uname -m)" in x86_64|aarch64|arm64) ;; *) exit 1 ;; esac
command -v bash tar curl file
command -v sha256sum || command -v shasum
command -v systemctl
systemctl --version
```

Also verify, using the target's normal operations process, that:

- the artifact architecture matches the guest/host architecture;
- the release, config and data paths have the required capacity and write
  permissions;
- the host can resolve and reach the SaaS URL over HTTPS;
- the host can reach the cameras over RTSP and, where discovery is required,
  ONVIF/WS-Discovery on the relevant CCTV network;
- reboot/start policy and persistent storage are available.

The installer performs its own binary architecture and path checks. It does
not replace this target/network preflight and does not modify the host's
firewall, routes, DNS, disks or unrelated services.

## Installation path shared by R1–R3

Build or obtain the architecture-specific package:

```sh
./deploy/appliance/scripts/package.sh <version> ./dist
```

The package contains the existing `geocam-edge` binary, helper scripts, systemd
template, configuration example, `VERSION` and `ARCH`. A static ffmpeg binary
is bundled only when the existing build step has placed
`dist/ffmpeg-linux-<arch>` there; otherwise ffmpeg must be provisioned
separately if the video pipeline is enabled.

On the target:

```sh
tar xzf geocam-edge-<version>-linux-<arch>.tar.gz
cd geocam-edge-<version>-linux-<arch>
sudo GEOCAM_VERSION=<version> ./scripts/install.sh ./geocam-edge [./ffmpeg]
```

Then configure `GEOCAM_SAAS_URL` in the existing environment file, enroll using
the existing bootstrap/manual flow, and start the service:

```sh
sudo systemctl start geocam-edge
sudo systemctl status geocam-edge
curl http://127.0.0.1:8091/healthz
curl http://127.0.0.1:8091/readyz
```

`install.sh` creates/updates only GEO CAM paths. It does not enable or rewrite
unrelated services. The existing `update.sh` and `rollback.sh` remain the only
upgrade and rollback mechanisms.

## R1 — Standard Linux VM

A VM is supported when its guest is Linux with `systemd` and an `amd64` or
`arm64` architecture produced by the real build path. No VMware, Hyper-V,
Proxmox or cloud provider is required by the code.

The VM operator supplies and validates:

- a persistent virtual disk for `GEOCAM_DATA_DIR`;
- a NIC with routing and DNS to the SaaS and CCTV networks;
- HTTPS egress to the SaaS;
- RTSP/ONVIF reachability to the cameras;
- reboot and automatic-start policy.

No CPU, RAM, storage or camera-count minimum is declared here because this
repository does not contain measurements for a corporate VM target.

**Status:** CODE/INSTALL PATH READY; DOCUMENTED; NOT VALIDATED ON A REAL
CORPORATE VM.

## R2 — Existing physical Linux server

The same package and `install.sh` run directly on an existing Linux server.
The server's existing operating system, storage, network, firewall and other
services remain under the operator's control.

GEO CAM uses only its documented paths:

- `/opt/geocam-edge` for versioned releases and the active symlink;
- `/etc/geocam-edge` for the non-secret environment file;
- `/var/lib/geocam-edge` for persistent device state;
- `/etc/systemd/system/geocam-edge*.service` for its units.

The installer does not configure firewall rules, routes, DNS, disks or unrelated
services. Validate coexistence, permissions and port policy with the server
owner before installation.

**Status:** CODE/INSTALL PATH READY; DOCUMENTED; NOT VALIDATED ON A REAL
CORPORATE SERVER.

## R3 — Industrial Linux appliance

An industrial appliance is treated as dedicated Linux hardware running the same
GEO CAM package and systemd service. No brand, model, temperature range, IP
rating, fanless certification, voltage, MTBF or accelerator capability is
certified by this repository.

Only these software requirements are claimed:

- Linux with systemd;
- `amd64` or `arm64`;
- persistent storage for the documented GEO CAM paths;
- network reachability to SaaS and CCTV;
- the existing install/update/rollback path.

Hardware durability, environmental behavior, power design, thermal behavior and
performance remain outside the evidence available here.

**Status:** CODE/INSTALL PATH READY; DOCUMENTED; **HARDWARE NOT VALIDATED**.

## Release and upgrade notes

The GitHub release workflow currently publishes architecture-specific binaries.
The full appliance tarball used by `install.sh` is produced by the existing
`deploy/appliance/scripts/package.sh` flow; packaging and transport to the
corporate target remain an operational responsibility. `update.sh` expects a
local artifact and its sibling checksum, then preserves previous releases and
rolls back automatically when readiness verification fails.

## Explicit gaps

- No real corporate VM, physical server or industrial appliance validation was
  performed for this closure.
- No hardware model, sizing number or camera-count claim is made.
- No automated firewall, DNS, routing or disk provisioning exists or is added.
- No remote/OTA distribution mechanism is implied.
