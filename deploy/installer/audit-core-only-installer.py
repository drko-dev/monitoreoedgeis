#!/usr/bin/env python3
from __future__ import annotations
"""Read-only archive member audit for GEO CAM's model-free installer profile.

This only checks declared archive ENTRY NAMES/types, not embedded executable
resources, source-license obligations, notarization, runtime readiness or
model provenance. Do not treat PASS as commercial distribution approval.
"""
import argparse
import json
import re
import stat
import sys
import tarfile
import zipfile
from pathlib import Path, PurePosixPath

BLOCKED_SUFFIXES = (
    ".pt", ".pth", ".onnx", ".safetensors", ".ckpt", ".engine",
    ".tflite", ".mlmodel", ".mlpackage", ".torchscript", ".whl",
    ".tar.gz", ".tgz", ".tar.xz", ".7z", ".zip",
)
BLOCKED_COMPONENTS = {
    "ultralytics", "ffmpeg", "ffprobe", "site-packages",
    "wheelhouse", ".wheelhouse", "runtime-pack", "runtime-packs",
    "runtime_pack", "runtime_packs", "models", "model-weights",
    "model_weights",
}
CONTROL_CHAR = re.compile(r"[\x00-\x1f\x7f]")
MAX_MEMBERS = 100000


def safe_member_name(name: str) -> tuple[bool, str]:
    if (not isinstance(name, str) or not name or CONTROL_CHAR.search(name)
            or "\\" in name or name.startswith("/") or "//" in name):
        return False, "absolute, empty, invalid separator or control characters"
    parts = name.rstrip("/").split("/")
    if not parts or any(p in ("", ".", "..") for p in parts):
        return False, "unsafe archive relative path"
    return True, ""


def blocked_member_name(name: str) -> str | None:
    clean = name.rstrip("/").lower()
    parts = clean.split("/")
    if any(segment in BLOCKED_COMPONENTS for segment in parts):
        return "Full Edge/model/dependency component in core-only installer"
    if any(clean.endswith(ext) for ext in BLOCKED_SUFFIXES):
        return "model weights or nested dependency archive in core-only installer"
    if any(part.endswith(".whl") for part in parts):
        return "wheel dependency in core-only installer"
    return None


def audit_archive(platform: str, path: Path) -> dict:
    result = {
        "profile": "CORE_ONLY_PATH_AUDIT_NOT_LEGAL_CERTIFICATION",
        "platform": platform,
        "members_checked": 0,
        "opaque_executable_payload_unverified": True,
        "approved_for_release": False,
        "model_redistribution_approved": False,
        "violations": [],
    }

    def violation(message: str) -> None:
        if len(result["violations"]) < 24:
            result["violations"].append(message)

    def verify_member(name: str, unsafe_type: bool = False) -> None:
        result["members_checked"] += 1
        if result["members_checked"] > MAX_MEMBERS:
            violation("archive has too many entries")
            return
        safe, why = safe_member_name(name)
        if not safe:
            violation(f"unsafe path {name!r}: {why}")
            return
        if unsafe_type:
            violation(f"unsupported archive member type: {name!r}")
        why = blocked_member_name(name)
        if why:
            violation(f"{name!r}: {why}")

    try:
        if platform.startswith("linux-"):
            with tarfile.open(path, "r:gz") as archive:
                for member in archive:
                    verify_member(member.name, not (member.isfile() or member.isdir()))
                    if result["members_checked"] > MAX_MEMBERS:
                        break
        elif platform == "macos-arm64":
            with zipfile.ZipFile(path) as archive:
                for member in archive.infolist():
                    unix_mode = member.external_attr >> 16
                    special = (stat.S_IFMT(unix_mode) not in (0, stat.S_IFDIR, stat.S_IFREG))
                    verify_member(member.filename, special)
                    if result["members_checked"] > MAX_MEMBERS:
                        break
        elif platform == "windows-amd64":
            # PE is opaque, not an archive with inspectable member paths.
            # This is a build-chain/profile check, NOT proof a PE cannot
            # embed arbitrary weight bytes. The assembler already validates
            # architecture and the immutable BUILD_INFO file digest.
            with path.open("rb") as stream:
                if stream.read(2) != b"MZ":
                    violation("expected Windows PE MZ header")
        else:
            violation("unknown platform")
    except (OSError, tarfile.TarError, zipfile.BadZipFile, ValueError) as exc:
        violation(f"cannot inspect package: {type(exc).__name__}: {exc}")

    result["path_inventory_passed"] = not result["violations"]
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("platform", choices=(
        "windows-amd64", "macos-arm64", "linux-amd64", "linux-arm64"))
    parser.add_argument("package", type=Path)
    args = parser.parse_args(argv)
    result = audit_archive(args.platform, args.package)
    print(json.dumps(result, sort_keys=True))
    return 0 if result["path_inventory_passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
