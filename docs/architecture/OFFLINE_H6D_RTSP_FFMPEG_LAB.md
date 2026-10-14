# H6D — RTSP simulation and FFmpeg dylib closure, Mac M4 (2026-10-14)

**Partial local laboratory result. Not READY or release certified.**

## RTSP

Executed selected Go tests in `internal/rtsptest`, `internal/rtsp`, and `internal/processing` using a local synthetic RTSP server. The simulator exercises DESCRIBE / SETUP / PLAY / TEARDOWN and interleaved RTP packets; `TestStopRacingLivePacketPathDoesNotDeadlock` exercises concurrent Stop/packet paths against actual Go managers. Selected tests **PASS**. This is not a real camera video decode or an end-to-end RTSP -> JPEG -> YOLO -> alert proof. No production or client cameras touched.

## macOS FFmpeg dylib closure

Read-only `deploy/installer/check-ffmpeg-dylib-closure.py` traverses dependencies reported by `otool -L` for `ffmpeg` and `ffprobe` in an expected bundle root. It fails on missing binaries, absolute dylib paths outside the bundle and unresolved `@...` paths. It allows macOS system paths under `/System/Library` and `/usr/lib`. Additional relocation, hardened runtime, code-signing and runtime verification are still necessary.

The checker correctly returned **FAIL** (exit 1) on Homebrew FFmpeg 8.1's `bin` directory, reporting 17 distinct external library dependencies for the executables. These libraries exist in Homebrew but are not under the `bin` bundle root. **Do not treat Homebrew FFmpeg as hermetic or copy it into a release pack**. Source/license review remains outstanding because this FFmpeg is GPL-enabled and uses libx264.

The checker is a **partial link-closure audit**, not a signing/packaging/provenance validator. Future packaging must stage a relocatable, legally usable build with its exact dependencies and demonstrate it works without Homebrew in the lookup path.

## Still pending

RTSP media sample with valid decodable frames, Go processing -> real YOLO under correctly scoped network isolation, pinned self-contained FFmpeg/FFprobe, Wails wiring and offline H7B/H8. `RUNTIME_READY=false`, `INFERENCE_READY=false` and model redistribution remains disallowed.
