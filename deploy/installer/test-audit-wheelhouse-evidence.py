#!/usr/bin/env python3
"""Synthetic negative cases for H6D read-only wheel metadata evidence."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

SPEC = importlib.util.spec_from_file_location(
    "wheel_evidence", Path(__file__).with_name("audit-wheelhouse-evidence.py")
)
audit = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(audit)

class WheelEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.TemporaryDirectory()
        self.addCleanup(self.root.cleanup)
        self.repo = Path(self.root.name, "repo")
        self.wheels = Path(self.root.name, "wheels")
        (self.repo / "internal/installer/assets").mkdir(parents=True)
        self.wheels.mkdir()
        self.filename = "demo-1.2.3-py3-none-any.whl"
        self.path = self.wheels / self.filename
        self.make_wheel()
        self.write_lock()

    def make_wheel(self, extra=None, name="demo", version="1.2.3", license_file=True):
        with zipfile.ZipFile(self.path, "w", compression=zipfile.ZIP_DEFLATED) as z:
            z.writestr("demo-1.2.3.dist-info/METADATA",
                       f"Metadata-Version: 2.3\nName: {name}\nVersion: {version}\nLicense-Expression: MIT\n\n")
            if license_file:
                z.writestr("demo-1.2.3.dist-info/licenses/LICENSE", "MIT test notice")
            if extra:
                for key, value in extra.items():
                    z.writestr(key, value)

    def write_lock(self):
        self.repo.joinpath("internal/installer/assets/wheelhouse-lock.json").write_text(
            json.dumps({"dependencies": [{
                "name": "demo", "version": "1.2.3",
                "filename": self.filename, "license": "MIT",
                "origin": "https://invalid.example/test",
                "size_bytes": self.path.stat().st_size,
                "sha256": audit.sha256_file(self.path),
            }]}))

    def report(self):
        return audit.audit(self.repo, self.wheels)

    def test_actual_archive_evidence_no_legal_approval(self):
        result = self.report()
        self.assertTrue(result["technical_inventory_complete"])
        self.assertEqual(result["hash_verified_wheels"], 1)
        self.assertEqual(result["with_license_text"], 1)
        self.assertFalse(result["legal_go"])
        self.assertFalse(result["release_eligible"])
        wheel = result["wheels"][0]
        self.assertEqual(wheel["license_expression_in_metadata"], "MIT")
        self.assertFalse(wheel["human_legal_review_complete"])
        self.assertFalse(wheel["signed_origin_verified"])

    def test_tampered_wheel_fails_sha(self):
        with self.path.open("ab") as f:
            f.write(b"x")
        result = self.report()
        self.assertFalse(result["technical_inventory_complete"])
        self.assertEqual(result["hash_verified_wheels"], 0)

    def test_wheel_name_version_mismatch_fails(self):
        self.make_wheel(name="impostor")
        self.write_lock()
        self.assertFalse(self.report()["technical_inventory_complete"])

    def test_malicious_zip_member_fails(self):
        self.make_wheel(extra={"../escape": "payload"})
        self.write_lock()
        self.assertFalse(self.report()["technical_inventory_complete"])

    def test_no_license_notice_is_reported_not_assumed(self):
        self.make_wheel(license_file=False)
        self.write_lock()
        result = self.report()
        self.assertTrue(result["technical_inventory_complete"])
        self.assertEqual(result["without_license_text"], 1)

    def test_unexpected_wheel_fails(self):
        (self.wheels / "extra.whl").write_bytes(b"x")
        result = self.report()
        self.assertFalse(result["technical_inventory_complete"])
        self.assertEqual(result["unexpected_wheel_files"], ["extra.whl"])

    def test_missing_wheel_fails(self):
        self.path.unlink()
        self.assertFalse(self.report()["technical_inventory_complete"])

    def test_symlinked_wheel_fails(self):
        target = self.wheels / "payload"
        self.path.rename(target)
        self.path.symlink_to(target)
        result = self.report()
        self.assertFalse(result["technical_inventory_complete"])
        self.assertIn("non-symlink", result["wheels"][0]["error"])

if __name__ == "__main__":
    unittest.main()
