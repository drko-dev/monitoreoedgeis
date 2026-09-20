# Offline & Recovery — Operations Reference

> Operational quick-reference for what Edge does when things go wrong:
> network loss, camera failures, restarts, power loss. Full failure-mode
> analysis and test coverage: `docs/resilience/RESILIENCE_MATRIX.md` — this
> document is the operator-facing summary, not a replacement for it.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`.

## SaaS offline / internet loss

- The **SaaS**, not the Edge, decides when to consider an Edge "offline" —
  based on missed heartbeats (`internal/heartbeat/`). The Edge itself keeps
  working locally the entire time: RTSP supervisors keep running, discovery
  keeps running, Full Edge inference keeps running.
- Cloud-bound frames (Gateway/Hybrid) buffer locally in the cloud sink
  (`internal/transport/cloudsink.go`) with age- and byte-based eviction,
  oldest-first, when the SaaS is unreachable.
- Control instructions and remote config changes queue locally until
  reconnect; nothing is lost by an outage, only delayed.

## Camera TCP refused / RTSP EOF / timeout / stall

Covered in full in `docs/runbooks/CAMERA_FROM_SCRATCH_EDGE.md` §B/§F. Summary:
the RTSP supervisor retries with exponential backoff (`InitialBackoff` 1s →
`MaxBackoff` 60s), staying in `degraded` — not `offline` — while it keeps
trying. `offline` is reserved for a supervisor that Edge itself stopped
(target removed), not a camera that's merely unreachable.

## Auth failures

`auth_failed` **does** keep retrying automatically with exponential backoff,
using the same credentials, just like any other dial failure
(`internal/rtsp/supervisor.go`) — it does not stop and wait. A genuinely wrong
credential will keep failing the same way on every retry until a SaaS
credential sync produces a *new* `CameraTarget`, which replaces the running
supervisor via `Manager.SetTargets()` rather than "unblocking" the old one.

## Durable backlog

`internal/edgebacklog/` persists pending event records to
`{GEOCAM_DATA_DIR}/local-event-backlog/pending/*.json`, strict FIFO, durable
across process restarts. On capacity overflow it returns an explicit error
rather than silently trimming — a full backlog is a visible, diagnosable
condition, not a silent data loss.

## Restart / graceful shutdown

`cmd/geocam-edge`'s `signal.NotifyContext(SIGINT, SIGTERM)` drives an
in-process graceful shutdown: modules stop in reverse start order, the
backlog and cloud buffer flush what they can before exit. Under systemd, the
agent sends `STOPPING=1` as soon as shutdown begins, which cancels the
watchdog timer — so an orderly shutdown is never raced by a watchdog kill
(`deploy/appliance/systemd/geocam-edge.service.in`).

On restart: the backlog is reloaded from disk, the cloud buffer is replayed,
and RTSP supervisors start fresh from `connecting` for every configured
camera.

## Power loss

**Designed/tested-local behavior** (what the code and local tests exercise)
vs. **physical validation** (an actual power-cut test on real hardware) are
different claims — do not conflate them. Graceful-shutdown and backlog
recovery are tested; see `docs/product/PHYSICAL_VALIDATION_REGISTER.md` for
whether an actual power-loss test has been physically executed. If that
register says `NOT_EXECUTED`, treat power-loss resilience as
designed-and-locally-tested, not physically validated.

---

See also: `docs/resilience/RESILIENCE_MATRIX.md` (full failure-mode matrix
and test references), `docs/operations/RETENTION.md` (disk/backlog
interaction), `docs/product/PHYSICAL_VALIDATION_REGISTER.md`,
`docs/README.md`.
