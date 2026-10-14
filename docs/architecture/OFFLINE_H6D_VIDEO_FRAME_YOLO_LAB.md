# H6D — H.264 decoded person frame to real Go / YOLO Worker (2026-10-14)

**LAB PASS for file-based media-to-detection, PARTIAL for real RTSP and hermetic distribution.**

Source input: bundled Matplotlib sample `grace_hopper.jpg` (no private SaaS image). On local Mac M4, host Homebrew FFmpeg 8.1 created a 2-second H.264 MP4 with 4 frames at 320x320, then decoded one frame to JPEG. FFprobe verified codec H.264 and 4 frames. These are generated files under untracked `runtime-lab/media-smoke` and not committed.

A local untracked Go harness based on `internal/vision.Worker` read the decoded JPEG and sent it through the **real** supervised Go -> AF_UNIX socket -> Python 3.11 Vision Worker -> YOLO backend -> Go round trip. Two pre-existing local models were read in place. Under macOS `sandbox-exec -f deploy/installer/mac-lab-deny-ip-egress.sb` and HF offline environment flags, **5/5** real CPU inferences returned **one person detection each**. Observed timings: 359.0, 38.4, 35.2, 35.1, 36.3 ms. This is a small one-image repeated lab experiment, not representative throughput.

Separately, all Go tests for `internal/rtsptest`, `internal/rtsp`, `internal/processing` passed, including RTSP simulation. **These two parts are not yet one connected RTSP -> decoded frame -> inference system test**. A real RTSP video server streaming decodable valid H.264 and full agent routing remains untested.

Homebrew's FFmpeg/FFprobe remain dynamically linked to external Homebrew libraries. No hermetic FFmpeg pack assembled or released. The closure checker correctly rejects host binaries. No downloads of weights, commercial deployment, network exposure, product READY, production or camera modifications.

Remaining: build/procure legally usable, relocatable FFmpeg/FFprobe with verified dependency closure and hashes, direct RTSP with valid video through real Go pipeline, durable Wails integration, restart and offline H7B/H8 certification. Keep `RUNTIME_READY=false`, `INFERENCE_READY=false`, model redistribution unapproved.
