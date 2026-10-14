# GEO CAM Full Edge — standalone private Mac installer test

**2026-10-14 — PRIVATE LOCAL LAB ONLY; no commercial distribution, deploy or release.**

## Functional artifact (physically produced on owner Mac)

`~/Downloads/GEO-CAM-Full-Edge-PORTABLE-LAB-2026-10-14.dmg` — **670 MB**. Open in Finder and drag the included `GEO-CAM-Full-Edge-PORTABLE-LAB-2026-10-14.app` to Applications. Launch it and click **Probar Full Edge local** at the top of the installer GUI, above SaaS enrollment.

SHA-256 DMG: `d2920cfed74b4939b863b1b2c2a72a2711146c4b5d72bc22e3a55d46e409ef1f`.

The app itself is ~1.1 GB and contains the **complete** local runtime under `Contents/Resources/RuntimePack`: independently packaged Python 3.11.9 with already-installed Torch 2.4.0, Ultralytics 8.3.1, all required Python dependencies, offline wheelhouse, FFmpeg/FFprobe and Python Worker. The **two user-owned local test weights** `yolo11n.pt` and `yolo11s-pose.pt` are locally copied to `Contents/Resources/Models`; prerecorded four-frame H264 laboratory source is bundled in `Contents/Resources/Fixtures`. The existing Go H6D real inference harness and macOS H7B sandbox are in `Contents/Resources`.

All bundle binaries and model weights are included **only in the owner-local DMG**; **none are committed or uploaded to GitHub**. Normal product releases and default Wails `NewApp()` are unchanged/fail closed.

## Validation evidence — explicitly executed, not speculative

- Native macOS ARM64 Wails executable rebuilt from current Go/UI. App ad-hoc signed using `codesign --force --sign -` on **outer bundle only** to avoid changing pinned Runtime Pack bytes. `codesign --verify --deep --strict` **PASS**.
- `RuntimePack/python/bin/python3.11 -I -c 'import torch,ultralytics'`: **PASS**, Torch 2.4.0, Ultralytics 8.3.1 from bundle.
- From a new temporary working directory, with `env -i`, synthetic empty HOME and no developer PATH/GO/PYTHON environment variables: **`APP/Contents/MacOS/geocam-edge-ui --private-lab-self-test` returned passed=true**, detecting a person via RTSP simulator -> FFmpeg -> Worker -> YOLO with Apple MPS, under the H7B outbound-denied sandbox. This executes the **same Go handler** called by the UI button.
- Re-ran self-test **after ad-hoc signing** and got the same result: **PASS**.
- Created UDZO disk image with app, Applications shortcut and Spanish instructions; `hdiutil verify` **PASS**.
- **Mounted the final DMG read-only**, verified app signature from the mounted image, reran the exact full inference self-test with clean HOME/PATH **PASS**; launched the Wails GUI from read-only mounted DMG, observed it running after 9 seconds **PASS**. Then unmounted successfully.
- Before integration, repaired H6D 4-frame fixture: looping it 12 times via the loopback RTSP simulator prevents buffered FFmpeg output from starving inference. Real proof from owner's models: `RTSP_GO_FFMPEG_YOLO_PASS frame=1 detections=1`.
- No SaaS, production camera, real-network service, device enrollment, deploy or release was required or touched. Testing on macOS 27.0.1 ARM64. A clean macOS14 physical machine remains untested.

## Source architecture

The new Go handler `RunPrivateFullEdgeLabTest()` is compiled **only** under `-tags privatefullgelab` and derives *all* Python, FFmpeg, model and fixture locations from the invoking app's `Contents/Resources`. It accepts no camera URL, arbitrary command or credentials, and never falls back to developer worktrees. It spawns only the bundled local E2E harness through the bundled audited H7B runner and returns sanitized JSON. The LAB-only CLI flag `--private-lab-self-test` calls the **same** Go handler without UI automation. The frontend private panel is built with `VITE_LOCAL_FULL_EDGE_LAB=1`; both backend and UI are excluded from normal production builds.

## Known limits — do not misrepresent to user

This is a **standalone functional private installer/demo for real inference on the included loopback video**. It is **not yet the finished consumer installer** that automatically configures a background Edge daemon, connects to user-selected real cameras, enrolls to SaaS, handles full installation/recovery or runs inference as a persistent production service. File-mode/ownership policy and signed release/physical H7/H8 certifications remain separate. Ad-hoc local signing is not Apple Developer ID notarization; Apple's Gatekeeper on other Macs may block it. No commercial redistribution permission is asserted, and no readiness flags were changed.

## macOS owner-local build outline

Reproduce using only a personally authorized pack + models from that operator's Mac:

1. Build Wails frontend with `VITE_LOCAL_FULL_EDGE_LAB=1 npm run build`; build native binary with `CGO_LDFLAGS='-framework UniformTypeIdentifiers' go build -tags production,privatefullgelab` into an .app's `Contents/MacOS`.
2. Copy the operator-approved lab pack **with APFS clone** (not hardlinks or references to worktree) into `Contents/Resources/RuntimePack`; copy actual operator-owned weights into `Contents/Resources/Models`, H264 test stream into `Fixtures` and Go E2E harness + sandbox policy/runner into `Contents/Resources`.
3. Ad-hoc sign the **outer app** and verify `codesign --verify --deep --strict`.
4. Exercise `--private-lab-self-test` with **empty HOME and `env -i`** (without any source directories). Package as UDZO .dmg with Applications shortcut.
5. **Mount the DMG read-only, re-run self-test from within the mounted app**, and GUI smoke-test before handing it to the owner. No remote model publishing.

**Only local app bundles/DMG were built. No release, merge or deployment.**
