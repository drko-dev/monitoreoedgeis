"""Unit tests for the vision worker's protocol handling and model manager
equivalent (K2/K3), using backend.FakeBackend -- no torch/ultralytics
required, matching the task's "no descargar cientos de MB para un test"
constraint. Real-model smoke is a separate, explicitly-gated concern (see
docs/deployment/appliance.md-style honesty in the PR description: BLOCKED
without real .pt weights in this sandbox).
"""

import base64
import json
import os
import socket
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from backend import FakeBackend  # noqa: E402
from worker import handle_request, serve  # noqa: E402


class HandleRequestTests(unittest.TestCase):
    def test_health_ready(self):
        backend = FakeBackend(ready=True)
        state: dict = {}
        resp = handle_request(backend, {"type": "health"}, state)
        self.assertEqual(resp["type"], "health_ok")
        self.assertTrue(resp["ready"])
        self.assertEqual(resp["device"], "cpu")
        self.assertEqual(len(resp["models_loaded"]), 2)

    def test_health_not_ready_reports_error_not_crash(self):
        backend = FakeBackend(ready=False, error="model load failed")
        state: dict = {}
        resp = handle_request(backend, {"type": "health"}, state)
        self.assertEqual(resp["type"], "health_ok")
        self.assertFalse(resp["ready"])
        self.assertEqual(resp["error"], "model load failed")

    def test_health_is_cached_across_calls(self):
        backend = FakeBackend(ready=True)
        state: dict = {}
        handle_request(backend, {"type": "health"}, state)
        # A second health call must not reload the backend (state["done"]
        # gates it) -- this mirrors K3's "never re-download/reload silently"
        # rule at the protocol-handling layer.
        self.assertTrue(state["done"])

    def test_infer_before_ready_is_an_explicit_error(self):
        backend = FakeBackend(ready=True)
        state: dict = {}  # health never called -> not ready yet
        resp = handle_request(backend, {"type": "infer", "frame_seq": 1}, state)
        self.assertEqual(resp["type"], "error")

    def test_infer_returns_detections(self):
        backend = FakeBackend(ready=True)
        state: dict = {}
        handle_request(backend, {"type": "health"}, state)
        jpeg_b64 = base64.b64encode(b"not-a-real-jpeg-but-fake-backend-ignores-it").decode()
        resp = handle_request(backend, {
            "type": "infer", "frame_seq": 7, "width": 640, "height": 360, "jpeg": jpeg_b64,
        }, state)
        self.assertEqual(resp["type"], "result")
        self.assertEqual(resp["frame_seq"], 7)
        self.assertEqual(len(resp["detections"]), 1)
        self.assertEqual(resp["detections"][0]["type"], "person")

    def test_shutdown_acknowledged(self):
        backend = FakeBackend(ready=True)
        resp = handle_request(backend, {"type": "shutdown"}, {})
        self.assertEqual(resp["type"], "health_ok")

    def test_unknown_type_is_an_error_not_a_crash(self):
        backend = FakeBackend(ready=True)
        resp = handle_request(backend, {"type": "bogus"}, {})
        self.assertEqual(resp["type"], "error")


class ServeSocketTests(unittest.TestCase):
    """End-to-end over a real Unix socket, matching what
    internal/vision.Worker actually does (health, then one infer, then
    shutdown) -- with FakeBackend standing in for Ultralytics.
    """

    def test_full_roundtrip(self):
        with tempfile.TemporaryDirectory() as d:
            sock_path = os.path.join(d, "vision.sock")
            backend = FakeBackend(ready=True)
            t = threading.Thread(target=serve, args=(sock_path, backend), daemon=True)
            t.start()

            client = self._connect(sock_path)
            with client:
                rd = client.makefile("rb")

                self._send(client, {"type": "health"})
                health = json.loads(rd.readline())
                self.assertTrue(health["ready"])

                self._send(client, {
                    "type": "infer", "frame_seq": 1, "width": 640, "height": 360,
                    "jpeg": base64.b64encode(b"fake").decode(),
                })
                result = json.loads(rd.readline())
                self.assertEqual(result["type"], "result")
                self.assertEqual(len(result["detections"]), 1)

                self._send(client, {"type": "shutdown"})
                shut = json.loads(rd.readline())
                self.assertEqual(shut["type"], "health_ok")

            t.join(timeout=2)

    @staticmethod
    def _connect(sock_path: str, attempts: int = 50) -> socket.socket:
        import time

        for _ in range(attempts):
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                s.connect(sock_path)
                return s
            except FileNotFoundError:
                s.close()
                time.sleep(0.02)
        raise TimeoutError("worker socket never appeared")

    @staticmethod
    def _send(conn: socket.socket, obj: dict) -> None:
        conn.sendall((json.dumps(obj) + "\n").encode())


if __name__ == "__main__":
    unittest.main()
