# H8 — macOS 14 ARM64 certification evidence and blockers

**Status: STATIC_AND_WORKER_LAB_PASS / PHYSICAL_MACOS14_BLOCKED. NOT CERTIFIED.**

Branch: `test/full-edge-h8-macos14-compat`. Issue #153 owner-approved for PR publication. Full Edge is not authorized for installation or commercial distribution. `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`.

## Verified host and audit results

The authorized development Mac is Apple Silicon ARM64 running **macOS 27.0.1**; no working approved macOS 14 physical host or VM was found. **A macOS 27 run does not prove that code will execute on macOS 14.**

Latest local runner output for explicitly supplied r4 lab pack, R9 FFmpeg candidates and local models directory:

| ID | Status | Grounded scope |
| --- | --- | --- |
| Host native ARM64 | PASS | Mac ARM64 / non-Rosetta |
| macOS 14 physical/clean rig | **BLOCKED** | No actual target OS available |
| Native Python/wheel Mach-O headers | PASS | Original r4 contains 316 Mach-O files; **two original FFmpeg files minos 27.0 are excluded from this specific Python/wheels pass**, all remaining native files satisfy minos ≤14.0 |
| Standalone FFmpeg R9 candidates | PASS | Two candidate executables declare minos 14.0, separately from unchanged r4 |
| Worker source protocol pattern audit | PASS | Source-level heuristic only, not a complete security proof |
| Model manifest declared hashes | PASS | Validates **declared SHA-256 fields**, does not hash the local weight files; distribution not authorized |
| Direct worker JPEG IPC — MPS | PASS | Actual Worker health/inference roundtrip on macOS 27, returned **zero detections** |
| Direct worker JPEG IPC — CPU | PASS | Actual Worker health/inference roundtrip on macOS 27, returned **zero detections** |

**Exact result: 7 PASS, 0 FAIL, 1 BLOCKED, of 8 checks.** The runner now exits **2** when a required gate is BLOCKED, suppresses certification success markers and refuses hidden fallback paths/models. It does not fabricate fake model fixtures when files are missing.

The observed original-pack Mach-O count is **316** and original `minos` values are 11.0:152, 11.1:5, 13.0:46, 14.0:111, **27.0:2** (total 316). When the two R9 candidates are substituted **for inspection only** the histogram becomes 11.0:152, 11.1:5, 13.0:46, 14.0:113; no 10.9 cohort was observed in this pack. The pack was **not modified**.

## What the lab pipeline tests actually prove

The standalone H8 Python runner exercises **direct JPEG over worker Unix socket** and verifies Worker health and inference response on MPS and CPU. It does **not** exercise RTSP or FFmpeg and does **not** prove person detection: the fixture produces `detections_count=0`.

**Separate established H6D-R10 evidence** proves local loopback RTSP → R9 FFmpeg → bundled Worker → YOLO/MPS person detection under the H7B sandbox on this **macOS 27** host, with the source r4 pack left unchanged. Neither result constitutes H8 clean macOS 14 certification.

The H8 Go test uses a synthetic Vision Worker to test sink/worker contracts; it must not be described as real YOLO inference or a complete video decoder path.

## Reproduce safely (explicit local inputs only)

```bash
W=/path/to/offline-h8-worktree
LAB=/path/to/local-runtime-lab
python3 "$W/deploy/installer/test-h8-macos14-certification-runner.py" -v
python3 "$W/deploy/installer/h8-macos14-certification-runner.py" \
  --pack-dir "$LAB/packs/lab-2026.10.14-0e8657f-r4" \
  --candidates-dir "$LAB/r9-macos14-double-build" \
  --models-dir /path/to/locally-authorized-models \
  --json-out "$W/docs/architecture/h8-macos14-certification-evidence.json"
# EXPECTED exit 2: required PHYSICAL_MACOS14_BLOCKED; not a certification failure to work around
go test -race -count=1 ./internal/vision -run TestH8
```

Report output records only model **basenames** and response type/frame-sequence/count; personal absolute model paths from previous evidence were removed. Model artifacts remain private and outside Git.

## Remaining hard gates

1. Obtain an owner-authorized real macOS 14 ARM64 environment, run exact relocated native pack and R9 candidates, check ABI/dyld closure, Python/Torch/torchvision/SciPy runtime, MPS, FFmpeg and actual RTSP→YOLO person detection, test clean install/rollback and permissions.
2. H5/H6 Wails installation remains fail-closed by default pending a publisher-owned authenticated release manifest and trusted signing keys; independently verify backend.py, binaries and whole pack.
3. H7 system/application offline isolation is distinct from H7B process probe (localhost relay bypass remains a risk); run full cert on installed product.
4. Model and Ultralytics legal permissions, LGPL/FFmpeg redistribution, signed notarized release and commercial governance remain **blocked**.

**H8 is LAB PARTIAL, never release-certified.** No merge, deploy, release, pack mutation or model redistribution performed.
