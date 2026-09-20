# Edge Install From Scratch

> From a clean Linux host to a verifiable, operational Edge appliance. Every
> path, flag and permission below is taken directly from
> `deploy/appliance/scripts/*.sh` and `deploy/appliance/systemd/*.in` — nothing
> here is inferred from a different project's conventions.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`.

## Supported architectures

`linux/amd64`, `linux/arm64` only (`deploy/appliance/scripts/lib.sh:host_arch`
dies on anything else). Binaries are architecture-checked twice before
activation: once via the ARCH sidecar file the packaged tarball ships, once by
inspecting the real binary with `file`/`readelf` — a mismatch on either check
is fail-closed and installs nothing (`install.sh`).

## 1. Get an artifact

Either download a signed release tarball (see
`docs/runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md`) or build one locally with
`deploy/appliance/scripts/package.sh`. You need the `geocam-edge` binary and,
optionally, the static `ffmpeg` binary for your architecture.

## 2. Install

```bash
sudo deploy/appliance/scripts/install.sh <path-to-geocam-edge-binary> [path-to-ffmpeg-binary]
```

Safe to re-run — it never overwrites identity, credentials, `GEOCAM_DATA_DIR`
contents, or an existing `geocam-edge.env`. What it does, in order:

1. **Service account** (real Linux targets only): creates system group/user
   `geocam-edge` (`GEOCAM_SERVICE_USER`/`GEOCAM_SERVICE_GROUP`), no login
   shell, no home directory contents.
2. **Directories** (all honor `GEOCAM_INSTALL_ROOT` as a staging prefix, real
   installs leave it empty):
   - `/opt/geocam-edge/releases/<version>/` — this release's binaries/scripts,
     `chmod 0755`
   - `/opt/geocam-edge/current` — symlink to the active release, atomically
     swapped
   - `/etc/geocam-edge/` — `chmod 0750`, holds `geocam-edge.env`
   - `/var/lib/geocam-edge/` (`GEOCAM_DATA_DIR`) — `chmod 0700`, identity/
     credentials/backlog/evidence live here
   - `/usr/libexec/geocam-edge/ota-updater.sh` — root-only OTA boundary
   - `/run/geocam-edge/ota-staging/` — `chmod 0700`
3. **Version resolution**: `GEOCAM_VERSION` env var → sibling `VERSION` file
   next to the binary → (dev-only fallback) running `<binary> version`. The
   last option only works for a native-arch install, never for cross-arch
   packaging.
4. **Binaries**: copied into `releases/<version>/`, `chmod 0755`. Previous
   versions are never deleted — that's what makes rollback possible.
5. **Full Edge vision worker sources** (`deploy/vision-worker/*.py`,
   `requirements.txt`) are copied alongside the release if found — versioned
   together with the agent, since the wire protocol lives in both. **Only
   sources travel — no interpreter, no venv, no model weights.**
6. **Config**: `/etc/geocam-edge/geocam-edge.env` is written from
   `deploy/appliance/config/geocam-edge.env.example` **only if it doesn't
   already exist** — an existing file is left untouched, always.
7. **systemd units** (always refreshed — not operator-editable state):
   `geocam-edge.service`, and `geocam-edge-bootstrap.service` /
   `geocam-edge-ota-updater.{service,path}` if the corresponding `.in`
   templates are present. Placeholders substituted: exec path, ffmpeg env
   line (only if this release bundles ffmpeg), vision worker script env line
   (only if this release bundles the worker), env file path, service
   user/group, data dir.
8. **Ownership** (real Linux targets): `GEOCAM_DATA_DIR` owned by the service
   user; `/opt/geocam-edge` and the libexec dir owned by `root:root`,
   `chmod 0755` — so the unprivileged daemon can never modify the code that
   root's OTA path executes.
9. **Enable, don't start**: `systemctl enable` for `geocam-edge.service` (and
   the bootstrap/OTA units if present) — it deliberately does **not** start
   the service, because enrollment (§3 below) typically needs to happen
   first.

## 3. Configure and enroll

Edit `/etc/geocam-edge/geocam-edge.env` (non-secret config: SaaS URL,
processing mode, retention bounds, etc. — see
`docs/architecture/EDGE_ARCHITECTURE.md` for what each subsystem reads).

Two enrollment paths:

- **Manual**: `geocam-edge enroll` (pipe the enrollment token via stdin —
  never pass it as a CLI arg, never let it land in shell history or logs).
- **Zero-touch** (`deploy/appliance/scripts/bootstrap.sh`): looks for a token
  in, in this order — `GEOCAM_ENROLLMENT_TOKEN` env var, `/boot/geocam-enroll.token`,
  `/boot/firmware/geocam-enroll.token` (Raspberry Pi OS layout), `/etc/geocam-edge/enroll.token`,
  then removable media (`/media/*/geocam-enroll.token`, `/mnt/*/geocam-enroll.token`).
  If found, it calls `geocam-edge enroll` with the token piped via stdin, then
  best-effort deletes the seed file (`shred -u`, falling back to `rm -f`) —
  note flash wear-leveling means this is not a cryptographically guaranteed
  erasure, so treat the seed file as secret while it exists.

## 4. Start and verify

```bash
sudo systemctl start geocam-edge.service
deploy/appliance/scripts/wait-ready.sh   # polls /readyz
geocam-edge check
curl -s http://127.0.0.1:8091/healthz
curl -s http://127.0.0.1:8091/readyz
curl -s http://127.0.0.1:8091/status
```

If installed via `bootstrap.sh` under systemd (`INVOCATION_ID` set), it
deliberately does **not** start the service itself — it defers to
`Before=geocam-edge.service` in the unit ordering. Run manually outside
systemd and it starts the service and waits for readiness itself.

## systemd unit facts (`geocam-edge.service.in`)

- `Type=notify` — the agent sends `READY=1` once its modules and health
  surface are up, and pings the watchdog while `/healthz` keeps answering.
- `TimeoutStartSec=30` — generous for local-only startup work (read
  identity/credentials, recover backlog/cloud spool, bind the health port).
- `WatchdogSec=60`, agent pings at ~20s — tolerates two missed checks before
  systemd intervenes; deliberately loose because ffmpeg decode + a YOLO
  worker can cause momentary stalls under load.
- `Restart=on-failure`, `RestartSec=2`, `StartLimitBurst=5` /
  `StartLimitIntervalSec=300` — at most 5 restarts per 5 minutes, then the
  unit is left failed for an operator to inspect.
- Hardening: `NoNewPrivileges=true`, `ProtectSystem=strict`,
  `ProtectHome=true`, `PrivateTmp=true`, `CapabilityBoundingSet=` (empty — no
  capabilities needed, confirmed by audit in `docs/security/least-privilege.md`).
  `PrivateDevices` is deliberately **not** set, because Full Edge's
  `GEOCAM_EDGE_YOLO_DEVICE=cuda` needs host accelerator device nodes.
  `ReadWritePaths` is only `GEOCAM_DATA_DIR`.
- Logging: stdout/stderr only, to `journal` — there is no separate file-log
  system to configure.

## Update

```bash
sudo deploy/appliance/scripts/update.sh <artifact.tar.gz>
```

See `docs/runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md` for the full flow
(checksum verification, architecture check, automatic rollback on failed
readiness).

## Rollback

```bash
sudo deploy/appliance/scripts/rollback.sh
```

Reverts `current` to the release recorded in `/opt/geocam-edge/.previous`
(written by `install.sh`/`update.sh` every time `current` is repointed),
restarts the service, verifies `/readyz`. Never touches `GEOCAM_DATA_DIR`,
credentials, or identity. Fails loudly (`die`) if there's nothing recorded to
roll back to, or if that previous release no longer exists on disk.

## Uninstall

```bash
sudo deploy/appliance/scripts/uninstall.sh            # keeps data + config
sudo deploy/appliance/scripts/uninstall.sh --purge     # also deletes data + config, asks "yes" first
```

By default, `GEOCAM_DATA_DIR` (identity/credentials/offline buffer) and the
config file are **always** left intact — uninstalling is never an accidental
way to lose enrollment. `--purge` requires typing `yes` at a prompt (or
`GEOCAM_UNINSTALL_PURGE_CONFIRM=yes` for scripted/test runs) before it deletes
`GEOCAM_DATA_DIR`/`GEOCAM_CONFIG_DIR`.

---

See also: `docs/architecture/EDGE_ARCHITECTURE.md`,
`docs/runbooks/FULL_EDGE_FROM_SCRATCH.md` (Python worker provisioning, not
covered by install.sh), `docs/runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md`,
`docs/security/least-privilege.md`, `docs/README.md`.
