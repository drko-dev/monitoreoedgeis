# H6D Local Laboratory Gate

Status: **LOCAL_LAB_ONLY / NOT READY**, not a commercial release and not an assertion of license clearance.

Run `python3 deploy/installer/check-local-full-edge-lab.py` for a read-only diagnostic. It reports the checked-in manifest type, locked wheel count, host tool locations, disk headroom and always distinguishes installed host tools from **verified pack-contained executables**. The output explicitly contains `runtime_ready=false`, `inference_ready=false`, `pack_ready=false` and `may_download=false`.

Observed 2026-10-14 on the authorized Mac M4: host Python 3.14.6, FFmpeg/FFprobe in /opt/homebrew/bin; manifest declares standalone Python 3.11.9; free space ~4.6 GiB. A conservative 8 GiB free-space advisory threshold is used as a **local planning threshold, not a product requirement**. No heavy downloads were performed.

Local development without commercial distribution may use legally obtained tools under applicable licenses, but source/weights rights and AGPL network obligations still apply. Homebrew executables must never silently satisfy hermetic pack checks. Do not change `MODEL_REDISTRIBUTION_APPROVED=false` or default Wails `UnavailableFullEdgePreparation`.

Next gates: provision enough disk, establish legal local model use, obtain trusted binaries from authorized sources, build a separate local-lab pack manifest and pinned artifacts, test FFmpeg/Worker offline with laboratory inputs, then proceed to H7B/H8. This diagnostic is not a release assembler or functional validator.
