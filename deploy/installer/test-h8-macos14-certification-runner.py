#!/usr/bin/env python3
"""Synthetic unit tests for H8 macOS 14 ARM64 certification runner."""

import importlib.util
import io
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
import sys
from unittest import mock

RUNNER_PATH = Path(__file__).with_name("h8-macos14-certification-runner.py")
spec = importlib.util.spec_from_file_location("h8_runner", RUNNER_PATH)
h8 = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = h8
spec.loader.exec_module(h8)


def create_synthetic_macho(
    minos_val: int = (14 << 16),
    cmd: int = h8.LC_BUILD_VERSION,
    cputype: int = h8.CPU_TYPE_ARM64,
    dylibs: list[str] = None,
) -> bytes:
    dylibs = dylibs or ["/usr/lib/libSystem.B.dylib"]
    endian = "<"
    magic = h8.MH_MAGIC_64

    # Build commands
    cmds_bytes = bytearray()
    ncmds = 0

    # 1. MinOS load command
    if cmd == h8.LC_BUILD_VERSION:
        cmd_sz = 32
        cmds_bytes.extend(
            struct.pack(
                endian + "IIIIIIII",
                h8.LC_BUILD_VERSION,
                cmd_sz,
                h8.PLATFORM_MACOS,
                minos_val,
                minos_val,  # sdk
                0,  # ntools
                0,  # pad
                0,  # pad
            )
        )
        ncmds += 1
    elif cmd == h8.LC_VERSION_MIN_MACOSX:
        cmd_sz = 16
        cmds_bytes.extend(
            struct.pack(
                endian + "IIII",
                h8.LC_VERSION_MIN_MACOSX,
                cmd_sz,
                minos_val,
                minos_val,  # sdk
            )
        )
        ncmds += 1

    # 2. Dylib load commands
    for d in dylibs:
        raw_str = d.encode("utf-8") + b"\x00"
        aligned_len = ((len(raw_str) + 7) // 8) * 8
        d_cmd_sz = 24 + aligned_len
        offset_str = 24
        d_bytes = struct.pack(
            endian + "IIIIII",
            h8.LC_LOAD_DYLIB,
            d_cmd_sz,
            offset_str,
            0,  # timestamp
            0,  # current_version
            0,  # compat_version
        ) + raw_str + b"\x00" * (aligned_len - len(raw_str))
        cmds_bytes.extend(d_bytes)
        ncmds += 1

    sizeofcmds = len(cmds_bytes)
    hdr = struct.pack(
        endian + "IIIIIIII",
        magic,
        cputype,
        0,  # cpusubtype
        2,  # MH_EXECUTE
        ncmds,
        sizeofcmds,
        0,  # flags
        0,  # reserved
    )
    return hdr + bytes(cmds_bytes)


class TestH8CertificationRunner(unittest.TestCase):
    def test_synthetic_macho_parsing_compatible_minos14(self):
        data = create_synthetic_macho(minos_val=(14 << 16))
        with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
            f.write(data)
            f.flush()
            temp_path = Path(f.name)

        try:
            res = h8.inspect_macho_file(temp_path)
            self.assertTrue(res.is_macho)
            self.assertTrue(res.arm64_found)
            self.assertTrue(res.minos_compatible)
            self.assertTrue(res.external_dylibs_clean)
            self.assertEqual(len(res.violations), 0)
            self.assertEqual(res.slices[0].minos, "14.0")
        finally:
            temp_path.unlink()

    def test_synthetic_macho_rejects_exceeding_minos27(self):
        data = create_synthetic_macho(minos_val=(27 << 16))
        with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
            f.write(data)
            f.flush()
            temp_path = Path(f.name)

        try:
            res = h8.inspect_macho_file(temp_path)
            self.assertTrue(res.is_macho)
            self.assertFalse(res.minos_compatible)
            self.assertIn("exceeds macOS 14.0 baseline", res.violations[0])
        finally:
            temp_path.unlink()

    def test_synthetic_macho_rejects_homebrew_dependency(self):
        data = create_synthetic_macho(
            minos_val=(14 << 16),
            dylibs=["/usr/lib/libSystem.B.dylib", "/opt/homebrew/lib/libcustom.dylib"],
        )
        with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
            f.write(data)
            f.flush()
            temp_path = Path(f.name)

        try:
            res = h8.inspect_macho_file(temp_path)
            self.assertTrue(res.is_macho)
            self.assertFalse(res.external_dylibs_clean)
            self.assertTrue(any("unauthorized host library" in v for v in res.violations))
        finally:
            temp_path.unlink()

    def test_synthetic_macho_rejects_non_arm64(self):
        data = create_synthetic_macho(
            cputype=h8.CPU_TYPE_X86_64,
        )
        with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
            f.write(data)
            f.flush()
            temp_path = Path(f.name)

        try:
            res = h8.inspect_macho_file(temp_path)
            self.assertTrue(res.is_macho)
            self.assertFalse(res.arm64_found)
            self.assertTrue(any("lacks arm64" in v for v in res.violations))
        finally:
            temp_path.unlink()

    def test_intel_runner_rejected(self):
        with mock.patch("platform.machine", return_value="x86_64"):
            env = h8.check_host_environment()
            self.assertFalse(env["is_arm64"])
            self.assertTrue(env["physical_macos14_blocked"])

    def test_rosetta_runner_rejected(self):
        with mock.patch("platform.machine", return_value="arm64"):
            with mock.patch(
                "subprocess.check_output",
                return_value="1\n",
            ):
                env = h8.check_host_environment()
                self.assertTrue(env["is_rosetta"])
                self.assertFalse(env["is_native_macos14"])

    def test_physical_macos14_blocked_declared_on_host_macos27(self):
        with mock.patch("platform.machine", return_value="arm64"):
            with mock.patch("sys.platform", "darwin"):
                with mock.patch("platform.mac_ver", return_value=("27.0.1", ("", "", ""), "arm64")):
                    with mock.patch("subprocess.run", side_effect=OSError("prlctl not available")):
                        env = h8.check_host_environment()
                        self.assertEqual(env["status_label"], "PHYSICAL_MACOS14_BLOCKED")
                        self.assertTrue(env["physical_macos14_blocked"])
                        self.assertIn("PHYSICAL_MACOS14_BLOCKED", env["reason"])

    def test_worker_contracts_auditor(self):
        with tempfile.TemporaryDirectory() as td:
            wdir = Path(td)
            (wdir / "worker.py").write_text(
                'parser.add_argument("--socket")\n'
                'if req_type == "health": return {"type": "health_ok"}\n'
                'if req_type == "infer": return {"type": "result", "detections": []}\n'
                'if req_type == "shutdown": return {"type": "health_ok"}\n'
                'except Exception as exc: pass # BLE001\n'
            )
            (wdir / "backend.py").write_text(
                'device = "mps"\n'
                'def resolve_device(req): return req, ""\n'
            )
            audit = h8.audit_worker_contracts(wdir)
            self.assertEqual(audit["status"], "PASS")
            self.assertTrue(audit["has_socket_arg"])
            self.assertTrue(audit["has_health"])
            self.assertTrue(audit["has_infer"])
            self.assertTrue(audit["has_mps_support"])

    def test_models_manifest_auditor(self):
        with tempfile.TemporaryDirectory() as td:
            mpath = Path(td) / "models-manifest.json"
            mpath.write_text(json.dumps({
                "pack_version": "1.0.0",
                "models": [
                    {
                        "name": "yolo11s-pose.pt",
                        "sha256": "43950efd914d79ba4a2ff40994f79496aa260a9f5d378ee091c667ce19d45863",
                        "min_size_bytes": 10000000,
                    },
                    {
                        "name": "yolo11n.pt",
                        "sha256": "0ebbc80d4a7680d14987a577cd21342b65ecfd94632bd9a8da63ae6417644ee1",
                        "min_size_bytes": 3000000,
                    },
                ],
            }))
            audit = h8.audit_models_manifest(mpath)
            self.assertEqual(audit["status"], "PASS")
            self.assertFalse(audit["model_redistribution_approved"])



class TestH8FailClosedRegressions(unittest.TestCase):
    def test_missing_explicit_models_cannot_pass_or_use_fake_weights(self):
        result = h8.run_synthetic_pipeline_test(
            python_bin=Path("/missing/python"),
            worker_script=Path("/missing/worker.py"),
            models_dir=None, test_device="cpu")
        self.assertEqual(result["status"], "BLOCKED")
        self.assertNotIn("marker", result)

    def test_missing_explicit_pack_and_candidates_blocks_certification(self):
        report = h8.run_certification(
            pack_dir=None, candidates_dir=None, models_dir=None, device="cpu")
        statuses = {check["id"]: check["status"] for check in report["checks"]}
        self.assertEqual(statuses["macho-python-runtime-minos"], "BLOCKED")
        self.assertEqual(statuses["macho-r9-ffmpeg-minos14"], "BLOCKED")
        self.assertEqual(statuses["worker-protocol-cpu"], "BLOCKED")
        self.assertFalse(report["runtime_ready"])
        self.assertFalse(report["inference_ready"])

    def test_evidence_scoping_no_person_detection_claim(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / "models").mkdir()
            # An absent model is blocked, not replaced with a fake success.
            result = h8.run_synthetic_pipeline_test(
                python_bin=root / "python3",
                worker_script=root / "worker.py",
                models_dir=root / "models", test_device="mps")
            self.assertEqual(result["status"], "BLOCKED")
            self.assertNotIn("health_response", result)


if __name__ == "__main__":
    unittest.main()
