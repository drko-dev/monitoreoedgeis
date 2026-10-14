#!/usr/bin/env python3
"""H6D selected-artifact release preflight: NO legal, signature or airgap approval.

Read-only. A caller-provided digest does not establish an independently trusted
publisher pin; the embedding release must authenticate it outside this pack.
This only checks explicitly referenced files, not every packed runtime file.
"""
import argparse
import hashlib
import json
import stat
from pathlib import Path, PurePosixPath

def digest_file(path):
    h = hashlib.sha256()
    with path.open("rb") as reader:
        for block in iter(lambda: reader.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()

def safe_relative(rel):
    if not isinstance(rel, str) or not rel or "\\" in rel or "\x00" in rel:
        return False
    pp = PurePosixPath(rel)
    return (not pp.is_absolute() and not any(p in (".", "..") for p in rel.split("/"))
            and all(pp.parts) and pp.as_posix() == rel)

def declared_artifact_fields(item, kind, failures):
    if not isinstance(item, dict):
        failures.append(f"{kind} must be an artifact object")
        return None
    rel = item.get("relative_binary_path") or item.get("entrypoint") or item.get("relative_path")
    if not safe_relative(rel):
        failures.append(f"unsafe/missing {kind} artifact path")
        return None
    size, digest = item.get("size_bytes"), item.get("sha256")
    if not isinstance(size, int) or isinstance(size, bool) or size <= 0:
        failures.append(f"invalid {kind} artifact size: {rel}")
        return None
    if not isinstance(digest, str) or len(digest) != 64 or any(c not in "0123456789abcdefABCDEF" for c in digest):
        failures.append(f"invalid {kind} SHA256: {rel}")
        return None
    return rel, digest.lower(), size

def check(pack: Path, pin: str) -> list[str]:
    failures = []
    if pack.is_symlink() or not pack.is_dir():
        return ["pack root must be an existing non-symlink directory"]
    manifest_file = pack / "runtime-manifest.json"
    try:
        if manifest_file.is_symlink() or not stat.S_ISREG(manifest_file.lstat().st_mode):
            return ["manifest must be a regular non-symlink file"]
        raw = manifest_file.read_bytes()
        manifest = json.loads(raw)
        if not isinstance(manifest, dict):
            return ["manifest must be a JSON object"]
    except (OSError, ValueError) as exc:
        return [f"manifest unavailable/invalid: {exc}"]
    if manifest.get("manifest_type") != "release":
        failures.append("manifest_type must be release, not template")
    if not isinstance(pin, str) or len(pin) != 64 or any(c not in "0123456789abcdefABCDEF" for c in pin):
        failures.append("trusted external 64-character SHA256 pin required")
    elif hashlib.sha256(raw).hexdigest() != pin.lower():
        failures.append("manifest does not match external trusted pin")
    if (pack / "models").exists() or (pack / "models").is_symlink():
        failures.append("model distribution is not approved; models directory present")
    python = manifest.get("python")
    worker = manifest.get("worker")
    if not isinstance(worker, dict):
        worker = {}
        failures.append("worker artifact declaration required")
    if not isinstance(python, dict):
        python = {}
        failures.append("python artifact declaration required")
    artifacts = [("python", python), ("worker", worker)]
    # Isolated mode -I does not add the script directory to sys.path.
    # backend.py is a distinct executable module and MUST be hash-pinned.
    worker_rel = worker.get("entrypoint")
    backend_rel = worker.get("backend_relative_path")
    if (not safe_relative(worker_rel) or
        not safe_relative(backend_rel) or
        PurePosixPath(backend_rel).name != "backend.py" or
        PurePosixPath(backend_rel).parent != PurePosixPath(worker_rel).parent):
        failures.append("worker backend.py must be independently pinned beside worker.py")
    artifacts.append(("worker.backend", {
        "relative_path": backend_rel,
        "sha256": worker.get("backend_sha256"),
        "size_bytes": worker.get("backend_size_bytes"),
    }))
    binaries = manifest.get("binaries")
    if not isinstance(binaries, list):
        binaries = []
    by_name = {b.get("name") for b in binaries if isinstance(b, dict)}
    for needed in ("ffmpeg", "ffprobe"):
        if needed not in by_name:
            failures.append(f"required bundled binary missing: {needed}")
    artifacts.extend((f"binary.{i}", item) for i, item in enumerate(binaries))
    dependencies = manifest.get("dependencies")
    if not isinstance(dependencies, list) or not dependencies:
        failures.append("release manifest must declare pinned wheel dependencies")
        dependencies = []
    artifacts.extend((f"wheel.{i}", item) for i, item in enumerate(dependencies))
    seen = set()
    for kind, item in artifacts:
        fields = declared_artifact_fields(item, kind, failures)
        if fields is None:
            continue
        rel, expected_hash, expected_size = fields
        if rel in seen:
            failures.append(f"duplicate artifact path: {rel}")
            continue
        seen.add(rel)
        # Never follow a symlink in a declared path, including its parents.
        actual = pack
        for part in PurePosixPath(rel).parts:
            actual = actual / part
            if actual.is_symlink():
                failures.append(f"symlink component forbidden: {rel}")
                break
        else:
            try:
                info = actual.lstat()
                if not stat.S_ISREG(info.st_mode):
                    failures.append(f"artifact not a regular file: {rel}")
                elif info.st_size != expected_size or digest_file(actual) != expected_hash:
                    failures.append(f"artifact integrity mismatch: {rel}")
            except OSError:
                failures.append(f"missing artifact: {rel}")
    return failures

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pack", type=Path)
    parser.add_argument("--trusted-manifest-sha256", default="")
    args = parser.parse_args()
    failures = check(args.pack, args.trusted_manifest_sha256)
    if failures:
        for failure in failures:
            print(f"NO-GO: {failure}")
        raise SystemExit(1)
    print("DECLARED ARTIFACT PRECHECK PASS (NOT FULL TREE / SIGNATURE / LICENSE / OFFLINE CERTIFICATION)")
