#!/usr/bin/env python3
"""H6D-R8 read-only FFmpeg build-chain evidence; NOT reproducible-build proof.

Compare all regular files in the previously PGP-authenticated FFmpeg release tar
against the locally configured source tree; inspect recorded build/configure
logs, and compare SHA256 of built, portable, and lab-pack executables.
Inspect actual Mach-O dependencies and deployment minimums. No builds, writes,
downloads, network requests, signatures, or model access.
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tarfile

SOURCE_SHA256 = "7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3"
BIN_SHA256 = {
    "ffmpeg": "9e97cd0015b2ed2e2b3bba0401f8554b7cceb93b27558f2ee309ffd824d29b07",
    "ffprobe": "ebf3c455845fee554bfb9ffa0e4bab6268c589c199852b0c3c641b4598282567",
}
TAR_ROOT = "ffmpeg-8.1.3"
REQUIRED_FLAGS = {
    "--disable-everything", "--disable-autodetect", "--disable-network",
    "--enable-static", "--disable-shared", "--enable-decoder=h264",
    "--enable-parser=h264", "--enable-ffmpeg", "--enable-ffprobe",
}
FORBIDDEN_FLAGS = {
    "--enable-gpl", "--enable-nonfree", "--enable-libx264",
    "--enable-network", "--enable-shared",
}
SYSTEM_PREFIXES = ("/usr/lib/", "/System/Library/")

def sha256_stream(f):
    h = hashlib.sha256()
    for block in iter(lambda: f.read(1024 * 1024), b""):
        h.update(block)
    return h.hexdigest()

def sha256_file(path):
    with path.open("rb") as f:
        return sha256_stream(f)

def regular_file(path):
    try:
        return not path.is_symlink() and stat.S_ISREG(path.lstat().st_mode)
    except OSError:
        return False

def source_match(archive, source_tree, expected_source_sha256=SOURCE_SHA256):
    result = {"expected_source_sha256": expected_source_sha256,
              "checked": 0, "matched": 0, "failures": []}
    if not regular_file(archive):
        result["failures"].append("source archive not a regular file")
        return result
    digest = sha256_file(archive)
    result["source_sha256"] = digest
    if digest != expected_source_sha256.lower():
        result["failures"].append("source archive SHA256 mismatch")
        return result
    if source_tree.is_symlink() or not source_tree.is_dir():
        result["failures"].append("source tree not an existing non-symlink directory")
        return result
    with tarfile.open(archive, "r:xz") as tar:
        for member in tar:
            if member.name == TAR_ROOT and member.isdir():
                continue
            if not member.name.startswith(TAR_ROOT + "/"):
                result["failures"].append("unexpected tarball root")
                continue
            rel = member.name[len(TAR_ROOT) + 1:]
            parts = PurePosixPath(rel).parts
            if (not parts or any(x in (".", "..") for x in parts)
                    or "\\" in rel or rel.startswith("/")):
                result["failures"].append("unsafe archive member path")
                continue
            if member.isdir():
                continue
            if not member.isfile():
                result["failures"].append("unsupported non-regular source archive member")
                continue
            result["checked"] += 1
            local = source_tree
            unsafe = False
            for part in parts:
                local = local / part
                if local.is_symlink():
                    unsafe = True
                    break
            if unsafe or not regular_file(local):
                result["failures"].append("missing or unsafe source tree member: " + rel)
                continue
            if local.stat().st_size != member.size:
                result["failures"].append("modified source file size: " + rel)
                continue
            reader = tar.extractfile(member)
            if reader is None or sha256_stream(reader) != sha256_file(local):
                result["failures"].append("source file digest mismatch: " + rel)
                continue
            result["matched"] += 1
    if result["checked"] == 0:
        result["failures"].append("no regular source files verified")
    result["all_archived_source_files_match"] = (
        result["checked"] > 0 and result["matched"] == result["checked"]
        and not result["failures"]
    )
    return result

def run_read_only(command):
    try:
        p = subprocess.run(command, text=True, capture_output=True,
                           timeout=15, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ValueError(f"inspection tool failed: {type(exc).__name__}") from exc
    if p.returncode != 0:
        raise ValueError("inspection tool exited nonzero: " + command[0])
    return p.stdout + p.stderr

def binary_report(name, source_tree, portable_dir, pack_dir,
                  expected_bin_sha256=BIN_SHA256,
                  max_minos=(11, 0), runner=run_read_only):
    result = {"name": name, "hashes": {}, "failures": [], "max_tested_minos": ".".join(map(str,max_minos))}
    for label, loc in [
        ("built", source_tree / name), ("portable", portable_dir / name),
        ("pack", pack_dir / "ffmpeg" / name),
    ]:
        if not regular_file(loc):
            result["failures"].append(f"{label} binary missing/non-regular")
            continue
        result["hashes"][label] = sha256_file(loc)
    hashes = list(result["hashes"].values())
    expected = expected_bin_sha256[name]
    if len(hashes) != 3 or any(h != expected for h in hashes):
        result["failures"].append("built/portable/pack hashes differ from pinned binary")
    executable = portable_dir / name
    if not regular_file(executable):
        return result
    try:
        archs = runner(["/usr/bin/lipo", "-archs", str(executable)]).strip().split()
        result["architectures"] = archs
        if archs != ["arm64"]:
            result["failures"].append("binary architecture must be arm64 only")
        links = runner(["/usr/bin/otool", "-L", str(executable)]).splitlines()
        dylibs = [line.strip().split(" (compatibility version", 1)[0]
                  for line in links[1:] if line.strip()]
        result["dylibs"] = dylibs
        if not dylibs or any(not d.startswith(SYSTEM_PREFIXES) for d in dylibs):
            result["failures"].append("binary has non-system or unknown dylib dependencies")
        build = runner(["xcrun", "vtool", "-show-build", str(executable)])
        version = re.search(r"(?m)^\s*minos\s+(\d+)\.(\d+)", build)
        if not version:
            result["failures"].append("Mach-O minimum macOS version unavailable")
        else:
            minos = tuple(map(int, version.groups()))
            result["macos_minos"] = ".".join(map(str,minos))
            if minos > max_minos:
                result["failures"].append(
                    "Mach-O deployment target exceeds baseline " +
                    ".".join(map(str,max_minos)))
        conf = runner([str(executable), "-hide_banner", "-buildconf"])
        flags = set(re.findall(r"(?m)^\s+(--[^\s]+)", conf))
        result["configure_flags"] = sorted(flags)
        missing = sorted(REQUIRED_FLAGS - flags)
        forbidden = sorted(FORBIDDEN_FLAGS & flags)
        if missing or forbidden:
            result["failures"].append("required/forbidden configure flags: " +
                                      repr((missing,forbidden)))
    except ValueError as exc:
        result["failures"].append(str(exc))
    return result

def audit(archive, source_tree, build_log, configure_log, portable_dir,
          pack_dir, max_minos=(11,0), expected_source_sha256=SOURCE_SHA256,
          expected_bin_sha256=BIN_SHA256, runner=run_read_only):
    sources = source_match(archive,source_tree,expected_source_sha256)
    build = {"events_present": {}, "failures": []}
    for label, path in (("build", build_log),("configure", configure_log)):
        if not regular_file(path):
            build["failures"].append(label+" log missing/non-regular")
    if not build["failures"]:
        build_text=build_log.read_text(errors="replace")
        config_text=configure_log.read_text(errors="replace")
        for event in ("LD\tffmpeg_g", "LD\tffprobe_g",
                      "STRIP\tffmpeg", "STRIP\tffprobe"):
            build["events_present"][event] = event in build_text
            if event not in build_text:
                build["failures"].append("missing build log marker")
        build["configure_declares_lgpl"] = "License: LGPL version 2.1 or later" in config_text
        build["configure_declares_no_network"] = "network support           no" in config_text
        if not build["configure_declares_lgpl"] or not build["configure_declares_no_network"]:
            build["failures"].append("configure log does not match expected LGPL/network policy")
        build["log_sha256"] = sha256_file(build_log)
        build["configure_log_sha256"] = sha256_file(configure_log)
    binaries = [binary_report(name,source_tree,portable_dir,pack_dir,
                              expected_bin_sha256,max_minos,runner)
                for name in ("ffmpeg","ffprobe")]
    return {
        "evidence_scope": "local-source-to-build-and-binary-consistency-not-reproducible-build-attestation",
        "signed_source_sha256_previously_validated_by_h6d_r7": expected_source_sha256,
        "source_tree": sources,
        "build_logs": build,
        "binaries": binaries,
        "technical_consistency_pass": (
            sources.get("all_archived_source_files_match",False)
            and not build["failures"]
            and all(b["hashes"].get("built")==b["hashes"].get("portable")==b["hashes"].get("pack")
                    == expected_bin_sha256[b["name"]] for b in binaries)
        ),
        "baseline_portability_pass": all(not b["failures"] for b in binaries),
        "reproducible_build_verified": False,
        "release_codesign_verified": False,
        "legal_redistribution_approved": False,
        "release_eligible": False,
    }

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-root",required=True,type=Path)
    parser.add_argument("--pack",required=True,type=Path)
    parser.add_argument("--max-macos-minos",default="11.0")
    parser.add_argument("--summary",action="store_true")
    args=parser.parse_args()
    version=re.fullmatch(r"(\d+)\.(\d+)",args.max_macos_minos)
    if not version:
        parser.error("invalid major.minor deployment baseline")
    root=args.lab_root
    output=audit(
        root/"ffmpeg-source/ffmpeg-8.1.3.tar.xz",
        root/"ffmpeg-source/ffmpeg-8.1.3",
        root/"ffmpeg-source/build.log",
        root/"ffmpeg-source/configure.log",
        root/"ffmpeg-portable",
        args.pack,
        max_minos=tuple(map(int,version.groups())),
    )
    if args.summary:
        output["binaries"]=[{
            k:v for k,v in binary.items() if k!="configure_flags"
        } for binary in output["binaries"]]
    print(json.dumps(output,indent=2,sort_keys=True))
    # A source matching/build consistent result MUST NOT imply compatibility.
    return 0 if output["technical_consistency_pass"] and output["baseline_portability_pass"] else 1

if __name__ == "__main__":
    raise SystemExit(main())
