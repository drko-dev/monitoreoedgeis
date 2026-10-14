#!/usr/bin/env python3
"""H6D local-only assembly gap report. Read-only, fail-closed."""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

def sha(path):
    h=hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda:handle.read(1024*1024),b""):
            h.update(block)
    return h.hexdigest()

def check(repo, lab, ffmpeg_dir):
    manifest=json.loads((repo/"internal/installer/assets/runtime-manifest.json").read_text())
    lock=json.loads((repo/"internal/installer/assets/wheelhouse-lock.json").read_text())
    issues=[]
    if manifest.get("manifest_type")!="release":
        issues.append("manifest is a template, not a pinned release")
    py=lab/"python/bin/python3"
    if not py.is_file():
        issues.append("standalone Python absent")
    else:
        result=subprocess.run([str(py),"--version"],capture_output=True,text=True)
        if result.returncode or manifest["python"]["version"] not in result.stdout+" "+result.stderr:
            issues.append("standalone Python version mismatch")
    for wheel in lock.get("dependencies",[]):
        file=lab/"wheelhouse"/wheel["filename"]
        if not file.is_file() or file.stat().st_size!=wheel["size_bytes"] or sha(file)!=wheel["sha256"]:
            issues.append("wheel missing or digest invalid: "+wheel["filename"])
    for name in ("ffmpeg","ffprobe"):
        binary=ffmpeg_dir/name
        if not binary.is_file():
            issues.append(name+" missing from candidate bundle")
            continue
        if binary.is_symlink():
            issues.append(name+" must not be a symlink")
    if (lab/"models").exists():
        issues.append("model folder present in staging; redistribution not approved")
    return {"status":"NOT_READY" if issues else "COMPONENTS_PRESENT_UNCERTIFIED",
            "runtime_ready":False,"inference_ready":False,
            "model_redistribution_approved":False,
            "verified_wheels":len(lock.get("dependencies",[]))-sum("wheel missing or digest invalid" in x for x in issues),
            "total_wheels":len(lock.get("dependencies",[])),
            "issues":issues,
            "note":"Not a trusted release, legal audit, dylib closure, or runtime certification."}
def main():
    p=argparse.ArgumentParser()
    p.add_argument("--repo",type=Path,default=Path(__file__).resolve().parents[2])
    p.add_argument("--lab",type=Path,required=True)
    p.add_argument("--ffmpeg-dir",type=Path,required=True)
    a=p.parse_args()
    report=check(a.repo.resolve(),a.lab.resolve(),a.ffmpeg_dir.resolve())
    print(json.dumps(report,indent=2))
    return 0 if report["status"]=="COMPONENTS_PRESENT_UNCERTIFIED" else 1
if __name__=="__main__":
    raise SystemExit(main())
