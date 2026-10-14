#!/usr/bin/env python3
"""H6D read-only licensing inventory; metadata is NOT legal verification."""
import argparse
import json
from collections import Counter
from pathlib import Path

def audit(root: Path):
    lock = json.loads((root / "internal/installer/assets/wheelhouse-lock.json").read_text())
    manifest = json.loads((root / "internal/installer/assets/runtime-manifest.json").read_text())
    entries = []
    for dep in lock["dependencies"]:
        license_declared = dep.get("license", "UNKNOWN")
        entries.append({
            "name": dep["name"], "version": dep["version"],
            "origin_declared": dep.get("origin", ""),
            "license_declared": license_declared,
            "license_verified_from_distribution": False,
            "redistribution_reviewed": False,
            "legal_go": False,
            "needs_special_review": "AGPL" in license_declared or dep["name"] in {"torch", "torchvision", "opencv-python"},
        })
    return {
        "release_eligible": False,
        "manifest_type": manifest.get("manifest_type"),
        "wheel_count": len(entries),
        "declared_license_counts": dict(Counter(x["license_declared"] for x in entries)),
        "additional_review": [
            {"component": "ffmpeg/ffprobe", "status": "BLOCKED", "reason": "No actual binary/configuration and evidence; clips request libx264. GPL/LGPL and distribution obligations must be reviewed.", "reference": "https://ffmpeg.org/legal.html"},
            {"component": "python-build-standalone", "status": "PENDING", "reason": "Pinned upstream build is declared; review archive-contained licensing metadata and bundled transitive libraries.", "reference": "https://github.com/astral-sh/python-build-standalone/blob/main/docs/running.rst"},
            {"component": "Ultralytics models/worker", "status": "BLOCKED", "reason": "AGPL-3.0/Enterprise application and model obligations unresolved. No redistribution permission assumed.", "reference": "https://www.ultralytics.com/license"},
        ],
        "wheels": entries,
    }

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    print(json.dumps(audit(args.repo), indent=2, sort_keys=True))
