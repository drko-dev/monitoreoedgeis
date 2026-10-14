import importlib.util
import unittest
from pathlib import Path

spec=importlib.util.spec_from_file_location("notices", Path(__file__).with_name("generate-third-party-notices.py"))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class NoticeTests(unittest.TestCase):
    def test_declared_wheels_and_caveat(self):
        source=module.notice_text(Path(__file__).resolve().parents[2])
        self.assertEqual(source.count("UNVERIFIED |"),36)
        self.assertIn("ultralytics",source)
        self.assertIn("AGPL-3.0",source)
        self.assertIn("FFmpeg",source)
        self.assertIn("DRAFT / NOT FOR RELEASE",source)
        self.assertIn("No redistribution is authorized",source)

if __name__=="__main__":
    unittest.main()
