#!/usr/bin/env python3
"""H6D selected-artifact preflight negative cases (synthetic; not legal review)."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("release_preflight", Path(__file__).with_name("check-runtime-pack-release.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ReleasePreflightTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for subdir in ("bin", "worker", "wheels"):
            (self.root / subdir).mkdir()
        self.files = {
            "bin/python3": b"test-python",
            "worker/worker.py": b"test-worker",
            "worker/backend.py": b"test-backend",
            "bin/ffmpeg": b"test-ffmpeg",
            "bin/ffprobe": b"test-ffprobe",
            "wheels/demo.whl": b"test-wheel",
        }
        for rel, data in self.files.items():
            (self.root / rel).write_bytes(data)
        def declaration(path, **extra):
            data = self.files[path]
            return dict(
                **extra, relative_path=path, size_bytes=len(data),
                sha256=hashlib.sha256(data).hexdigest(),
            )
        self.manifest = {
            "manifest_type": "release",
            "python": declaration("bin/python3"),
            "worker": {
                "entrypoint": "worker/worker.py",
                "sha256": hashlib.sha256(self.files["worker/worker.py"]).hexdigest(),
                "size_bytes": len(self.files["worker/worker.py"]),
                "backend_relative_path": "worker/backend.py",
                "backend_sha256": hashlib.sha256(self.files["worker/backend.py"]).hexdigest(),
                "backend_size_bytes": len(self.files["worker/backend.py"]),
            },
            "binaries": [
                declaration("bin/ffmpeg", name="ffmpeg"),
                declaration("bin/ffprobe", name="ffprobe"),
            ],
            "dependencies": [declaration("wheels/demo.whl", name="demo")],
        }
        self.pin = ""
        self.write_manifest()

    def write_manifest(self):
        raw = json.dumps(self.manifest, sort_keys=True).encode()
        (self.root / "runtime-manifest.json").write_bytes(raw)
        self.pin = hashlib.sha256(raw).hexdigest()

    def errors(self, pin=None):
        return module.check(self.root, self.pin if pin is None else pin)

    def test_complete_synthetic_selected_artifacts_precheck_pass(self):
        self.assertEqual([], self.errors())

    def test_template_fails_closed(self):
        self.manifest["manifest_type"] = "template"
        self.write_manifest()
        self.assertTrue(any("template" in x for x in self.errors()))

    def test_pin_required_and_mismatch(self):
        self.assertTrue(any("pin" in x for x in self.errors("")))
        self.assertTrue(any("pin" in x for x in self.errors("0"*64)))

    def test_missing_or_unpinned_backend_fails(self):
        self.manifest["worker"].pop("backend_sha256")
        self.write_manifest()
        self.assertTrue(any("backend" in x for x in self.errors()))

    def test_backend_integrity_tampering_fails(self):
        (self.root / "worker/backend.py").write_bytes(b"tampered")
        self.assertTrue(any("integrity" in x and "backend" in x for x in self.errors()))

    def test_missing_ffprobe_fails(self):
        self.manifest["binaries"] = self.manifest["binaries"][:1]
        self.write_manifest()
        self.assertTrue(any("ffprobe" in x for x in self.errors()))

    def test_no_wheels_fails(self):
        self.manifest.pop("dependencies")
        self.write_manifest()
        self.assertTrue(any("dependencies" in x for x in self.errors()))

    def test_path_traversal_rejected(self):
        self.manifest["dependencies"][0]["relative_path"] = "../outside.whl"
        self.write_manifest()
        self.assertTrue(any("unsafe" in x for x in self.errors()))

    def test_symlink_in_backend_path_rejected(self):
        dest = self.root / "worker/backend.actual"
        (self.root / "worker/backend.py").rename(dest)
        (self.root / "worker/backend.py").symlink_to(dest)
        self.assertTrue(any("symlink" in x for x in self.errors()))

    def test_duplicate_artifact_rejected(self):
        self.manifest["dependencies"][0]["relative_path"] = "bin/ffprobe"
        self.write_manifest()
        self.assertTrue(any("duplicate" in x for x in self.errors()))

    def test_models_present_rejected(self):
        (self.root / "models").mkdir()
        self.assertTrue(any("model distribution" in x for x in self.errors()))

if __name__ == "__main__":
    unittest.main()
