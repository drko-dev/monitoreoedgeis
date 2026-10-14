#!/usr/bin/env python3
"""Non-authoritative THIRD-PARTY-NOTICES generator from locked declarations.

It generates a review worksheet. It does not grant rights, copy upstream
license texts, or certify that a binary can be redistributed.
"""
import argparse
import json
from pathlib import Path

def notice_text(repo: Path) -> str:
    lock = json.loads((repo / "internal/installer/assets/wheelhouse-lock.json").read_text())
    lines = [
        "# Third-party dependency notices — DRAFT / NOT FOR RELEASE",
        "",
        "These license labels and URLs originate solely from the checked-in lock file.",
        "They are **not verified against actual wheel archives**. Actual LICENSE,",
        "METADATA, copyrights and transitive native libraries must be audited.",
        "No redistribution is authorized by this file.",
        "",
        "| Name | Version | License declared | Origin declared | Verification |",
        "|---|---|---|---|---|",
    ]
    for dep in sorted(lock["dependencies"], key=lambda x: x["name"].lower()):
        esc=lambda v: str(v).replace("|", "/").replace("\n", " ")
        lines.append("| " + " | ".join([esc(dep["name"]),esc(dep["version"]),
            esc(dep.get("license","UNKNOWN")),esc(dep.get("origin","")),
            "UNVERIFIED"]) + " |")
    lines += [
        "",
        "## Additional runtime components requiring source and legal verification",
        "",
        "- CPython and python-build-standalone: original archive licenses, notices and bundled shared libraries.",
        "- FFmpeg and FFprobe: actual build configure flags, LGPL/GPL source offer, patches, linked libraries; libx264 is requested by current clip encoding.",
        "- Vision Worker: its code, imports, model framework and packaged license texts.",
        "- Model weights: not licensed for redistribution in this release.",
        "",
        "## Required gates before release",
        "",
        "1. Independently verify each artifact hash, source and legal notices.",
        "2. Choose and document a valid Ultralytics AGPL-compliance or Enterprise contract path.",
        "3. Choose FFmpeg GPL-compliant distribution or a validated LGPL-compatible codec/build path.",
        "4. Have the product owner and legal reviewer approve the exact release artifacts in an authenticated, separately managed process.",
        "5. Create a real sealed Runtime Pack and release pin, then pass functional offline tests.",
        "",
    ]
    return "\n".join(lines)

if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--repo",type=Path,default=Path(__file__).resolve().parents[2])
    parser.add_argument("--out",type=Path)
    args=parser.parse_args()
    output=notice_text(args.repo)
    if args.out:
        args.out.write_text(output)
    else:
        print(output)
