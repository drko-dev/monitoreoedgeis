# H6D — Mac M4 minimally linked FFmpeg/FFprobe laboratory binaries (2026-10-14)

**DECODING LAB PASS; RELEASE NOT AUTHORIZED OR CERTIFIED.**

Source obtained on the authorized Mac from `https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz` with detached `.asc` alongside, in untracked `runtime-lab/ffmpeg-source`. Tarball SHA-256 `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`. **PGP release signature was NOT verified** (`gpg` unavailable locally). Hash is an observed local digest, not a previously trusted pin.

Configured source with `--disable-everything --disable-autodetect --disable-network --enable-static --disable-shared --disable-programs --enable-ffmpeg --enable-ffprobe --enable-protocol=file,pipe --enable-demuxer=h264,mov,matroska --enable-muxer=null,image2,rawvideo --enable-parser=h264 --enable-decoder=h264 --enable-encoder=rawvideo,wrapped_avframe --enable-filter=scale,format --enable-swscale --enable-avformat --enable-avcodec --enable-avutil --disable-doc`. No GPL, libx264 or external optional libraries enabled. Configure reports **LGPL 2.1 or later**, subject to independent legal review. Static link here means FFmpeg libraries included in executable; macOS system libraries/frameworks remain dynamic dependencies.

Built in an **untracked** local directory, executables at `runtime-lab/ffmpeg-portable/{ffmpeg,ffprobe}`. Read-only dylib closure audit: **PASS**, no dependencies outside macOS system libraries/frameworks. SHA-256 of local final binaries:

- `ffmpeg`: `9e97cd0015b2ed2e2b3bba0401f8554b7cceb93b27558f2ee309ffd824d29b07`
- `ffprobe`: `ebf3c455845fee554bfb9ffa0e4bab6268c589c199852b0c3c641b4598282567`

`ffprobe` successfully recognized laboratory MP4's H264 stream (320 x 320). Portable FFmpeg successfully decoded an existing 4-frame H264 Annex-B fixture to 614,400 bytes of yuv420p, exactly 4 x (320 x 320 x 3/2). **This is a real decode smoke test**. The existing `TestFFmpegDecoder_DecodesRealClip` deliberately generates its fixture from `lavfi` using `libx264`; that generator cannot run through this minimal binary. Executing that test with the minimal binary at the front of `PATH` failed at fixture generation (`Unknown input format: lavfi`), **not at decoding**. It needs a pre-generated fixture or a test-specific separation of generator from decoder to validate the Go decoder binary integration.

This binary is a **minimal H264 decoder/prober**, not feature-compatible with host Homebrew FFmpeg. No RTSP network input (intentionally disabled); the Go agent owns RTSP and feeds elementary H264 over pipes. No signed/notarized binary, redistribution clearance, full package manifest, external trust pin, Runtime Pack activation or Wails provisioning. Do not infer release eligibility from locally observed hashes. `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`. No weights or commercial release artifacts committed.
