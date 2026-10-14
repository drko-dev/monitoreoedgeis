#!/usr/bin/env python3
"""H6D-R9 synthetic minimum-version audit, no actual macOS required."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "mac_minos", Path(__file__).with_name("audit-full-edge-macos-minos.py"))
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)

class MinOSMatrixTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.pack=self.root/"pack"
        self.pack.joinpath("ffmpeg").mkdir(parents=True)
        self.pack.joinpath("python/lib").mkdir(parents=True)
        self.candidate=self.root/"candidate"
        self.candidate.mkdir()
        self.versions={
            "ffmpeg":(27,0), "ffprobe":(27,0),
            "ffi.so":(14,0),
            "ffmpeg-1":(14,0),"ffprobe-1":(14,0),
        }
        for path in (
            self.pack/"ffmpeg/ffmpeg",
            self.pack/"ffmpeg/ffprobe",
            self.pack/"python/lib/ffi.so",
            self.candidate/"ffmpeg-1",
            self.candidate/"ffprobe-1",
        ):
            path.write_bytes(bytes.fromhex("cffaedfe")+b"synthetic Mach-O")
    def mocked_minos(self,path):
        return self.versions.get(path.name)
    def report(self,candidate=False,baseline=(14,0)):
        return audit.scan_pack(self.pack,baseline,
                               self.candidate if candidate else None,
                               self.mocked_minos)

    def test_parse_valid_minimum_version(self):
        self.assertEqual(audit.minimum_version(
            "Load command 4\n    minos 14.0\n sdk 27.0"),(14,0))
    def test_parse_missing_or_ambiguous_fails(self):
        self.assertIsNone(audit.minimum_version("minos missing"))
        self.assertIsNone(audit.minimum_version("minos 14.0\n minos 15.0"))
    def test_original_r4_larger_than_baseline(self):
        r=self.report()
        self.assertEqual(r["audited_macho_files"],3)
        self.assertEqual(r["violations_count"],2)
        self.assertEqual(r["declared_minos_counts"],{"14.0":1,"27.0":2})
        self.assertFalse(r["macho_minimums_within_baseline"])
        self.assertFalse(r["release_eligible"])
    def test_candidate_replacement_inspection_not_installed(self):
        r=self.report(True)
        self.assertTrue(r["macho_minimums_within_baseline"])
        self.assertEqual(r["declared_minos_counts"],{"14.0":3})
        self.assertEqual(r["substituted_entries"],["ffmpeg/ffmpeg","ffmpeg/ffprobe"])
        self.assertFalse(r["clean_machine_tested"])
        self.assertFalse(r["full_dependency_api_compatibility_tested"])
        self.assertFalse(r["release_eligible"])
    def test_cannot_claim_macos_13_when_python_requires_14(self):
        self.assertFalse(self.report(True,(13,0))["macho_minimums_within_baseline"])
    def test_missing_candidate_fails_closed(self):
        (self.candidate/"ffmpeg-1").unlink()
        self.assertFalse(self.report(True)["macho_minimums_within_baseline"])
    def test_unreadable_buildversion_fails_closed(self):
        self.versions["ffi.so"]=None
        self.assertFalse(self.report(True)["macho_minimums_within_baseline"])
    def test_higher_native_minimum_fails_closed(self):
        self.versions["ffi.so"]=(14,1)
        self.assertFalse(self.report(True)["macho_minimums_within_baseline"])
    def test_symlink_pack_root_rejected(self):
        link=self.root/"linked"
        link.symlink_to(self.pack)
        with self.assertRaisesRegex(ValueError,"non-symlink"):
            audit.scan_pack(link)
    def test_symlink_candidate_root_rejected(self):
        link=self.root/"linked"
        link.symlink_to(self.candidate)
        with self.assertRaisesRegex(ValueError,"non-symlink"):
            audit.scan_pack(self.pack,(14,0),link)
    def test_ignores_regular_non_macho_file(self):
        (self.pack/"python/lib/text.py").write_text("hello")
        self.assertEqual(self.report(True)["audited_macho_files"],3)

if __name__=="__main__":
    unittest.main()
