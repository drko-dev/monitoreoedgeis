import hashlib
import json
import tempfile
import unittest
from pathlib import Path
import importlib.util

spec = importlib.util.spec_from_file_location("preflight", Path(__file__).with_name("check-runtime-pack-release.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ReleasePreflightTest(unittest.TestCase):
    def test_template_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            payload = json.dumps({"manifest_type": "template", "python": {"relative_binary_path":"python/bin/python3","sha256":"0"*64,"size_bytes":100}}).encode()
            (root / "runtime-manifest.json").write_bytes(payload)
            errs = module.check(root, hashlib.sha256(payload).hexdigest())
            self.assertTrue(any("template" in err for err in errs))
            self.assertTrue(any("missing artifact" in err for err in errs))
    def test_release_with_pinned_synthetic_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/"bin").mkdir()
            (root/"bin"/"ffmpeg").write_bytes(b"synthetic-only")
            (root/"bin"/"worker").write_bytes(b"worker")
            payload=json.dumps({"manifest_type":"release","worker":{"entrypoint":"bin/worker","sha256":hashlib.sha256(b"worker").hexdigest(),"size_bytes":6},"python":{"relative_binary_path":"bin/ffmpeg","sha256":hashlib.sha256(b"synthetic-only").hexdigest(),"size_bytes":14}}).encode()
            (root/"runtime-manifest.json").write_bytes(payload)
            self.assertEqual(module.check(root,hashlib.sha256(payload).hexdigest()),[])
            (root/"bin"/"ffmpeg").write_bytes(b"tampered")
            self.assertTrue(any("integrity" in e for e in module.check(root,hashlib.sha256(payload).hexdigest())))
    def test_missing_pin_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/"runtime-manifest.json").write_text('{"manifest_type":"release"}')
            self.assertTrue(any("pin" in e for e in module.check(root,"")))

if __name__ == "__main__":
    unittest.main()
