#!/usr/bin/env bash
# Verifies that this appliance can actually run Full Edge's local inference
# worker, WITHOUT models, GPU or inference.
#
# Full Edge runs its Vision Worker out of process (PyTorch is never embedded in
# the Go agent). The appliance package ships the worker's SOURCES but
# deliberately does NOT ship a Python runtime: ultralytics/PyTorch wheels are
# architecture-specific, so a "portable venv" inside an amd64+arm64 tarball
# cannot exist honestly. The runtime is therefore provisioned on the appliance,
# and this script is how that requirement is checked explicitly instead of being
# discovered at first inference.
#
# Usage: check-vision-runtime.sh [--worker-dir DIR] [--python PATH]
#
#   --worker-dir  directory holding worker.py/backend.py (default:
#                 $GEOCAM_PREFIX/current/vision-worker, else ../vision-worker
#                 relative to this script)
#   --python      interpreter to check (default: $GEOCAM_EDGE_YOLO_WORKER_CMD,
#                 else the first python3 on PATH)
#
# Exit status: 0 only when every requirement is satisfied. Non-zero with a
# specific diagnostic otherwise. Never downloads a model, never loads weights,
# never runs inference, and never mutates anything.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

WORKER_DIR=""
PYTHON_BIN=""

while [ $# -gt 0 ]; do
    case "$1" in
        --worker-dir) WORKER_DIR="${2:-}"; shift 2 ;;
        --python)     PYTHON_BIN="${2:-}"; shift 2 ;;
        -h|--help)    sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \?//'; exit 0 ;;
        *) die "usage: check-vision-runtime.sh [--worker-dir DIR] [--python PATH]" ;;
    esac
done

if [ -z "$WORKER_DIR" ]; then
    if [ -n "${GEOCAM_PREFIX:-}" ] && [ -d "$GEOCAM_PREFIX/current/vision-worker" ]; then
        WORKER_DIR="$GEOCAM_PREFIX/current/vision-worker"
    else
        WORKER_DIR="$(cd "$SCRIPT_DIR/../vision-worker" 2>/dev/null && pwd || echo "$SCRIPT_DIR/../vision-worker")"
    fi
fi

if [ -z "$PYTHON_BIN" ]; then
    PYTHON_BIN="${GEOCAM_EDGE_YOLO_WORKER_CMD:-}"
fi
if [ -z "$PYTHON_BIN" ]; then
    PYTHON_BIN="$(command -v python3 || true)"
fi

FAILED=0
fail() { printf '[check-vision-runtime] FAIL: %s\n' "$1" >&2; FAILED=1; }
ok()   { printf '[check-vision-runtime] ok: %s\n' "$1"; }

# --- 1. Worker sources are present ----------------------------------------
for f in worker.py backend.py requirements.txt; do
    if [ -f "$WORKER_DIR/$f" ]; then
        ok "found $WORKER_DIR/$f"
    else
        fail "missing $WORKER_DIR/$f — is this a release that shipped vision-worker/ (B2), or was it removed?"
    fi
done
if [ "$FAILED" != "0" ]; then
    die "the Full Edge vision worker sources are incomplete at $WORKER_DIR"
fi

# --- 2. An interpreter exists ---------------------------------------------
if [ -z "$PYTHON_BIN" ]; then
    fail "no Python interpreter found: set GEOCAM_EDGE_YOLO_WORKER_CMD, pass --python, or install python3 on PATH"
    die "cannot check the Full Edge runtime without an interpreter"
fi
if [ ! -x "$PYTHON_BIN" ]; then
    fail "interpreter $PYTHON_BIN is not executable"
    die "cannot check the Full Edge runtime"
fi
ok "interpreter: $PYTHON_BIN"

# --- 3. Interpreter version ------------------------------------------------
# The worker's own code needs Python 3.7+ (it uses `from __future__ import
# annotations` plus dataclasses). That is the hard floor enforced here; below it
# the sources cannot even be compiled, so it is a real requirement, not a
# preference. The pinned ultralytics major may raise this in practice — the
# dependency check below is what ultimately decides, and it reports the versions
# it actually found.
PYVER="$("$PYTHON_BIN" -c 'import sys; print("%d.%d.%d" % sys.version_info[:3])' 2>/dev/null || true)"
if [ -z "$PYVER" ]; then
    fail "could not determine the interpreter version ($PYTHON_BIN did not run)"
    die "interpreter $PYTHON_BIN is unusable"
fi
PYMAJOR="${PYVER%%.*}"
PYREST="${PYVER#*.}"
PYMINOR="${PYREST%%.*}"
if [ "$PYMAJOR" -lt 3 ] || { [ "$PYMAJOR" -eq 3 ] && [ "$PYMINOR" -lt 7 ]; }; then
    fail "python $PYVER is too old: the worker requires 3.7+ (from __future__ annotations, dataclasses)"
    die "unsupported Python version"
fi
ok "python version: $PYVER"

# --- 4. The worker sources compile ----------------------------------------
# py_compile needs no third-party package, so this isolates "our code is valid
# for this interpreter" from "the ML runtime is installed".
if "$PYTHON_BIN" -m py_compile "$WORKER_DIR/worker.py" "$WORKER_DIR/backend.py" 2>/tmp/.geocam-vision-compile.$$; then
    ok "worker.py and backend.py compile under this interpreter"
    rm -f /tmp/.geocam-vision-compile.$$
else
    err="$(cat /tmp/.geocam-vision-compile.$$ 2>/dev/null || true)"
    rm -f /tmp/.geocam-vision-compile.$$
    fail "worker.py/backend.py do not compile under $PYTHON_BIN: $err"
    die "the shipped worker sources are not usable with this interpreter"
fi

# --- 5. Runtime dependencies are importable --------------------------------
# This is the check that matters, and it MUST name the packages explicitly:
# worker.py/backend.py import ultralytics and torch LAZILY inside load(), so
# importing the worker succeeds even with nothing installed. A check that only
# imported the worker would report a healthy Full Edge that cannot infer at all.
#
# GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS is honoured only so packaging/install
# tests can exercise the file-and-interpreter checks in a sandbox with no ML
# stack. It never makes the script report success on its own — it downgrades the
# import check to an explicit, loud warning.
if [ "${GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS:-0}" = "1" ]; then
    # The escape hatch exists ONLY so tests can exercise the file, interpreter
    # and model-reporting paths on a machine with no ML stack (notably the test
    # asserting the preflight never fetches weights). It must never be able to
    # report readiness, so it sets FAILED and the run ends non-zero below: the
    # script's contract is "exit 0 only when every requirement is satisfied",
    # and a skipped requirement is not a satisfied one. The run still continues
    # to the model-reporting block so that test remains meaningful.
    printf '[check-vision-runtime] FAIL: import check skipped by GEOCAM_VISION_PREFLIGHT_SKIP_IMPORTS=1; Full Edge runtime readiness is NOT established\n' >&2
    FAILED=1
else
    PROBE_OUT="$("$PYTHON_BIN" - <<'PY' 2>&1
import importlib, json, sys

missing = []
versions = {}
for mod, dist in (("ultralytics", "ultralytics"), ("torch", "torch"), ("PIL", "pillow")):
    try:
        m = importlib.import_module(mod)
        versions[mod] = getattr(m, "__version__", "unknown")
    except Exception as exc:
        missing.append(f"{dist} ({mod}: {exc.__class__.__name__}: {exc})")

print(json.dumps({"missing": missing, "versions": versions, "python": sys.version.split()[0]}))
PY
)" || true

    if printf '%s' "$PROBE_OUT" | grep -q '"missing": \[\]'; then
        ok "runtime dependencies importable: $PROBE_OUT"
    else
        fail "runtime dependencies are NOT satisfied: $PROBE_OUT"
        printf '[check-vision-runtime]   install them reproducibly with:\n' >&2
        printf '[check-vision-runtime]     %s -m venv /opt/geocam-edge/vision-venv\n' "$PYTHON_BIN" >&2
        printf '[check-vision-runtime]     /opt/geocam-edge/vision-venv/bin/pip install -r %s/requirements.txt\n' "$WORKER_DIR" >&2
        printf '[check-vision-runtime]   then set GEOCAM_EDGE_YOLO_WORKER_CMD=/opt/geocam-edge/vision-venv/bin/python\n' >&2
        printf '[check-vision-runtime]   Model weights are NOT downloaded by anything: place them under\n' >&2
        printf '[check-vision-runtime]   $GEOCAM_DATA_DIR/models yourself.\n' >&2
    fi
fi

# --- 6. Model weights are only REPORTED, never required here --------------
# This script must not need models, and must never fetch them. Their absence is
# already a first-class, non-fatal state in the agent (vision worker
# "model_missing"), so reporting is the correct level here.
MODELS_DIR="${GEOCAM_EDGE_YOLO_MODELS_DIR:-${GEOCAM_DATA_DIR:-/var/lib/geocam-edge}/models}"
if [ -d "$MODELS_DIR" ] && [ -n "$(ls -A "$MODELS_DIR" 2>/dev/null || true)" ]; then
    ok "model directory $MODELS_DIR exists and is non-empty (contents not validated here)"
else
    printf '[check-vision-runtime] note: no model weights in %s — the agent reports model_missing until you place them (nothing is auto-downloaded)\n' "$MODELS_DIR" >&2
fi

if [ "$FAILED" != "0" ]; then
    # No PASS line is printed on any failure path, including a skipped import
    # check. Exit non-zero so a caller (or an operator) cannot mistake a
    # partially-checked runtime for a ready one.
    die "Full Edge runtime is NOT ready — see the FAIL lines above"
fi
printf '[check-vision-runtime] PASS: interpreter, worker sources and runtime dependencies are all satisfied\n'
printf '[check-vision-runtime] (this does NOT run inference, validate CUDA, or certify hardware)\n'
