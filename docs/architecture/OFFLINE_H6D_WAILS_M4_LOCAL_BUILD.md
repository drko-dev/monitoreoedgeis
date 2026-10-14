# H6D — First macOS ARM64 desktop shell build (local laboratory)

Date: 2026-10-14. Worktree: `offline-h6d`. **LAB APP BUILD PASS, NOT Full Edge Runtime READY.**

Built `cmd/geocam-edge-ui` on the authorized Mac M4 with Wails v2.16.0 (`go run github.com/wailsapp/wails/v2/cmd/wails@v2.16.0 build -platform darwin/arm64`). Built application:

`cmd/geocam-edge-ui/build/bin/geocam-edge-ui.app`

Lab-only ZIP (untracked and stored on the Mac, not published):

`runtime-lab/desktop-build/geocam-edge-ui-mac-m4-LAB.zip`

ZIP SHA256: `9f8d079efc6093e9637403cdd6ab475d1eb82fb7d31c9255757f2c4f9a5ad62f` (local artifact; not a release pin).

Checks: binary is Mach-O 64-bit arm64; `--version` prints `GEO CAM Edge Installer 0.1.0 (commit unknown, built unknown)`; local ad-hoc code-sign verification passed; ZIP integrity test passed. The build reported a macOS object linker min-version warning; it completed successfully. The app has **not** been GUI-launched, installed, commissioned or tested with isolated settings. It is NOT a certified installer or a packaged sealed Full Edge Runtime; the `unknown` provenance stamp is explicitly not suitable for release. The build contains no YOLO models and does not provide a hermetic FFmpeg runtime.

The frontend generation changed build-generated files; they were restored after producing the app. Local runtime-lab remains outside Git tracking via per-worktree `info/exclude`. No merge, release, tag, production change or commercial redistribution authorized.

Next gates: proper development stamp, explicitly isolated Wails launch (no real cluster/camera/service), GUI flow tests, assembled and externally pinned Runtime Pack, FFmpeg dependency closure, license audit, installation/recovery, H7B/H8.

Flags remain `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`.
