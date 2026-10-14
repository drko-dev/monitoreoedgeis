#!/bin/bash
# H6D-R10 opt-in lab ONLY: R9 candidate overlay + H7B-denied-egress RTSP/YOLO.
# The source pack is NEVER modified, installed, shipped or marked READY.
set -euo pipefail
umask 077
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 || $# -ne 5 ]]; then
  echo "Usage on macOS arm64: $0 <r4-lab-pack> <r9-candidates> <external-models> <fixture.h264> <h7b-runner.py>" >&2
  exit 2
fi
repo="$(cd "$(dirname "$0")/../.." && pwd -P)"
pack="$1"
cand="$2"
models="$3"
fixture="$4"
h7b="$5"
goexe="/opt/homebrew/bin/go"
for path in "$pack" "$cand" "$models"; do
  if [[ ! -d "$path" || -L "$path" || "$path" != /* ]]; then
    echo "NO-GO: required directory absent/relative/symlinked" >&2; exit 2
  fi
done
for file in "$fixture" "$h7b" "$cand/ffmpeg-1" "$cand/ffprobe-1"; do
  if [[ ! -f "$file" || -L "$file" || "$file" != /* ]]; then
    echo "NO-GO: required test file absent/relative/symlinked" >&2; exit 2
  fi
done
if [[ ! -x "$goexe" ]]; then echo "NO-GO: explicit Go toolchain missing" >&2;exit 2;fi
temp="$(/usr/bin/mktemp -d /tmp/geocam-h6d-r10-lab.XXXXXXXX)"
cleanup() {
  if [[ -n "$temp" && "$temp" == /tmp/geocam-h6d-r10-lab.* && -d "$temp" ]]; then
    /bin/rm -rf -- "$temp"
  fi
}
trap cleanup EXIT
# APFS CoW clone: never make a mutable hardlink to the original pack files.
# The two replacements are new files and installed atomically into the clone.
echo "R10_CLONE_COW: creating temporary non-release overlay" >&2
/bin/cp -cR "$pack" "$temp/pack"
/usr/bin/python3 - "$temp/pack" "$cand" <<'PY'
from pathlib import Path
import hashlib, json, os, subprocess, sys
pack, cand = map(Path, sys.argv[1:])
mfile=pack/"lab-manifest.json"
if not mfile.is_file() or mfile.is_symlink():
    raise SystemExit("NO-GO: lab-only manifest missing")
m=json.loads(mfile.read_text())
required_false=("release_eligible","runtime_ready","inference_ready",
                "model_redistribution_approved","trusted_pin_present")
if m.get("pack_type") != "laboratory-only" or any(m.get(key) is not False for key in required_false):
    raise SystemExit("NO-GO: lab-only flags not explicitly false")
original={
    "ffmpeg":"9e97cd0015b2ed2e2b3bba0401f8554b7cceb93b27558f2ee309ffd824d29b07",
    "ffprobe":"ebf3c455845fee554bfb9ffa0e4bab6268c589c199852b0c3c641b4598282567",
}
candidates={
    "ffmpeg":"fb05859cd1517959a8b5edc609b02c4028005619763c27ad59602d0b46a32082",
    "ffprobe":"130d9a09aa290508c65ba6fe067d1d500745e8036a96e1e488c39c4ac1dd0979",
}
def digest(p):
    with p.open("rb") as inp:
        h=hashlib.sha256()
        for chunk in iter(lambda:inp.read(1024*1024), b""):
            h.update(chunk)
        return h.hexdigest()
for name in ("ffmpeg","ffprobe"):
    current=pack/"ffmpeg"/name
    candidate=cand/(name+"-1")
    if current.is_symlink() or digest(current)!=original[name]:
        raise SystemExit("NO-GO: source lab binary changed: "+name)
    if candidate.is_symlink() or digest(candidate)!=candidates[name]:
        raise SystemExit("NO-GO: unrecognized R9 candidate: "+name)
    build=subprocess.run(["/usr/bin/xcrun","vtool","-show-build",str(candidate)],
                         capture_output=True,text=True,timeout=10,check=True).stdout
    if "minos 14.0" not in build:
        raise SystemExit("NO-GO: wrong candidate deployment minimum: "+name)
    item=[f for f in m["files"] if f["path"]=="ffmpeg/"+name]
    if len(item)!=1 or item[0]["sha256"]!=original[name]:
        raise SystemExit("NO-GO: original inventory FFmpeg entry missing/changed")
    overlay=current.parent/("."+name+".r10")
    overlay.write_bytes(candidate.read_bytes())
    overlay.chmod(0o755)
    os.replace(overlay,current)
    item[0]["sha256"]=digest(current)
    item[0]["size_bytes"]=current.stat().st_size
    print("R10_CANDIDATE",name,item[0]["sha256"])
m["pack_id"]=m["pack_id"]+"-r10-ephemeral"
m["provenance"]["r10_overlay"]="Temporary untrusted lab test only"
staged=mfile.with_name(".lab-manifest.r10.tmp")
staged.write_text(json.dumps(m,indent=2,sort_keys=True)+"\n")
os.replace(staged,mfile)
PY
echo "R10_H7B_SANDBOX: running end-to-end RTSP -> R9 ffmpeg -> worker -> YOLO" >&2
cd "$repo"
env PATH=/usr/bin:/bin H6D_LAB_PACK="$temp/pack" H6D_LAB_MODELS="$models" \
  H6D_LAB_H264="$fixture" H6D_LAB_DEVICE=mps \
  /usr/bin/python3 "$h7b" --require-marker RTSP_GO_FFMPEG_YOLO_PASS -- \
  "$goexe" test -count=1 -run '^TestLabRTSPToRealYOLO$' -v ./internal/vision
# No copied model weights, no release manifest or distribution artifacts.
echo "R10_LAB_ONLY_PASS_NO_RELEASE; temporary pack will be removed" >&2
