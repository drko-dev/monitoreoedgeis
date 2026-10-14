# H6D — Go-to-real-Python vision worker laboratory proof (2026-10-14)

**LAB PASS (Go-to-real-Worker), NOT product READY.** Mac M4, Python standalone 3.11.9 and 36 hash-pinned wheels installed in local, untracked `runtime-lab`. Model weights were **read-only** from the sibling SaaS `geocam/models`; no model was copied, modified, redistributed, or committed.

An ephemeral Go harness (stored only in untracked `runtime-lab/go-worker-lab.go`) invoked the real `internal/vision.NewWorker` with `deploy/vision-worker/worker.py`, existing `yolo11s-pose.pt` and `yolo11n.pt`, Unix socket in temporary local directory, a 320x320 synthetic gray JPEG, explicit MPS or CPU selection, and context timeouts. Environment included `HF_HUB_OFFLINE=1` and `TRANSFORMERS_OFFLINE=1`; this is **not** proof of network-denial enforcement.

| Observation | MPS | CPU |
| --- | --- | --- |
| Worker health state | ready | ready |
| Real Go -> Unix socket -> Python -> YOLO -> Go response | PASS | PASS |
| Frame sequence | 1 | 1 |
| Detections on synthetic gray image | 0 | 0 |
| Reported inference time | 1595.3 ms | 395.6 ms |
| Ultralytics pose warning | Apple MPS known Pose bug | None observed |

These are **one-shot non-equivalent warmup observations**, not a throughput or speed comparison. The first direct (non-Go) MPS inference in the previous exercise was 15794.5 ms and also had the warning. Do not extrapolate performance or detection accuracy from these observations.

Previous `go test ./internal/vision -count=1` passed (fake-worker tests). Missing: authentic representative camera frames, repeated warmup/cold-run benchmark, RTSP decode, sealed FFmpeg binaries, runtime pack manifest trusted pin, enforced airgap, Wails integration, crash/restart recovery, H7B/H8. `RUNTIME_READY=false` and `INFERENCE_READY=false` remain.
