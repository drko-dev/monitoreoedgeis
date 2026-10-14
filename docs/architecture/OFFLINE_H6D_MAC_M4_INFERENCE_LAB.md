# H6D Mac M4 local inference proof — 2026-10-14

Status: **LAB VALIDATED PARTIAL**. Not release-ready or an offline certification.

- Python 3.11.9 standalone installed in untracked runtime-lab; the 36 hash-pinned wheels installed with pip --no-index --require-hashes --no-deps. pip check: no broken requirements.
- Torch 2.4.0, torchvision 0.19.0, OpenCV 4.11.0, Ultralytics 8.3.1 imported. Torch MPS was built and available; a tensor operation returned 4.0 on mps:0.
- Models yolo11s-pose.pt and yolo11n.pt were read **without alteration or copying** from the local sibling SaaS geocam/models directory. Their existing presence does not certify redistribution rights, provenance, or model hashes.
- Direct YOLOBackend.load() with device=mps reported ready=true and loaded two models. A synthetic gray JPEG 320x320 underwent a direct YOLOBackend.infer() call: 0 detections, 15,794.5 ms elapsed. Blank image gives no detection accuracy evidence. First-run/startup timing and pose-on-MPS warning may affect performance; no benchmark claim.
- Ultralytics emitted **Apple MPS known Pose bug** warning and recommended CPU for pose; validate CPU pose and realistic frames before any inference-ready claim.
- Go internal/vision: go test ./internal/vision -count=1 passed. These are Go unit tests with fake/test workers, **NOT** a Go-to-real-Worker integration proof.
- Not tested: Go <-> real Python Worker Unix socket, camera RTSP, FFmpeg hermetic packaging, runtime manifest pin, actual no-network-enforcement tests, functional accuracy, restarts, Wails setup, physical H7B/H8.
- Mac host models and executable caches must not be included in any PR, installer, or Runtime Pack.
- Preserve flags MODEL_REDISTRIBUTION_APPROVED=false, RUNTIME_READY=false, INFERENCE_READY=false.

Next: local supervised Go-to-real Python worker smoke using a Unix socket and existing model paths, pose CPU comparison and realistic authorized laboratory images, then hermetic FFmpeg and network-denial H7B testing.
