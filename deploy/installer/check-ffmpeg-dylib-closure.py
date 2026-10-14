#!/usr/bin/env python3
"""Read-only macOS FFmpeg/FFprobe dylib closure audit (lab only)."""
import argparse
import json
import subprocess
from pathlib import Path

def audit(root: Path):
    binaries=[root/"ffmpeg", root/"ffprobe"]
    problems=[]
    dependencies={}
    pending=list(binaries)
    visited=set()
    while pending:
        p=pending.pop()
        key=str(p)
        if key in visited:
            continue
        visited.add(key)
        if not p.is_file():
            problems.append("missing: "+key)
            continue
        proc=subprocess.run(["otool","-L",key],capture_output=True,text=True,check=False)
        if proc.returncode:
            problems.append("otool failed: "+key)
            continue
        paths=[line.strip().split(" (compatibility version",1)[0] for line in proc.stdout.splitlines()[1:]]
        dependencies[key]=paths
        for lib in paths:
            if lib.startswith("/System/Library/") or lib.startswith("/usr/lib/"):
                continue
            if lib.startswith("@"):
                problems.append("unresolved loader-relative: "+lib)
                continue
            dep=Path(lib)
            if not dep.is_relative_to(root):
                problems.append("external dependency: "+lib)
                continue
            pending.append(dep)
    return {"pass":not problems,"problems":sorted(set(problems)),"files_examined":len(dependencies),"dependencies":dependencies}

if __name__=="__main__":
    p=argparse.ArgumentParser()
    p.add_argument("root",type=Path)
    args=p.parse_args()
    report=audit(args.root.resolve())
    print(json.dumps({"pass":report["pass"],"files_examined":report["files_examined"],"problems":report["problems"]},indent=2))
    raise SystemExit(0 if report["pass"] else 1)
