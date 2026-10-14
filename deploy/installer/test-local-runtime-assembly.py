import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

script=Path(__file__).with_name("check-local-runtime-assembly.py")
spec=importlib.util.spec_from_file_location("local_assembly",script)
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class LocalAssemblyTests(unittest.TestCase):
    def test_empty_stage_fails_closed(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)
            (p/"internal/installer/assets").mkdir(parents=True)
            (p/"internal/installer/assets/runtime-manifest.json").write_text(json.dumps({"manifest_type":"template","python":{"version":"3.11.9"}}))
            (p/"internal/installer/assets/wheelhouse-lock.json").write_text(json.dumps({"dependencies":[]}))
            report=module.check(p,p/"lab",p/"bin")
            self.assertEqual(report["status"],"NOT_READY")
            self.assertFalse(report["runtime_ready"])
            self.assertFalse(report["inference_ready"])
            self.assertFalse(report["model_redistribution_approved"])
            self.assertTrue(any("template" in issue for issue in report["issues"]))
            self.assertTrue(any("ffmpeg missing" in issue for issue in report["issues"]))

if __name__=="__main__":
    unittest.main()
