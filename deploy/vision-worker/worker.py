#!/usr/bin/env python3
"""GEO CAM Edge local vision worker (Milestone K2).

Spawned and supervised by the Go agent (internal/vision.Worker) -- never run
standalone in production. Speaks newline-delimited JSON over a single Unix
domain socket, loopback-local by construction (a unix socket has no network
exposure at all): one request, one response, in order, matching
internal/vision/protocol.go's wireRequest/wireResponse exactly.

Never logs a frame's bytes, a credential, or an RTSP URI -- stdout/stderr
carry only startup/diagnostic text, which the Go agent folds into its own
structured logs (internal/vision/helpers.go's slogWriter).
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import socket
import sys
from dataclasses import asdict

from backend import Detection, InferenceBackend, YOLOBackend  # noqa: F401 (Detection used via asdict)


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def parse_args(argv: list[str]) -> argparse.Namespace:
    p = argparse.ArgumentParser(description="GEO CAM Edge local vision worker")
    p.add_argument("--socket", required=True)
    p.add_argument("--person-model", required=True)
    p.add_argument("--vehicle-model", required=True)
    p.add_argument("--person-confidence", type=float, default=0.5)
    p.add_argument("--vehicle-confidence", type=float, default=0.5)
    p.add_argument("--nms-iou", type=float, default=0.45)
    p.add_argument("--device", default="cpu")
    p.add_argument("--imgsz", type=int, default=640)
    return p.parse_args(argv)


def build_backend(args: argparse.Namespace) -> InferenceBackend:
    return YOLOBackend(
        person_model_path=args.person_model,
        vehicle_model_path=args.vehicle_model,
        device=args.device,
        imgsz=args.imgsz,
        person_confidence=args.person_confidence,
        vehicle_confidence=args.vehicle_confidence,
        nms_iou=args.nms_iou,
    )


def handle_request(backend: InferenceBackend, req: dict, loaded_state: dict) -> dict:
    req_type = req.get("type")

    if req_type == "health":
        if not loaded_state.get("done"):
            result = backend.load()
            loaded_state["done"] = True
            loaded_state["ready"] = result.ready
            if not result.ready:
                return {"type": "health_ok", "ready": False, "error": result.error}
            loaded_state["device"] = result.device
            loaded_state["device_requested"] = getattr(result, "device_requested", "")
            loaded_state["models_loaded"] = result.models_loaded
            # A fallback (or any other non-fatal note) must be visible in the
            # agent's logs, never silent -- the Go side folds this stream in.
            if getattr(result, "warning", ""):
                log(f"device: {result.warning}")
        return {
            "type": "health_ok",
            "ready": loaded_state.get("ready", False),
            "device": loaded_state.get("device", ""),
            "device_requested": loaded_state.get("device_requested", ""),
            "models_loaded": loaded_state.get("models_loaded", []),
        }

    if req_type == "infer":
        if not loaded_state.get("ready"):
            return {"type": "error", "error": "backend not ready"}
        try:
            jpeg = base64.b64decode(req.get("jpeg", ""))
            result = backend.infer(jpeg, req.get("width", 0), req.get("height", 0))
        except Exception as exc:  # noqa: BLE001 - never crash the worker on a bad frame
            return {"type": "error", "error": str(exc)}
        return {
            "type": "result",
            "frame_seq": req.get("frame_seq", 0),
            "inference_ms": result.inference_ms,
            "detections": [asdict(d) for d in result.detections],
        }

    if req_type == "shutdown":
        return {"type": "health_ok"}

    return {"type": "error", "error": f"unknown request type {req_type!r}"}


def serve(sock_path: str, backend: InferenceBackend) -> None:
    if os.path.exists(sock_path):
        os.remove(sock_path)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(sock_path)
    srv.listen(1)
    log(f"vision worker listening on {sock_path}")

    loaded_state: dict = {}
    try:
        with srv:
            try:
                conn, _ = srv.accept()
            except Exception as exc:  # noqa: BLE001
                log(f"accept failed: {exc}")
                return

            with conn:
                rd = conn.makefile("rb")
                while True:
                    line = rd.readline()
                    if not line:
                        return
                    try:
                        req = json.loads(line)
                    except json.JSONDecodeError as exc:
                        conn.sendall((json.dumps({"type": "error", "error": f"bad json: {exc}"}) + "\n").encode())
                        continue

                    resp = handle_request(backend, req, loaded_state)
                    conn.sendall((json.dumps(resp) + "\n").encode())

                    if req.get("type") == "shutdown":
                        return
    finally:
        if os.path.exists(sock_path):
            os.remove(sock_path)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    backend = build_backend(args)
    serve(args.socket, backend)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
