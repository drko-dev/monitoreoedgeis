#!/usr/bin/env python3
"""H6D-R9 advisory macOS minimums across locally assembled non-release pack.

Reads Mach-O LC_BUILD_VERSION from each regular file with a Mach-O magic.
Optionally substitutes the *inspection path* for FFmpeg/FFprobe using clean
same-host R9 candidates. Does NOT edit pack, activate runtime, or certify
older macOS support. Mach-O minos is a necessary not sufficient requirement.
"""
import argparse
from collections import Counter
import json
from pathlib import Path
import re
import stat
import subprocess

MACHO_MAGICS = {
    bytes.fromhex("cffaedfe"), bytes.fromhex("feedfacf"),
    bytes.fromhex("cafebabe"), bytes.fromhex("bebafeca"),
}

def minimum_version(contents):
    found = re.findall(r"(?m)^\s*minos\s+(\d+)\.(\d+)\s*$", contents)
    if len(found) != 1:
        return None
    return tuple(map(int, found[0]))

def macos_string(pair):
    return ".".join(map(str, pair))

def read_minos(path):
    try:
        result = subprocess.run(
            ["/usr/bin/xcrun", "vtool", "-show-build", str(path)],
            capture_output=True, text=True, timeout=10, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return minimum_version(result.stdout) if result.returncode == 0 else None

def is_macho_regular_file(path):
    try:
        if path.is_symlink() or not stat.S_ISREG(path.lstat().st_mode):
            return False
        with path.open("rb") as file:
            return file.read(4) in MACHO_MAGICS
    except OSError:
        return False

def scan_pack(pack, baseline=(14,0), candidate=None, get_minos=read_minos):
    if pack.is_symlink() or not pack.is_dir():
        raise ValueError("pack must be an existing non-symlink directory")
    if candidate is not None and (candidate.is_symlink() or not candidate.is_dir()):
        raise ValueError("candidate must be an existing non-symlink lab directory")
    minos_counts = Counter()
    failures = []
    examples = []
    checked = 0
    substituted = []
    for entry in pack.rglob("*"):
        if not is_macho_regular_file(entry):
            continue
        rel = entry.relative_to(pack).as_posix()
        inspected = entry
        if candidate is not None and rel in ("ffmpeg/ffmpeg","ffmpeg/ffprobe"):
            name = rel.split("/")[-1]
            inspected = candidate / (name + "-1")
            substituted.append(rel)
            if not is_macho_regular_file(inspected):
                failures.append("missing/invalid candidate: " + rel)
                continue
        minos = get_minos(inspected)
        checked += 1
        if minos is None:
            failures.append("unreadable LC_BUILD_VERSION: " + rel)
            continue
        ver = macos_string(minos)
        minos_counts[ver] += 1
        if minos > baseline:
            if len(examples) < 25:
                examples.append({"path":rel,"macos_minos":ver})
            failures.append("deployment target above baseline: " + rel)
    if candidate is not None and sorted(substituted) != ["ffmpeg/ffmpeg","ffmpeg/ffprobe"]:
        failures.append("both FFmpeg/FFprobe candidates must be considered")
    if checked == 0:
        failures.append("no Mach-O files inspected")
    return {
        "audited_macho_files": checked,
        "declared_minos_counts": dict(sorted(minos_counts.items(),
                                             key=lambda pair: tuple(map(int,pair[0].split("."))))),
        "comparison_baseline_macos": macos_string(baseline),
        "candidate_substituted_for_inspection_only": candidate is not None,
        "substituted_entries": sorted(substituted),
        "above_baseline_examples": examples,
        "violations_count": len(failures),
        "violations_first": failures[:25],
        "macho_minimums_within_baseline": checked > 0 and not failures,
        "clean_machine_tested": False,
        "full_dependency_api_compatibility_tested": False,
        "release_eligible": False,
        "warning": "Mach-O minos is necessary, never sufficient for deployment compatibility."
    }

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pack",required=True,type=Path)
    parser.add_argument("--candidate-dir",type=Path)
    parser.add_argument("--baseline",default="14.0")
    args=parser.parse_args()
    match=re.fullmatch(r"(\d+)\.(\d+)",args.baseline)
    if not match:
        parser.error("invalid macOS major.minor baseline")
    data=scan_pack(args.pack,tuple(map(int,match.groups())),args.candidate_dir)
    print(json.dumps(data,indent=2,sort_keys=True))
    return 0 if data["macho_minimums_within_baseline"] else 1

if __name__=="__main__":
    raise SystemExit(main())
