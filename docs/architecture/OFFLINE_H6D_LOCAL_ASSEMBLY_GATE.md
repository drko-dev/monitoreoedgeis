# H6D — Local Runtime Pack assembly gap gate (2026-10-14)

**NO-GO for full Runtime Pack installation / Wails readiness.**

Read-only command:

```sh
python3 deploy/installer/check-local-runtime-assembly.py \
  --lab runtime-lab --ffmpeg-dir /opt/homebrew/Cellar/ffmpeg/8.1/bin
python3 deploy/installer/check-ffmpeg-dylib-closure.py \
  /opt/homebrew/Cellar/ffmpeg/8.1/bin
```

Mac M4 laboratory observations: installed standalone Python 3.11.9 passes version check; wheelhouse contains **36/36** hash/size-verified artifacts. First checker exits **1 / NOT_READY** because `internal/installer/assets/runtime-manifest.json` is a **template** (not an independently trusted release manifest). The second checker exits **1** with **17 external dylib dependencies** referenced by host Homebrew FFmpeg/FFprobe. This does not prove portable FFmpeg is available, or that licensing has been cleared. No release manifest or trusted pin was fabricated; no binaries or model weights committed.

The first checker inventories local prerequisites only; it **does not** verify the FFmpeg dylib closure or actual runtime inference. A future release must require *all* gates to pass independently and retain the existing fail-closed `NewAppWithPinnedRuntimePack` and production `NewApp()` behavior until the pack is complete. Do not claim `RUNTIME_READY` or `INFERENCE_READY` from this result. Distribution approval remains false.

Next: prepare and validate a relocatable, license-reviewed FFmpeg/FFprobe build with dependency closure; stage the remaining real artifacts and verify exact SHA/size; source the manifest's external trusted digest from the authorized embedding build; validate transactional provisioning and rollback under an isolated Wails UI. Until then leave the preparation adapter unavailable in the actual app.
