#!/usr/bin/env python3
"""Non-mutating inventory of declared H6D artifacts; never claims approval."""
import argparse
import json
from pathlib import Path

def inventory(root: Path):
    manifest = json.loads((root / "internal/installer/assets/runtime-manifest.json").read_text())
    entries = []
    for category, values in (
        ("python", [manifest.get("python", {})]),
        ("worker", [manifest.get("worker", {})]),
        ("binary", manifest.get("binaries", [])),
        ("wheel", manifest.get("dependencies", [])),
    ):
        for item in values:
            path = item.get("relative_binary_path") or item.get("entrypoint") or item.get("relative_path")
            entries.append({
                "category": category,
                "name": item.get("name", category),
                "version_declared": item.get("version", ""),
                "path_declared": path,
                "sha256_declared": item.get("sha256", ""),
                "provenance_verified": False,
                "redistribution_approved": False,
                "artifact_verified": False,
            })
    return {
        "manifest_type": manifest.get("manifest_type"),
        "platform": manifest.get("platform"),
        "architecture": manifest.get("architecture"),
        "release_eligible": False,
        "notes": [
            "Declaration is not evidence of origin, license, or presence.",
            "Each executable and wheel requires independent provenance and redistribution review.",
            "No models or model weights authorized for redistribution.",
            "Release-manifest pin must originate from separately authenticated publisher metadata.",
        ],
        "artifacts": entries,
    }

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    print(json.dumps(inventory(args.repo), indent=2, sort_keys=True))
