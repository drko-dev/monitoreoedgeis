import importlib.util
import unittest
from pathlib import Path

spec=importlib.util.spec_from_file_location("lab",Path(__file__).with_name("check-local-full-edge-lab.py"))
lab=importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)
class LabTests(unittest.TestCase):
 def test_host_tools_never_imply_pack_ready(self):
  root=Path(__file__).resolve().parents[2]
  r=lab.inspect(root,python_path="/usr/bin/python3",ffmpeg_path="/opt/homebrew/bin/ffmpeg",ffprobe_path="/opt/homebrew/bin/ffprobe",disk_free=100*1024**3)
  self.assertFalse(r["runtime_ready"])
  self.assertFalse(r["inference_ready"])
  self.assertFalse(r["pack_ready"])
  self.assertEqual(r["mode"],"LOCAL_LAB_ONLY")
  self.assertFalse(next(c for c in r["checks"] if c["id"]=="ffmpeg")["pass"])
 def test_low_disk_refuses_recommendation(self):
  r=lab.inspect(Path(__file__).resolve().parents[2],disk_free=4*1024**3)
  self.assertFalse(next(c for c in r["checks"] if c["id"]=="disk")["pass"])
if __name__=="__main__": unittest.main()
