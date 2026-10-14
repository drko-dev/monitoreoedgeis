# H6D — Integrated RTSP to real YOLO detection on Mac M4 (2026-10-14)

**LAB FUNCTIONAL E2E PASS, NOT OFFLINE/RELEASE READY.**

Opt-in integration test `internal/vision/h6d_full_lab_test.go` exercises the actual local path in **one process integration test**: loopback `internal/rtsptest.Simulator` -> actual `rtsp.Manager` -> `processing.Manager` -> actual FFmpeg H.264 decoder -> real `vision.Sink` -> supervised Python 3.11 Worker on AF_UNIX -> YOLO models -> detected event returned to Go through a lab-only `EventConsumer`. The test checks a **person** detection, not merely process startup or blank-frame inference.

Only the bundled Matplotlib `grace_hopper.jpg` test picture was used, encoded to RTP/H264 by host FFmpeg as a transient fixture. Existing local weights `yolo11s-pose.pt` and `yolo11n.pt` were read only from sibling SaaS; no client cameras, live SaaS services or models changed.

Observed `go test ./internal/vision -run '^TestLabRTSPToRealYOLO$' -count=1 -v`: **PASS**, `RTSP_GO_FFMPEG_YOLO_PASS frame=1 detections=1`. Default `go test ./internal/vision -count=1` PASS. The test is skipped without explicit `H6D_LAB_PYTHON`, `H6D_LAB_MODELS`, and `H6D_LAB_IMAGE` env vars, so CI does not need model weights. Transient H264 is produced inside the test and sent via RTP with FU-A for fragmented NAL units.

**Important limits:** The RTSP server is simulated locally rather than a physical camera. Actual FFmpeg executable is the non-hermetic Homebrew 8.1 binary. This test is **not** enclosed in the earlier IP-egress sandbox, because an RTSP loopback TCP listener is required. No enforced airgap, packaging, source rights review, production load, service restart, alert persistence or Wails installer certification. Models remain unapproved for redistribution (`MODEL_REDISTRIBUTION_APPROVED=false`). Maintain `RUNTIME_READY=false`, `INFERENCE_READY=false`.

Next: isolate internet egress without blocking local RTSP TCP and Worker AF_UNIX; build a relocatable, legally usable and hash-pinned FFmpeg/FFprobe closure; run H7B/H8 restart/install/disconnect tests.
