import importlib.util
import unittest
from pathlib import Path
spec=importlib.util.spec_from_file_location("licenseaudit",Path(__file__).with_name("runtime-pack-license-audit.py"))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
class Tests(unittest.TestCase):
    def test_all_declared_wheels_unapproved(self):
        a=module.audit(Path(__file__).resolve().parents[2])
        self.assertEqual(a["wheel_count"],36)
        self.assertFalse(a["release_eligible"])
        self.assertTrue(all(not x["legal_go"] and not x["license_verified_from_distribution"] for x in a["wheels"]))
        self.assertTrue(any(x["name"]=="ultralytics" and x["needs_special_review"] for x in a["wheels"]))
        self.assertTrue(any(x["component"]=="ffmpeg/ffprobe" and x["status"]=="BLOCKED" for x in a["additional_review"]))
if __name__=="__main__":unittest.main()
