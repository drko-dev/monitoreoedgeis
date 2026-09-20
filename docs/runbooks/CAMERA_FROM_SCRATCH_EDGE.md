# Camera From Scratch — Edge

> Scenario: you have a fresh Edge install and a new camera. How do you get it
> connected? This document walks the whole path, with real commands and real
> code references. It never invents a manufacturer default — where the repo
> doesn't fix a value, this document says so explicitly.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`.
Deep design reference for this flow: `docs/product/G1_CAMERA_TARGET_WIRING.md`.

---

## A. Requirements

What the camera needs, before Edge can do anything with it:

- **IP address reachable from the Edge host's LAN.** Discovery
  (`internal/discovery/`) is WS-Discovery on the local network segment — it
  does **not** cross routed subnets (`README.md`). A camera on a different
  VLAN/subnet than the Edge host will not be found by discovery and must be
  provisioned as a manual `CameraTarget` instead.
- **ONVIF support**, if you want automatic discovery/credential-probing.
  Cameras without ONVIF can still be used via manually configured RTSP
  targets, but Edge does not document a manufacturer-specific manual flow
  here — that would be inventing vendor behavior this repo doesn't own.
- **RTSP support**, since that's the only stream transport this repo speaks
  (`internal/rtsp/`).
- **Username/password** for the camera (ONVIF + RTSP auth).
- **Reachable ports**: whatever the camera actually exposes for ONVIF
  (commonly 80/8080/8899 depending on vendor — **not fixed by this repo**,
  it's whatever the discovered device advertises) and RTSP (commonly 554,
  again vendor-set, not hardcoded here).
- Codec/stream/path/resolution/FPS are all camera-specific and are *read
  from* the camera during discovery/`GetProfiles`, not assumed by Edge.

**Do not assume a manufacturer default not present in this repo.** If a value
isn't returned by the camera or set by you, Edge does not invent one.

## B. Connectivity

Real, generic checks you can run from the Edge host before trusting Edge's own
discovery:

```bash
# TCP reachability of ONVIF/RTSP ports (replace host/port with the camera's real ones)
nc -zv <camera-ip> <onvif-port>
nc -zv <camera-ip> 554

# RTSP handshake without decoding anything (needs ffmpeg or ffprobe on the
# operator's own machine — this is a generic diagnostic step, not an Edge CLI
# command)
ffprobe -rtsp_transport tcp "rtsp://<user>:<pass>@<camera-ip>:554/<path>"
```

What each failure actually means (mapped to Edge's own RTSP states,
`internal/rtsp/types.go`):

| Symptom | Edge state | Meaning |
|---|---|---|
| `Connection refused` | never reaches `connecting` | Nothing listening on that TCP port — wrong port, camera off, firewall |
| `Connection timed out` / no route | stays `connecting`, retries with backoff | Network path problem: wrong subnet, ACL, camera unreachable |
| RTSP `401` / digest failure | `auth_failed` (`ErrAuthFailed`) | Wrong username/password. **No automatic retry** — must fix credentials |
| ONVIF "invalid credentials" | discovery/credential resolution fails for that device | Same root cause as RTSP 401, but at the ONVIF probe stage |
| ONVIF unreachable | device does not appear in discovery inventory | Either not ONVIF-capable, on a different subnet, or ONVIF service disabled on the camera |
| Stream connects, no packets (silence) | `degraded` (`ErrTimeout`, `PacketTimeout` 5s default) | Camera accepted the connection but stopped sending — codec mismatch, camera-side stream failure |
| Connection drops mid-stream | `degraded` (`ErrClosed`/EOF), retries with backoff | Camera closed the connection — reboot, resource limit on camera, network blip |
| Supervisor not running for this camera at all | `offline` | This is Edge's own supervisor state, not "camera unreachable" — it means Edge stopped tracking this target (removed from `SetTargets`) |
| Working normally | `connecting` → `online` | Steady state |

Backoff parameters (`internal/rtsp/types.go`): `InitialBackoff` 1s,
`MaxBackoff` 60s (exponential), `DialTimeout` 5s, `PacketTimeout` 5s. These are
compiled defaults — check `internal/config/config.go` before assuming an env
var overrides them; this document does not claim one exists unless the dossier
grep found it.

## C. Discovery

Real command: `geocam-edge discovery` (`cmd/geocam-edge/main.go`). It runs
WS-Discovery on the LAN and reports found ONVIF devices.

- **What it discovers:** ONVIF devices announcing themselves on the local
  network segment.
- **What it does NOT discover:** anything outside the LAN/subnet, non-ONVIF
  RTSP-only cameras, DVR/NVR-aggregated channels behind a proxy that doesn't
  itself speak WS-Discovery.
- **Inventory / TTL:** discovered devices are tracked with a last-seen
  timestamp; a device that stops responding ages out rather than being
  removed the instant one probe fails — see `docs/product/G1_CAMERA_TARGET_WIRING.md`
  for the exact TTL and removal test coverage (`TestG1B_RemoveByInventoryTTL`).
- **A camera that disappears:** it is not immediately deleted from
  `CameraTarget`s; it ages out of the inventory first. Once aged out, its
  `CameraTarget` is removed via the next `SetTargets()` reconciliation, which
  stops its RTSP supervisor cleanly.

## D. Credentials

Real flow (`docs/product/G1_CAMERA_TARGET_WIRING.md`, `internal/cameracreds/`):

```
SaaS camera-credentials API
    -> Edge sync (internal/cameracreds, synced from the SaaS)
    -> discovery/ONVIF authentication probe
    -> CameraTarget (username/password attached)
    -> rtsp.Manager.SetTargets()
```

Do not put real secrets in any document, config example, or log — credentials
are per-device and per-group; see `docs/security/least-privilege.md` for the
exact precedence rules between device-level and group-level credentials. This
document does not restate secret values or a specific customer's credential
scheme.

## E. CameraTarget

`internal/rtsp/supervisor.go` — the struct that carries everything the RTSP
layer needs for one camera: candidate key (stable identity, not just an IP,
so a camera with a rotating IP is still recognized — see the G1 doc), address,
RTSP path, username/password, stream role (`main`/`sub`), codec, width,
height, FPS.

- **Built:** by discovery + credential resolution once a device is both
  found and authenticated.
- **Updated:** whenever discovery/credentials produce a new value for an
  existing candidate key (e.g. IP changed, credentials rotated) — reconciled
  through `rtsp.Manager.SetTargets()`, not mutated in place.
- **Removed:** when a candidate key disappears from the resolved target list
  (camera aged out, manually removed) — `SetTargets()` stops its supervisor.
- **Reaches `rtsp.Manager.SetTargets`:** as a full list, every reconciliation
  cycle; `SetTargets` diffs against the currently running supervisors and
  only starts/stops what actually changed.

## F. RTSP

Covered in detail in §B above. Summary of the state machine
(`internal/rtsp/types.go`):

```
connecting --(TCP+RTSP handshake OK)--> online
online --(packet silence > PacketTimeout)--> degraded
online --(connection closed/EOF)--> degraded
degraded --(handshake OK again)--> online
connecting/degraded --(401/digest failure)--> auth_failed
any state --(supervisor stopped: target removed)--> offline
```

`auth_failed` does not self-heal — a human or a SaaS credential update has to
supply a new credential before the supervisor retries.

## G. Validation

Real, verified commands (`internal/agent/health_module.go`,
`cmd/geocam-edge/main.go`; port confirmed, not assumed):

```bash
geocam-edge check

curl -s http://127.0.0.1:8091/healthz
curl -s http://127.0.0.1:8091/readyz
curl -s http://127.0.0.1:8091/status
```

Port `8091` is the compiled default for `GEOCAM_HEALTH_ADDR` — check that env
var in your `geocam-edge.env` before assuming it wasn't overridden. These
endpoints are loopback-only by design (`127.0.0.1`), so run `curl` on the Edge
host itself, not remotely.

`geocam-edge check` output shape (`cmd/geocam-edge/main.go`):

```
status:           READY|DEGRADED|...
edge_id:          <sanitized-id>
version:          <version>
processing_mode:  <cloud|hybrid|edge>
uptime:           <duration>
```

Example `/status` shape (values sanitized, never real credentials or a real
camera identity):

```json
{
  "status": "READY",
  "cameras": {
    "cam-aaaa1111": { "state": "online" },
    "cam-bbbb2222": { "state": "auth_failed" }
  }
}
```

(Field names shown here reflect the general `health.Snapshot` shape described
in the dossier and `docs/ARCHITECTURE.md`; treat the exact JSON layout as
"call `/status` yourself and read what comes back" rather than a frozen
contract this document freezes independently of the code.)

## H. Troubleshooting

| Symptom | Meaning | Diagnostic command | Probable cause | Action |
|---|---|---|---|---|
| Edge doesn't see the camera at all | Not in discovery inventory | `geocam-edge discovery` | Different subnet, ONVIF disabled on camera, camera off | Verify LAN segment; confirm ONVIF is enabled on the device; check `nc -zv` on its ONVIF port |
| ONVIF doesn't respond | ONVIF service unreachable | `nc -zv <ip> <onvif-port>` | Camera's ONVIF service down/disabled, wrong port, firewall | Check camera's own ONVIF settings; confirm the port the camera actually advertises |
| Wrong credentials | `auth_failed` in `/status`, ONVIF "invalid credentials" | `curl .../status`, `geocam-edge discovery` | Password rotated on camera but not in Edge/SaaS | Update credential via SaaS camera-credentials flow (§D); it syncs into `internal/cameracreds` |
| RTSP 401 | `auth_failed` on the RTSP supervisor specifically (ONVIF may have succeeded) | `/status` state per camera | RTSP-specific credential differs from ONVIF credential on that camera | Confirm the camera doesn't use separate RTSP vs ONVIF auth |
| RTSP connects, no frames | `degraded`, `PacketTimeout` firing | `/status`, `ffprobe` against the same URL | Codec Edge/ffmpeg can't decode, camera-side stream stall | Check the stream's actual codec with `ffprobe`; confirm it's one ffmpeg (the static binary shipped) supports |
| `ffmpeg` missing | Decode never starts | `geocam-edge check`, journal logs | Appliance package didn't ship/install `ffmpeg`, or `GEOCAM_VIDEO_FFMPEG_PATH` misconfigured | Re-run `install.sh` with the ffmpeg binary argument (`docs/runbooks/EDGE_INSTALL_FROM_SCRATCH.md`) |
| Codec incompatible | ffmpeg exits/rejects the stream | journal logs for the ffmpeg subprocess | Camera stream uses a codec/profile ffmpeg build doesn't support | Confirm codec via `ffprobe`; this is a real limitation, not a config bug |
| Camera intermittent | Repeated `online`↔`degraded` cycling | `/status` over time | Weak network path, camera resource exhaustion, Wi-Fi camera | Check camera's own network stability; consider wired connection |
| SaaS offline | Heartbeat not reaching SaaS | `/status` (backlog/queue depth growing) | Internet/SaaS outage | See `docs/operations/OFFLINE_AND_RECOVERY.md` — Edge keeps working locally |
| Backlog growing | Durable backlog not draining | `/status` queue/backlog fields | SaaS unreachable, or sync rejecting records | See `docs/operations/OFFLINE_AND_RECOVERY.md` |
| Full Edge without Python | Vision worker unavailable | `deploy/appliance/scripts/check-vision-runtime.sh` | Interpreter/deps not provisioned | See `docs/runbooks/FULL_EDGE_FROM_SCRATCH.md` |
| Model missing | `/status` reports `model_missing` | `/status`, `check-vision-runtime.sh` | Weights not placed under `$GEOCAM_DATA_DIR/models` (no auto-download exists) | Place the exact model files named in `docs/runbooks/FULL_EDGE_FROM_SCRATCH.md` |
| Worker not responding | Full Edge inference stalls | `/status`, worker socket check | Worker process crashed or never started | Restart the service; check `GEOCAM_EDGE_YOLO_WORKER_CMD` points at a working interpreter |
| Storage full | Retention/evidence writes failing | `/status`, `df -h $GEOCAM_DATA_DIR` | Disk exhaustion | See `docs/operations/RETENTION.md` — disk gate behavior |

---

See also: `docs/architecture/EDGE_ARCHITECTURE.md` (system overview),
`docs/product/G1_CAMERA_TARGET_WIRING.md` (deep design detail for this exact
flow), `docs/operations/OFFLINE_AND_RECOVERY.md` (SaaS/network outage
behavior), `docs/README.md` (full index).
