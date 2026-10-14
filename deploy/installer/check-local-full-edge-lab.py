#!/usr/bin/env python3
"""Read-only local Full Edge laboratory prerequisites; never enables READY."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

def inspect(repo: Path, python_path=None, ffmpeg_path=None, ffprobe_path=None, disk_free=None):
    manifest=json.loads((repo/"internal/installer/assets/runtime-manifest.json").read_text())
    lock=json.loads((repo/"internal/installer/assets/wheelhouse-lock.json").read_text())
    python_path=python_path if python_path is not None else sys.executable
    ffmpeg_path=ffmpeg_path if ffmpeg_path is not None else shutil.which("ffmpeg")
    ffprobe_path=ffprobe_path if ffprobe_path is not None else shutil.which("ffprobe")
    if disk_free is None:
        disk_free=shutil.disk_usage(repo).free
    checks=[]
    checks.append({"id":"manifest","pass":manifest.get("manifest_type")=="release","detail":"template is not an installed pack" if manifest.get("manifest_type")!="release" else "release declaration exists; signature still unverified"})
    checks.append({"id":"wheelhouse","pass":False,"detail":f"{len(lock['dependencies'])} locked wheels declared, not physically verified in a sealed pack"})
    checks.append({"id":"python","pass":False,"detail":f"host={python_path or 'missing'}, expected standalone={manifest['python']['version']}; host Python is not pack verification"})
    for name,path in (("ffmpeg",ffmpeg_path),("ffprobe",ffprobe_path)):
        checks.append({"id":name,"pass":False,"detail":f"host={path or 'missing'}; must use pinned pack executable"})
    checks.append({"id":"disk","pass":disk_free>=8*1024**3,"detail":f"free_bytes={disk_free}; advisory minimum 8GiB before attempting heavyweight lab assembly"})
    checks.append({"id":"authorization","pass":False,"detail":"No licensed model files or full functional offline certification supplied"})
    return {"mode":"LOCAL_LAB_ONLY","runtime_ready":False,"inference_ready":False,"pack_ready":False,"may_download":False,"checks":checks}

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--repo",type=Path,default=Path(__file__).resolve().parents[2])
    args=p.parse_args()
    report=inspect(args.repo)
    print(json.dumps(report,indent=2))
    return 0 # diagnostic exits successfully; consumers must inspect explicit booleans
if __name__=="__main__":
    raise SystemExit(main())
