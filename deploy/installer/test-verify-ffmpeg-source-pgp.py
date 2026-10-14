#!/usr/bin/env python3
"""Only ephemeral synthetic OpenPGP keypair and archive; NEVER ships real keys."""
import hashlib
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("ffmpeg_pgp", Path(__file__).with_name("verify-ffmpeg-source-pgp.py"))
pgp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pgp)

@unittest.skipUnless(shutil.which("gpg"), "GnuPG not installed; cannot perform PGP tests")
class FFmpegSourcePGPTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.session = tempfile.TemporaryDirectory()
        cls.root = Path(cls.session.name)
        cls.homedir = cls.root / "keyring"
        cls.homedir.mkdir(mode=0o700)
        cls.key = cls.root / "test-release-key.asc"
        cls.source = cls.root / "ffmpeg-test-release.tar.xz"
        cls.source.write_bytes(b"SYNTHETIC-FFMPEG-TARBALL-TEST-ONLY")
        cls.signature = cls.root / "test-release.asc"
        cls.source_sha = hashlib.sha256(cls.source.read_bytes()).hexdigest()
        cls.gpg = shutil.which("gpg")
        def run(*arguments):
            return subprocess.run([
                cls.gpg, "--homedir", str(cls.homedir), "--batch",
                "--no-tty", "--pinentry-mode", "loopback", "--passphrase", "",
                *arguments
            ], capture_output=True, text=True, timeout=30)
        created = run("--quick-generate-key",
                      "GEO CAM synthetic fixture <noreply@example.invalid>",
                      "ed25519", "sign", "0")
        if created.returncode:
            raise RuntimeError(f"test key generation failed: {created.stderr[-600:]}")
        listed = run("--with-colons", "--fingerprint")
        cls.fingerprint = next(
            line.split(":")[9] for line in listed.stdout.splitlines()
            if line.startswith("fpr:")
        ).upper()
        exported = run("--armor", "--export", cls.fingerprint)
        if exported.returncode:
            raise RuntimeError("test public key export failed")
        cls.key.write_text(exported.stdout)
        signed = run("--armor", "--detach-sign", "--output", str(cls.signature),
                     str(cls.source))
        if signed.returncode:
            raise RuntimeError("test signature generation failed")

    @classmethod
    def tearDownClass(cls):
        cls.session.cleanup()

    def verify(self, **overrides):
        arguments = dict(
            source=self.source, signature=self.signature,
            public_key=self.key, upstream_signature=self.signature,
            gpg=self.gpg, expected_fingerprint=self.fingerprint,
            expected_source_sha256=self.source_sha, require_filename=False,
        )
        arguments.update(overrides)
        return pgp.verify(**arguments)

    def test_synthetic_pgp_signed_archive_pass(self):
        result = self.verify()
        self.assertEqual(result["result"], "FFMPEG_SOURCE_PGP_VALID")
        self.assertTrue(result["signature_equal_to_official_copy"])
        self.assertFalse(result["signed_binary_verified"])
        self.assertFalse(result["redistribution_approved"])
        self.assertFalse(result["release_eligible"])

    def test_wrong_key_fingerprint_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "fingerprint"):
            self.verify(expected_fingerprint="0" * 40)

    def test_wrong_digest_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "SHA256"):
            self.verify(expected_source_sha256="0" * 64)

    def test_tampered_signature_fails_closed(self):
        temp = self.root / "tampered.asc"
        temp.write_bytes(self.signature.read_bytes() + b"broken")
        with self.assertRaisesRegex(ValueError, "detached signature differs"):
            self.verify(signature=temp)

    def test_cryptographically_wrong_detached_signature_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            wrong = Path(directory, "wrong.asc")
            wrong.write_text("-----BEGIN PGP SIGNATURE-----\ncorrupt\n-----END PGP SIGNATURE-----\n")
            with self.assertRaisesRegex(ValueError, "PGP signature invalid"):
                self.verify(signature=wrong, upstream_signature=None)

    def test_unavailable_gpg_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "GPG binary unavailable"):
            self.verify(gpg="/does-not-exist/geocam-gpg")

    def test_symlinked_source_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory, "link.tar.xz")
            target.symlink_to(self.source)
            with self.assertRaisesRegex(ValueError, "non-symlink"):
                self.verify(source=target)

    def test_unpinned_release_filename_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "not the pinned"):
            self.verify(require_filename=True)

if __name__ == "__main__":
    unittest.main()
