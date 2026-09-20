# Full Edge From Scratch

> Full Edge = the Go agent + a local Python Vision Worker running YOLO
> inference out of process. This document covers provisioning that Python
> side — the appliance package deliberately does not ship a Python runtime,
> models, or a virtualenv.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`.

## Why out-of-process

PyTorch/ultralytics are never embedded in the Go binary
(`deploy/vision-worker/backend.py` imports them lazily, inside `load()`, so
even importing the worker module works without them installed). The reason:
ultralytics/PyTorch wheels are architecture- and (for CUDA) hardware-specific,
so a single portable venv covering both `amd64` and `arm64` — let alone GPU
vs. CPU — cannot exist honestly inside one appliance tarball. Install.sh ships
only `worker.py` + `backend.py` + `requirements.txt`; you provision the
interpreter.

## GEOCAM_EDGE_YOLO_DEVICE

Read by the Go agent when launching the worker and passed through as the
worker's `--device` argument. Accepts whatever `ultralytics`'s own device
resolution accepts: `cpu`, `cuda`, `auto` (default — picks CUDA if available,
else CPU), `mps`, or a numeric device index. **CUDA is not physically
validated** in this repo — see
`docs/product/PHYSICAL_VALIDATION_REGISTER.md`.

## Vision Worker internals

- **Location:** `deploy/vision-worker/worker.py` (entrypoint),
  `deploy/vision-worker/backend.py` (`YOLOBackend`, `Detection`,
  `HealthResult`, `InferResult` dataclasses).
- **Protocol:** newline-delimited JSON over a Unix domain socket
  (`GEOCAM_EDGE_YOLO_SOCKET_PATH`, default `/run/geocam-edge/vision.sock`),
  mirrored on the Go side by `internal/vision/`. Request types: `health`,
  `infer` (base64 JPEG + dimensions), `shutdown`. Response types: `health_ok`
  (ready, device, models loaded), `result` (detections + inference time),
  `error`.
- **Startup:** `worker.py` parses `--socket --person-model --vehicle-model
  --device --imgsz --confidence --nms-iou`, builds the backend, then serves
  the socket.
- **Failure semantics:** a bad JPEG never crashes the worker — it's caught
  and returned as an `error` response. A worker that isn't running or doesn't
  respond degrades the agent to reporting the failure via `/status`; it does
  not crash the Go process.

## check-vision-runtime.sh

```bash
deploy/appliance/scripts/check-vision-runtime.sh [--worker-dir DIR] [--python PATH]
```

Verifies the appliance **can** run Full Edge — never runs inference, never
downloads a model, never mutates anything. Checks, in order (exit 0 only if
every one passes):

1. Worker sources exist: `worker.py`, `backend.py`, `requirements.txt` in the
   worker directory (default: `$GEOCAM_PREFIX/current/vision-worker`).
2. An interpreter is resolvable (`--python` flag →
   `GEOCAM_EDGE_YOLO_WORKER_CMD` → first `python3` on `PATH`) and executable.
3. Interpreter is Python **3.7+** — a hard floor, the worker's own source uses
   `from __future__ import annotations` and dataclasses; below 3.7 it cannot
   even compile.
4. `worker.py`/`backend.py` compile under that interpreter (`py_compile`) —
   isolates "our code is valid for this interpreter" from "the ML stack is
   installed".
5. **Runtime dependencies are importable**: `ultralytics`, `torch`, `PIL`
   (pillow) — checked explicitly by name, because the worker imports them
   lazily, so a check that only imported the worker module would report a
   healthy Full Edge that cannot actually infer. `GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS=1`
   downgrades this to a loud warning for sandboxes with no ML stack, but it
   can **never** make the overall check report success on its own.
6. Model weights are only **reported**, never required by this script:
   `$GEOCAM_EDGE_YOLO_MODELS_DIR` (default `$GEOCAM_DATA_DIR/models`) is
   checked for non-empty contents and logged either way.

If dependencies are missing, the script itself prints the exact remediation:

```bash
python3 -m venv /opt/geocam-edge/vision-venv
/opt/geocam-edge/vision-venv/bin/pip install -r <worker-dir>/requirements.txt
# then, in geocam-edge.env:
GEOCAM_EDGE_YOLO_WORKER_CMD=/opt/geocam-edge/vision-venv/bin/python
```

## Models

Fixed defaults confirmed in `internal/config/config.go`:

- Person: `yolo11s-pose.pt`
- Vehicle: `yolo11n.pt`

Both overridable via Remote Config (`docs/REMOTE_CONFIG.md`), never
auto-selected otherwise. **No component in this repo downloads a model.**
Place the exact files under `$GEOCAM_DATA_DIR/models` (default
`/var/lib/geocam-edge/models`) yourself. A missing model is reported as
`model_missing` by the agent/worker — a first-class, non-fatal state, not a
crash.

## Failure states this document does not invent numbers for

- **`model_missing`** — weights not found under the models directory.
- **`torch missing` / `ultralytics missing` / `Pillow missing`** — reported
  by `check-vision-runtime.sh`'s dependency probe, with the exact package and
  Python exception in its output.
- **CPU vs CUDA** — CPU inference is exercised locally
  (`docs/product/PHYSICAL_VALIDATION_REGISTER.md`); **CUDA has not been
  physically validated** — treat any CUDA-specific latency/throughput claim
  as unverified until that register says otherwise.

---

See also: `docs/architecture/EDGE_ARCHITECTURE.md` §10–12,
`docs/product/PHYSICAL_VALIDATION_REGISTER.md`,
`docs/runbooks/EDGE_INSTALL_FROM_SCRATCH.md`, `docs/README.md`.
