# H6D — repeated Go/YOLO and FFmpeg smoke on Mac M4 (2026-10-14)

**LAB PARTIAL; no RUNTIME_READY/INFERENCE_READY.** This exercise used read-only existing models and local untracked `runtime-lab`, not distributed binaries.

## Repeated Go-to-real-Worker inference

Real Go `internal/vision.Worker` started the real Python Worker over Unix socket, loaded existing `yolo11s-pose.pt` and `yolo11n.pt`, and returned responses for 5 synthetic 320x320 gray JPEGs on each device.

| Frame | CPU (ms) | MPS (ms) |
|---|---:|---:|
| 1 | 392.8 | 1891.6 |
| 2 | 36.9 | 76.8 |
| 3 | 38.5 | 87.6 |
| 4 | 36.9 | 160.7 |
| 5 | 36.2 | 85.4 |

All 10 inferences returned successfully; zero detections on blank inputs. Ultralytics warned on MPS about known pose issues and recommended CPU. These small, synthetic, non-randomized runs do **not** demonstrate realistic throughput, precision or CPU superiority across workloads. The first frame includes warmup.

## FFmpeg smoke (host tool, NOT hermetic)

Homebrew FFmpeg 8.1 encoded a 2-second 320x240 synthetic testsrc2 clip (10 frames) using libx264 to H.264 MP4. FFprobe confirmed codec h264, dimensions 320x240 and 10 frames. FFmpeg decoded a PNG frame 320x240 successfully. The installed FFmpeg was built with `--enable-gpl --enable-libx264` and dynamically links to Homebrew Cellar dylibs, as shown by `otool -L`. **Do not copy this single executable into a pack and claim it is standalone/hermetic**; it depends on external dylibs and the GPL distribution conditions need a separate decision. This smoke is local executable functionality only, not H4/H6 packaging certification.

## Missing gates

- Representative authorized camera frames and repeated benchmarks with successful detections
- Artifact-manifest pinned FFmpeg/FFprobe with native libraries, provenance, permissions and licensing; immutable runtime pack
- Network *enforcement* (offline environment flags are not a network-denial test)
- Integrated Go camera/frame pipeline and real RTSP, Wails UX, restart recovery and H7B/H8 certification
- Model redistribution still disallowed (`MODEL_REDISTRIBUTION_APPROVED=false`)

The local media files, harnesses, Python dependencies and models are excluded from this PR.
