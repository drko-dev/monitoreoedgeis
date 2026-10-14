# Full Edge private macOS LAB app — one-click real inference

**2026-10-14, private local testing only. Not a product release or independent installer certification.**

## Verified on owner Mac (macOS 27.0.1 ARM64)

- Native Wails GUI built using local source and the existing macOS .app skeleton, with `CGO_LDFLAGS="-framework UniformTypeIdentifiers"`, Go `-tags production,privatefullgelab` and `VITE_LOCAL_FULL_EDGE_LAB=1` frontend flag.
- App ad-hoc signed with `codesign --deep --force --sign -`; `codesign --verify --deep --strict` PASS; opened as a desktop process in an isolated HOME/data directory and remained alive through a 12-second smoke test.
- Private UI includes **Probar Full Edge local** above enrollment, reachable without SaaS registration or camera onboarding. Calls `App.RunPrivateFullEdgeLabTest()` only when tagged `privatefullgelab`; normal shipping builds omit both the method and the panel.
- The app bundle embeds a compiled Go *test harness binary* (`Contents/Resources/geocam-lab-e2e.test`) and audited H7B loopback-only sandbox runner/policy; no Go toolchain is needed to run the test from the .app. The harness reads operator-owned pack and model files at their **existing local** paths, not from inside the ZIP.
- H6D lab fixture test fixed to **replay a four-frame H264 file 12 times** over a loopback RTSP simulator: one short stream caused FFmpeg to buffer without output. E2E observed `RTSP_GO_FFMPEG_YOLO_PASS frame=1 detections=1` (PASS 8.49s) with user's local pose/person YOLO models on MPS, offline Python and pack FFmpeg. Separate R9 FFmpeg candidate under H7B denied-egress sandbox returned `LAB_EGRESS_EVIDENCE_PASS_NOT_CERTIFIED`.
- Running the compiled **app-bundled** harness under H7B sandbox returned `LAB_EGRESS_EVIDENCE_PASS_NOT_CERTIFIED` with successful marker and exit 0. No real camera, service installation, SaaS/Internet call, model copy or release action.

## Owner-ready artifacts (not in Git)

`~/Downloads/GEO-CAM-Full-Edge-LAB-2026-10-14.app` and corresponding `.zip`, plus `GEO-CAM-Full-Edge-LAB-LEEME.txt`. Signed locally (not Developer ID/notarized). SHA-256 of ZIP: `23c7a79229231fca2a716b172c39d2472c05aade00c58010c530b84613cdb4c4`.

## Requirements and limitations

1. Current app is **lab test interface**, not a fully self-contained production installer. Its Python/FFmpeg pack resides in `~/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d/runtime-lab/packs/lab-2026.10.14-0e8657f-r4`, H264 fixture in the same lab tree, and user models in `~/geocam-edge-data-fulledge/models/`. Removing those locations blocks the test; they are intentionally **not copied into the ZIP**.
2. Test panel uses no Wails input that could choose an arbitrary executable, camera, command or credential. It accepts no user-supplied command line. Bound helper paths are derived from `os.UserHomeDir()`. Runner only accepts audited paths and returns sanitized H7B JSON.
3. MacOS 14 clean-host physical certification, signed/verifiable installed Runtime Pack, actual installer prepare/recover transaction linked to GUI, real camera live onboarding, model and FFmpeg redistribution rights, code-signing/notarization all remain **separate blocked tasks**. Lab-model loading is local testing evidence, not product distribution authorization.
4. No deploy, release, merge, tags or mutation of installed production services. This feature is **build-tag gated**, default Wails `NewApp()` remains fail closed, and the lab UI does not assert READY.

## Re-run source-level local E2E (only with explicit lab data)

```bash
env GOPROXY=off GOSUMDB=off HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 \
  H6D_LAB_PACK="$HOME/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d/runtime-lab/packs/lab-2026.10.14-0e8657f-r4" \
  H6D_LAB_MODELS="$HOME/geocam-edge-data-fulledge/models" \
  H6D_LAB_H264="$HOME/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d/runtime-lab/media-smoke/person-annexb.h264" \
  H6D_LAB_DEVICE=mps \
  go test -run '^TestLabRTSPToRealYOLO$' -count=1 -v ./internal/vision
```

**No network or production changes are required for this test.**
