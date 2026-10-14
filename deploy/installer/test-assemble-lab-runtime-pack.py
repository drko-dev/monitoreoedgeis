#!/usr/bin/env python3
"""Synthetic tests for the H6D lab assembler; no product artifacts are built."""

import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).with_name("assemble-lab-runtime-pack.py")
SPEC = importlib.util.spec_from_file_location("lab_assembler", MODULE_PATH)
ASSEMBLER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ASSEMBLER)


class LabAssemblerTests(unittest.TestCase):
    def test_hardlink_tree_reuses_bytes_and_preserves_internal_symlink(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "source"
            target = root / "target"
            source.mkdir()
            (source / "tool").write_text("synthetic")
            os.chmod(source / "tool", 0o755)
            os.symlink("tool", source / "tool-link")

            ASSEMBLER.hardlink_tree(source, target)

            self.assertEqual(os.stat(source / "tool").st_ino, os.stat(target / "tool").st_ino)
            self.assertTrue((target / "tool-link").is_symlink())
            self.assertEqual((target / "tool-link").read_text(), "synthetic")
            self.assertTrue(os.access(target / "tool", os.X_OK))

    def test_rejects_external_and_broken_symlinks(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "source"
            source.mkdir()
            external = root / "outside"
            external.write_text("outside")
            os.symlink(external, source / "escape")
            with self.assertRaisesRegex(ValueError, "absolute symlink rejected"):
                ASSEMBLER.validate_tree(source)

            (source / "escape").unlink()
            os.symlink("missing", source / "broken")
            with self.assertRaises(FileNotFoundError):
                ASSEMBLER.validate_tree(source)

    def test_rejects_special_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "source"
            root.mkdir()
            os.mkfifo(root / "pipe")
            with self.assertRaisesRegex(ValueError, "special file rejected"):
                ASSEMBLER.validate_tree(root)

    def test_wheel_inventory_requires_exact_hash_and_size(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            wheelhouse = root / "wheelhouse"
            wheelhouse.mkdir()
            payload = b"wheel-fixture"
            (wheelhouse / "one.whl").write_bytes(payload)
            lock = root / "lock.json"
            lock.write_text(json.dumps({"dependencies": [{
                "name": "fixture", "version": "1", "filename": "one.whl",
                "size_bytes": len(payload), "sha256": ASSEMBLER.hashlib.sha256(payload).hexdigest(),
            }]}))
            with self.assertRaisesRegex(ValueError, "expected the locked 36 wheels"):
                ASSEMBLER.wheel_records(lock, wheelhouse)
            lock.write_text(json.dumps({"dependencies": [{
                "name": "fixture", "version": "1", "filename": "one.whl",
                "size_bytes": len(payload) + 1, "sha256": ASSEMBLER.hashlib.sha256(payload).hexdigest(),
            }]}))
            with self.assertRaisesRegex(ValueError, "wheel hash/size mismatch"):
                ASSEMBLER.wheel_records(lock, wheelhouse)

    def test_pack_id_rejects_path_traversal_before_any_output(self):
        with tempfile.TemporaryDirectory() as temp:
            repo = Path(temp)
            lab = repo / "runtime-lab"
            lab.mkdir()
            with self.assertRaisesRegex(ValueError, "pack id"):
                ASSEMBLER.assemble(repo, lab, "../release", [])
            self.assertFalse((repo / "release").exists())


if __name__ == "__main__":
    unittest.main()
