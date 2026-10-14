#!/usr/bin/env python3
"""Fail-closed OFFLINE PGP check of the *source tarball*, never binary/legal go.

The caller must separately obtain the public key and detached signature from
an authenticated FFmpeg publisher endpoint and review the expected key
fingerprint. Does NOT download, import into the user's keyring, or sign files.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile

RELEASE_FILENAME = "ffmpeg-8.1.3.tar.xz"
RELEASE_SHA256 = "7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3"
FFMPEG_RELEASE_KEY_FINGERPRINT = "FCF986EA15E6E293A5644F10B4322F04D67658D8"
UPSTREAM_KEY_URL = "https://ffmpeg.org/ffmpeg-devel.asc"
UPSTREAM_SIGNING_ID_REFERENCE = "https://ffmpeg.org/download.html"

def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as reader:
        for chunk in iter(lambda: reader.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

def regular_file(path):
    try:
        return stat.S_ISREG(path.lstat().st_mode)
    except OSError:
        return False

def gpg_run(gpg, homedir, args):
    try:
        return subprocess.run(
            [str(gpg), "--homedir", str(homedir), "--batch", "--no-tty",
             "--no-auto-key-retrieve", *args],
            capture_output=True, text=True, check=False, timeout=35,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ValueError(f"GPG invocation failed: {type(exc).__name__}") from exc

def primary_fingerprint(gpg, homedir, public_key):
    result = gpg_run(gpg, homedir, [
        "--with-colons", "--show-keys", "--fingerprint", str(public_key)])
    if result.returncode != 0:
        raise ValueError("cannot inspect public signing key")
    lines = [line.split(":") for line in result.stdout.splitlines()]
    primary = []
    reading_primary = False
    for parts in lines:
        if parts[0] == "pub":
            reading_primary = True
        elif parts[0] in ("sub", "ssb"):
            reading_primary = False
        elif parts[0] == "fpr" and reading_primary and len(parts) > 9:
            primary.append(parts[9].upper())
            reading_primary = False
    if len(primary) != 1:
        raise ValueError("expected exactly one primary release signing key")
    return primary[0]

def verify(source, signature, public_key, upstream_signature=None,
           gpg="gpg", expected_fingerprint=FFMPEG_RELEASE_KEY_FINGERPRINT,
           expected_source_sha256=RELEASE_SHA256,
           require_filename=True):
    source, signature, public_key = map(Path, (source, signature, public_key))
    if require_filename and source.name != RELEASE_FILENAME:
        raise ValueError("source archive is not the pinned FFmpeg release")
    for name, path in (("source", source), ("signature", signature), ("key", public_key)):
        if not regular_file(path):
            raise ValueError(f"{name} must be an existing non-symlink regular file")
    actual_sha = sha256_file(source)
    if actual_sha.lower() != expected_source_sha256.lower():
        raise ValueError("source SHA256 differs from verified FFmpeg release source")
    if upstream_signature is not None:
        upstream_signature = Path(upstream_signature)
        if not regular_file(upstream_signature):
            raise ValueError("upstream detached signature must be regular file")
        if sha256_file(signature) != sha256_file(upstream_signature):
            raise ValueError("detached signature differs from FFmpeg publisher copy")
    binary = shutil.which(gpg)
    if not binary:
        raise ValueError("GPG binary unavailable; signature NOT VERIFIED")
    with tempfile.TemporaryDirectory(prefix="geocam-ffmpeg-verify-") as directory:
        home = Path(directory, "gnupg")
        home.mkdir(mode=0o700)
        fpr = primary_fingerprint(binary, home, public_key)
        if fpr != expected_fingerprint.upper():
            raise ValueError("signing key fingerprint does not match pinned FFmpeg identity")
        result = gpg_run(binary, home, ["--import", str(public_key)])
        if result.returncode != 0:
            raise ValueError("could not import key into temporary verification-only keyring")
        result = gpg_run(binary, home, [
            "--status-fd", "1", "--verify", str(signature), str(source)])
        statuses = [
            line.removeprefix("[GNUPG:] ").split()
            for line in result.stdout.splitlines() if line.startswith("[GNUPG:] ")
        ]
        valid = [st for st in statuses if st and st[0] == "VALIDSIG"]
        good = [st for st in statuses if st and st[0] == "GOODSIG"]
        forbidden = {"BADSIG", "ERRSIG", "REVKEYSIG", "EXPKEYSIG",
                     "EXPSIG", "NO_PUBKEY", "NODATA"}
        if (result.returncode != 0 or len(valid) != 1 or len(good) != 1 or
            len(valid[0]) < 2 or valid[0][1].upper() != fpr or
            any(st and st[0] in forbidden for st in statuses)):
            raise ValueError("PGP signature invalid, expired, revoked or wrong release signer")
    return {
        "result": "FFMPEG_SOURCE_PGP_VALID",
        "release": RELEASE_FILENAME if require_filename else source.name,
        "source_sha256": actual_sha,
        "release_signer_primary_fingerprint": expected_fingerprint.upper(),
        "signature_equal_to_official_copy": upstream_signature is not None,
        "trusted_identity_reference": UPSTREAM_SIGNING_ID_REFERENCE,
        "provenance_scope": "signed-source-bytes-only-not-build-reproducibility",
        "signed_binary_verified": False,
        "redistribution_approved": False,
        "release_eligible": False,
    }

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--signature", type=Path, required=True)
    parser.add_argument("--publisher-public-key", type=Path, required=True)
    parser.add_argument("--publisher-signature", type=Path)
    parser.add_argument("--gpg", default="gpg")
    args = parser.parse_args()
    try:
        report = verify(args.source, args.signature, args.publisher_public_key,
                        args.publisher_signature, args.gpg)
    except ValueError as exc:
        print(json.dumps({"result": "NOT_VERIFIED", "reason": str(exc),
                          "redistribution_approved": False,
                          "release_eligible": False}, sort_keys=True))
        return 1
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
