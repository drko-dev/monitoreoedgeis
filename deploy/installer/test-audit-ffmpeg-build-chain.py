#!/usr/bin/env python3
"""H6D-R8 synthetic chain verification; no FFmpeg build or artifact copying."""
import hashlib
import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "h6d_build", Path(__file__).with_name("audit-ffmpeg-build-chain.py"))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

class FFmpegBuildChainTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.lab = Path(self.temp.name)
        self.sources = self.lab/"ffmpeg-source"
        self.tree = self.sources/"ffmpeg-8.1.3"
        self.tree.joinpath("libavcodec").mkdir(parents=True)
        self.tree.joinpath("libavcodec", "h264.c").write_bytes(b"synthetic h264 source")
        self.archive = self.sources/"ffmpeg-8.1.3.tar.xz"
        with tarfile.open(self.archive,"w:xz") as tar:
            tar.add(self.tree/"libavcodec/h264.c",
                    arcname="ffmpeg-8.1.3/libavcodec/h264.c")
        self.expected_source = mod.sha256_file(self.archive)
        self.portable = self.lab/"ffmpeg-portable"
        self.pack = self.lab/"packs"/"lab"
        self.portable.mkdir()
        self.pack.joinpath("ffmpeg").mkdir(parents=True)
        self.expected_bins = {}
        for name in ("ffmpeg","ffprobe"):
            payload=("synthetic-"+name).encode()
            self.expected_bins[name]=hashlib.sha256(payload).hexdigest()
            for out in (self.tree/name,self.portable/name,self.pack/"ffmpeg"/name):
                out.write_bytes(payload)
        self.log = self.sources/"build.log"
        self.log.write_text("LD\tffmpeg_g\nLD\tffprobe_g\nSTRIP\tffmpeg\nSTRIP\tffprobe\n")
        self.configure = self.sources/"configure.log"
        self.configure.write_text("License: LGPL version 2.1 or later\nnetwork support           no\n")
        self.minos="11.0"
        self.ext_dylib=False
        self.gpl=False

    def runner(self,command):
        if "-archs" in command:
            return "arm64\n"
        if "-L" in command:
            dep="/opt/homebrew/lib/libavcodec.dylib" if self.ext_dylib else "/usr/lib/libSystem.B.dylib"
            return "path:\n\t"+dep+" (compatibility version 1.0.0)\n"
        if "-show-build" in command:
            return "Load command 11\n  cmd LC_BUILD_VERSION\n platform MACOS\n minos "+self.minos+"\n"
        if "-buildconf" in command:
            flags=mod.REQUIRED_FLAGS|({"--enable-gpl"} if self.gpl else set())
            return "configuration:\n"+"\n".join("    "+f for f in sorted(flags))
        raise AssertionError(command)

    def report(self):
        return mod.audit(
            self.archive,self.tree,self.log,self.configure,
            self.portable,self.pack,max_minos=(11,0),
            expected_source_sha256=self.expected_source,
            expected_bin_sha256=self.expected_bins,
            runner=self.runner
        )

    def test_consistent_synthetic_chain_never_enables_release(self):
        result=self.report()
        self.assertTrue(result["source_tree"]["all_archived_source_files_match"])
        self.assertTrue(result["technical_consistency_pass"])
        self.assertTrue(result["baseline_portability_pass"])
        for field in ("reproducible_build_verified","release_codesign_verified",
                      "legal_redistribution_approved","release_eligible"):
            self.assertFalse(result[field])

    def test_native_deployment_version_27_exceeds_11_baseline(self):
        self.minos="27.0"
        report=self.report()
        self.assertTrue(report["technical_consistency_pass"])
        self.assertFalse(report["baseline_portability_pass"])
        self.assertTrue(all(b["macos_minos"]=="27.0" for b in report["binaries"]))

    def test_mismatched_source_archive_sha_denied(self):
        self.assertFalse(mod.source_match(self.archive,self.tree,"0"*64).get(
            "all_archived_source_files_match", False))

    def test_modified_source_tree_file_denied(self):
        (self.tree/"libavcodec/h264.c").write_bytes(b"synthetic tamper")
        self.assertFalse(self.report()["source_tree"]["all_archived_source_files_match"])

    def test_symlinked_tree_source_denied(self):
        source=self.tree/"libavcodec/h264.c"
        target=self.lab/"elsewhere.c"
        source.rename(target)
        source.symlink_to(target)
        self.assertFalse(self.report()["source_tree"]["all_archived_source_files_match"])

    def test_tampered_pack_binary_denied(self):
        (self.pack/"ffmpeg/ffprobe").write_bytes(b"mutated")
        self.assertFalse(self.report()["technical_consistency_pass"])

    def test_homebrew_dynamic_dependency_denied(self):
        self.ext_dylib=True
        self.assertFalse(self.report()["baseline_portability_pass"])

    def test_gpl_or_codec_build_flag_denied(self):
        self.gpl=True
        self.assertFalse(self.report()["baseline_portability_pass"])

    def test_incomplete_build_log_denied(self):
        self.log.write_text("LD\tffmpeg_g\n")
        self.assertFalse(self.report()["technical_consistency_pass"])

    def test_missing_binary_denied(self):
        (self.tree/"ffmpeg").unlink()
        self.assertFalse(self.report()["technical_consistency_pass"])

    def test_unpinned_archive_name_roots_denied(self):
        with tarfile.open(self.archive,"w:xz") as tar:
            tar.add(self.tree/"libavcodec/h264.c",arcname="wrong/libavcodec/h264.c")
        self.expected_source=mod.sha256_file(self.archive)
        self.assertFalse(self.report()["source_tree"]["all_archived_source_files_match"])

if __name__=="__main__":
    unittest.main()
