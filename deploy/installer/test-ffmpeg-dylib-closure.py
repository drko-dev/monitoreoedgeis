import importlib.util
import tempfile
import unittest
from pathlib import Path

spec=importlib.util.spec_from_file_location("closure",Path(__file__).with_name("check-ffmpeg-dylib-closure.py"))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ClosureTests(unittest.TestCase):
    def test_missing_binaries_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            result=module.audit(Path(directory))
            self.assertFalse(result["pass"])
            self.assertEqual(len(result["problems"]),2)

if __name__=="__main__":
    unittest.main()
