# H6D — Enforced-network-denial real-image inference (Mac M4, 2026-10-14)

**PARTIAL LAB VALIDATION — NOT Full Edge READY.**

Local `sandbox-exec` profile `(version 1)(deny network*)(allow default)` was validated by attempting a TCP connection to 1.1.1.1:80 from the standalone Python 3.11.9 interpreter. It failed with `PermissionError: [Errno 1] Operation not permitted` (nonzero exit). This is a macOS local process sandbox test, **not an airgapped deployment** or proof of isolation of other processes.

With the same deny-network profile, Ultralytics 8.3.1 / torch 2.4.0 loaded the existing two models *read-only* from the adjacent local SaaS project. The CPU YOLOBackend inferred on Matplotlib's bundled `mpl-data/sample_data/grace_hopper.jpg` (512x600) and returned **one person detection, 0.96 confidence**, inference 412.1 ms. No sample, frame, model, or user data was committed. Lab harness is confined to untracked `runtime-lab/`.

Mac Homebrew FFmpeg 8.1 produced H.264 MP4 and decoded test frame in previous lab; current `otool -L` shows **18 Homebrew-linked library entries** on FFmpeg and configure flags `--enable-gpl --enable-libx264`. This *host* executable is not hermetic. Do not copy it to the pack without dependency closure and rights/provenance audit. Licensing for local use and redistribution remain distinct.

Outstanding: real Go-supervised Worker test inside enforced network denial; actual RTSP laboratory stream; ffmpeg/ffprobe relocatable self-contained runtime with dependency closure; provenance/license review; verified release pin; Wails and H7B/H8. `RUNTIME_READY=false`; `INFERENCE_READY=false`; `MODEL_REDISTRIBUTION_APPROVED=false`.
