#!/usr/bin/env python3
import importlib.util
import io
import json
import os
import stat
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path

audit_path = Path(__file__).with_name("audit-core-only-installer.py")
spec = importlib.util.spec_from_file_location("core_only_audit", audit_path)
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


def make_zip(path, names):
    with zipfile.ZipFile(path, "w") as out:
        for name in names:
            out.writestr(name, b"fixture")


def make_tar(path, names):
    with tarfile.open(path, "w:gz") as out:
        for name in names:
            info = tarfile.TarInfo(name)
            data = b"fixture"
            info.size = len(data)
            out.addfile(info, io.BytesIO(data))


class CoreOnlyAuditTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def archive(self, platform, names):
        p = self.root / ("installer.zip" if platform == "macos-arm64" else "installer.tar.gz")
        if platform == "macos-arm64":
            make_zip(p, names)
        else:
            make_tar(p, names)
        return audit.audit_archive(platform, p)

    def test_normal_core_only_packages_pass_without_claiming_rights(self):
        names = ["geocam-edge-ui.app/Contents/MacOS/geocam-edge-ui",
                 "geocam-edge-ui.app/Contents/Info.plist"]
        r = self.archive("macos-arm64", names)
        self.assertTrue(r["path_inventory_passed"], r)
        self.assertFalse(r["approved_for_release"])
        self.assertFalse(r["model_redistribution_approved"])
        self.assertTrue(r["opaque_executable_payload_unverified"])

    def test_linux_package_accepts_core_binary(self):
        r = self.archive("linux-arm64", [
            "geocam-edge-installer-1.2.3-linux-arm64/geocam-edge-ui",
            "geocam-edge-installer-1.2.3-linux-arm64/README.txt",
        ])
        self.assertTrue(r["path_inventory_passed"], r)

    def test_rejects_model_weights_and_unlicensed_dependencies(self):
        for platform in ("macos-arm64", "linux-amd64"):
            for name in ("models/yolo11n.pt", "weights/model.onnx",
                         "wheelhouse/ultralytics-8.3.1.whl",
                         "runtime-pack/python3", "bin/ffmpeg",
                         "site-packages/ultralytics/__init__.py",
                         "packed/dependencies.tar.gz", "assets/vision.mlmodel"):
                with self.subTest(platform=platform, name=name):
                    r = self.archive(platform, ["normal/app", name])
                    self.assertFalse(r["path_inventory_passed"], r)
                    self.assertTrue(r["violations"])

    def test_rejects_archive_path_traversal(self):
        for name in ("../weights.pt", "/etc/passwd", "./app", "app//inside",
                     "dir/../inside", "app\\bad"):
            with self.subTest(path=name):
                r = self.archive("linux-amd64", [name])
                self.assertFalse(r["path_inventory_passed"], r)

    def test_rejects_nul_in_unserialized_archive_name(self):
        # tarfile truncates NUL at serialization; validate the raw name
        # directly rather than incorrectly expecting the parser to preserve it.
        safe, _ = audit.safe_member_name("app\x00evil")
        self.assertFalse(safe)

    def test_rejects_symlink_in_tar(self):
        p = self.root / "bad.tar.gz"
        with tarfile.open(p, "w:gz") as out:
            info = tarfile.TarInfo("safe/worker")
            info.type = tarfile.SYMTYPE
            info.linkname = "../../bad"
            out.addfile(info)
        r = audit.audit_archive("linux-amd64", p)
        self.assertFalse(r["path_inventory_passed"], r)

    def test_rejects_symlink_in_zip(self):
        p = self.root / "bad.zip"
        with zipfile.ZipFile(p, "w") as out:
            info = zipfile.ZipInfo("geocam-edge-ui.app/Contents/MacOS/host-python")
            info.create_system = 3
            info.external_attr = (stat.S_IFLNK | 0o777) << 16
            out.writestr(info, "../../python")
        r = audit.audit_archive("macos-arm64", p)
        self.assertFalse(r["path_inventory_passed"], r)

    def test_opaque_windows_payload_never_proves_legality(self):
        exe = self.root / "test.exe"
        exe.write_bytes(b"MZ" + b"fake-pe")
        r = audit.audit_archive("windows-amd64", exe)
        self.assertTrue(r["path_inventory_passed"])
        self.assertTrue(r["opaque_executable_payload_unverified"])
        self.assertFalse(r["approved_for_release"])

    def test_garbage_windows_executable_rejected(self):
        exe = self.root / "test.exe"
        exe.write_bytes(b"not a PE")
        self.assertFalse(audit.audit_archive("windows-amd64", exe)["path_inventory_passed"])


if __name__ == "__main__":
    unittest.main()
