#!/usr/bin/env python3
"""Read-only observations from LOCAL pinned wheel archives. Never a legal approval.

Validates the wheel itself against repository SHA256/size and extracts bounded
license/METADATA observations from the wheel, with no PyPI or host imports.
These observations CANNOT establish signed provenance, license compliance,
redistribution permission, or Full Edge release readiness.
"""
import argparse
import hashlib
import json
import re
import stat
import zipfile
from email.parser import Parser
from pathlib import Path, PurePosixPath

MAX_METADATA = 1024 * 1024
MAX_LICENSE = 2 * 1024 * 1024

def canonical_name(name):
    return re.sub(r"[-_.]+", "-", name).lower()

def sha256_file(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()

def safe_members(archive):
    seen = set()
    for info in archive.infolist():
        name = info.filename
        part = PurePosixPath(name)
        if (not name or name.startswith("/") or "\\" in name or
            ".." in part.parts or "\x00" in name or name in seen):
            raise ValueError("unsafe or duplicate ZIP member")
        seen.add(name)
    return archive.infolist()

def archive_evidence(path, dep):
    with zipfile.ZipFile(path) as archive:
        members = safe_members(archive)
        metadata = [m for m in members if m.filename.endswith(".dist-info/METADATA")]
        if len(metadata) != 1:
            raise ValueError("wheel must contain exactly one .dist-info/METADATA")
        meta = metadata[0]
        if meta.file_size > MAX_METADATA:
            raise ValueError("distribution METADATA too large")
        message = Parser().parsestr(archive.read(meta).decode("utf-8", "replace"))
        actual_name, actual_version = message.get("Name", ""), message.get("Version", "")
        if canonical_name(actual_name) != canonical_name(dep["name"]) or actual_version != dep["version"]:
            raise ValueError("distribution name/version mismatch against pinned lock")
        distroot = meta.filename.split(".dist-info/", 1)[0] + ".dist-info/"
        license_files = []
        for member in members:
            base = PurePosixPath(member.filename).name.lower()
            is_license = (
                member.filename.startswith(distroot + "licenses/") or
                member.filename.startswith(distroot) and
                (base.startswith(("license", "licence", "copying", "notice"))))
            if is_license and not member.is_dir():
                if member.file_size > MAX_LICENSE:
                    raise ValueError("oversized license/notice file")
                text = archive.read(member)
                license_files.append({
                    "path": member.filename,
                    "size": len(text),
                    "sha256": hashlib.sha256(text).hexdigest(),
                })
        evidence = {
            "metadata_name": actual_name,
            "metadata_version": actual_version,
            "license_expression_in_metadata": message.get("License-Expression", "").strip(),
            "legacy_license_field": message.get("License", "").strip()[:300],
            "license_file_references": message.get_all("License-File", []),
            "license_files_found": license_files,
            "license_text_present": bool(license_files),
            "metadata_read_from_actual_wheel": True,
            "signed_origin_verified": False,
            "human_legal_review_complete": False,
            "redistribution_approved": False,
        }
        return evidence

def audit(repo, wheelhouse):
    lock = json.loads((repo / "internal/installer/assets/wheelhouse-lock.json").read_text())
    expected = {d["filename"] for d in lock["dependencies"]}
    try:
        extra = sorted(p.name for p in wheelhouse.iterdir()
                       if p.suffix == ".whl" and p.name not in expected)
    except OSError:
        extra = ["wheelhouse inaccessible"]
    records = []
    for dep in lock["dependencies"]:
        filename = dep["filename"]
        record = {
            "name": dep["name"], "version": dep["version"], "filename": filename,
            "lock_sha256": dep["sha256"], "lock_license_claim": dep.get("license", ""),
            "origin_declared_in_lock": dep.get("origin", ""),
            "content_hash_verified_against_lock": False,
            "signed_origin_verified": False,
            "human_legal_review_complete": False,
            "redistribution_approved": False,
        }
        path = wheelhouse / filename
        try:
            if path.parent.is_symlink() or path.is_symlink() or not stat.S_ISREG(path.lstat().st_mode):
                raise ValueError("wheel must be a regular, non-symlink file")
            if path.stat().st_size != dep["size_bytes"]:
                raise ValueError("wheel size differs from pinned lock")
            if sha256_file(path).lower() != dep["sha256"].lower():
                raise ValueError("wheel digest differs from pinned lock")
            record["content_hash_verified_against_lock"] = True
            record.update(archive_evidence(path, dep))
        except (OSError, ValueError, zipfile.BadZipFile, RuntimeError, KeyError) as exc:
            record["error"] = f"{type(exc).__name__}: {exc}"
        records.append(record)
    return {
        "report_scope": "local-archive-metadata-observation-only",
        "wheelhouse_location": str(wheelhouse),
        "expected_wheels": len(lock["dependencies"]),
        "hash_verified_wheels": sum(x["content_hash_verified_against_lock"] for x in records),
        "metadata_read_wheels": sum(x.get("metadata_read_from_actual_wheel", False) for x in records),
        "with_license_text": sum(x.get("license_text_present", False) for x in records),
        "without_license_text": sum(x.get("metadata_read_from_actual_wheel", False) and not x.get("license_text_present", False) for x in records),
        "wheel_errors": sum(bool(x.get("error")) for x in records),
        "unexpected_wheel_files": extra,
        "technical_inventory_complete": (
            not extra and len(records) == len(expected) and
            all(x.get("metadata_read_from_actual_wheel", False) for x in records)
        ),
        "legal_go": False,
        "release_eligible": False,
        "warning": "Wheel contents can match a lock and have license notices while still lacking authenticated origin or redistribution approval.",
        "wheels": records,
    }

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--wheelhouse", required=True, type=Path)
    parser.add_argument("--summary", action="store_true")
    args = parser.parse_args()
    evidence = audit(args.repo, args.wheelhouse)
    if args.summary:
        evidence = {key: value for key, value in evidence.items() if key != "wheels"}
    print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0 if evidence["technical_inventory_complete"] else 1

if __name__ == "__main__":
    raise SystemExit(main())
