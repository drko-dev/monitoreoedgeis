#!/usr/bin/env python3
"""Read-only H6D release eligibility preflight: no downloads or mutations."""
import argparse
import hashlib
import json
import os
from pathlib import Path

def check(pack: Path, pin: str) -> list[str]:
    failures = []
    manifest_file = pack / "runtime-manifest.json"
    try:
        raw = manifest_file.read_bytes()
        manifest = json.loads(raw)
    except (OSError, ValueError) as exc:
        return [f"manifest unavailable/invalid: {exc}"]
    if manifest.get("manifest_type") != "release":
        failures.append("manifest_type must be release, not template")
    if not pin or len(pin) != 64 or any(c not in "0123456789abcdefABCDEF" for c in pin):
        failures.append("trusted external 64-character SHA256 pin required")
    elif hashlib.sha256(raw).hexdigest() != pin.lower():
        failures.append("manifest does not match external trusted pin")
    if (pack / "models").exists():
        failures.append("model distribution is not approved; models directory present")
    artifacts = [manifest.get("python", {}), manifest.get("worker", {})]
    artifacts += manifest.get("binaries", [])
    artifacts += manifest.get("dependencies", [])
    seen = set()
    for item in artifacts:
        rel = item.get("relative_binary_path") or item.get("entrypoint") or item.get("relative_path")
        if not isinstance(rel, str) or not rel:
            failures.append("artifact lacks relative path")
            continue
        path = Path(rel)
        if path.is_absolute() or ".." in path.parts or rel in seen:
            failures.append(f"unsafe/duplicate artifact path: {rel}")
            continue
        seen.add(rel)
        actual = pack / path
        if any(p.is_symlink() for p in [pack, *list(actual.parents)[:len(path.parts)], actual]):
            failures.append(f"symlink component forbidden: {rel}")
            continue
        if not actual.is_file():
            failures.append(f"missing artifact: {rel}")
            continue
        expected_hash = item.get("sha256")
        expected_size = item.get("size_bytes")
        data_hash = hashlib.sha256()
        with actual.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                data_hash.update(chunk)
        if data_hash.hexdigest() != expected_hash or actual.stat().st_size != expected_size:
            failures.append(f"artifact integrity mismatch: {rel}")
    if len(artifacts) == 0:
        failures.append("empty artifact manifest")
    return failures

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("pack", type=Path)
    parser.add_argument("--trusted-manifest-sha256", default="")
    args = parser.parse_args()
    failures = check(args.pack, args.trusted_manifest_sha256)
    if failures:
        for failure in failures:
            print(f"NO-GO: {failure}")
        raise SystemExit(1)
    print("INTEGRITY PRECHECK PASS (NOT LICENSE / SIGNATURE / OFFLINE CERTIFICATION)")
