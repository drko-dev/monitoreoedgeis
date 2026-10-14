# GEO CAM — H7B outbound-IP network isolation (Mac M4 laboratory)

**Scope/status:** LAB EGRESS EVIDENCE PASS; H7B PARTIAL, H7 FULL CERTIFICATION **NOT VERIFIED**. This is a lab security test, not a production policy or legal distribution approval. Issue #156: https://github.com/drko-dev/monitoreoedgeis/issues/156

## Purpose and threat model

H7A's `pip`/PATH traps do not prove egress denial. The prior H6D sandbox profile `mac-lab-deny-ip-egress.sb` rejected outbound IP connections **including** the simulator's RTSP on loopback. H7B uses an explicitly opt-in *process-local* macOS `sandbox-exec` profile:

```scheme
(version 1)
(allow default)
(deny network-outbound (remote ip "*:*"))
(allow network-outbound (remote ip "localhost:*"))
```

The real profile is `deploy/installer/mac-h7b-loopback-egress.sb`; the harness rejects unexpected alterations of its audited contents. On the tested Mac, order matters: deny first, narrowly allow localhost last. This is not a global PF firewall, kernel/network namespace, or a general solution for services not launched inside this sandbox. It does not prove all possible protocols or privileged escapes are denied; the tests cover specific socket APIs and the exercised process tree.

## Verified observations (Mac M4, 2026-10-14)

- `/usr/bin/sandbox-exec` present and usable without `sudo`.
- Parent Python, sandboxed Python child, and a **sandbox-inherited grandchild** checked ephemeral `127.0.0.1:<port>` success and direct IPv4 TCP/UDP to documentation address `203.0.113.1:9` blocked with kernel `EPERM=1`.
- IPv6 TCP/UDP to reserved documentation destination `2001:db8::1:9` also returned `EPERM=1` on the Mac. A refusal or unroutable-network result does **not** count as an enforced network-denial pass.
- OS sandbox leaves local Unix-domain sockets usable for Go ↔ Vision Worker IPC and permits simulator RTSP TCP on loopback. The process runner forces `GOPROXY=off`, `GOSUMDB=off`, `PIP_NO_INDEX=1`, and offline model-hub flags, but those alone do not establish isolation.
- The sandbox runner invoked the existing **H6D-R5** Go test `TestLabRTSPToRealYOLO` using the non-release lab pack r4: local RTSP simulator → portable FFmpeg → bundled isolated Python/Worker → local YOLO model/MPS → person detection. Exit code 0 and literal `RTSP_GO_FFMPEG_YOLO_PASS` marker confirmed (approximately 35 seconds including orchestration/probes). The H.264 test fixture was previously generated using host FFmpeg; it is not a shipping dependency.
- No real camera, Internet endpoint, production instance, deployment, service restart, or global network rule was touched. The test target is a reserved documentation IP; the kernel denies the outbound socket call before establishing a connection.
- Test weights remain external local read-only references and are **not copied or redistributed**. `MODEL_REDISTRIBUTION_APPROVED=false`.

## Reproduction (opt-in, on the authorized Mac only)

Within the H7B worktree:

```bash
python3 deploy/installer/test-h7b-egress-cert.py
python3 deploy/installer/h7b-egress-cert.py
```

For the integrated lab E2E, from the H6D-R5 worktree, after checking `AGENTS.md`, disk budget, local model fixture and availability of the r4 lab pack:

```bash
H7=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h7b
H6=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
cd "$H6"
env PATH=/usr/bin:/bin \
  H6D_LAB_PACK="$H6/runtime-lab/packs/lab-2026.10.14-0e8657f-r4" \
  H6D_LAB_MODELS=/Users/gustavomarcon/Documents/proyectos/monitoreoia/geocam/models \
  H6D_LAB_H264=/tmp/geocam-h6d-person-30.h264 \
  H6D_LAB_DEVICE=mps \
  /usr/bin/python3 "$H7/deploy/installer/h7b-egress-cert.py" \
  --require-marker RTSP_GO_FFMPEG_YOLO_PASS -- \
  /opt/homebrew/bin/go test -count=1 -run '^TestLabRTSPToRealYOLO$' -v ./internal/vision
```

The harness produces a machine-readable JSON report and **never** emits `RUNTIME_READY` or `INFERENCE_READY`. No current lab pack is a signed/pinned release pack. Linux CI only tests fail-closed policy logic; macOS CI can validate the real boundary on the hosted runner, but does not execute YOLO/FFmpeg/model inference or certify a hardware rig.

## Gaps / NO-GO criteria

1. This test wraps Go, Python and FFmpeg in a lab sandbox. It does not yet prove actual packaged Wails app process-tree enforcement, clean-machine isolation, or no outbound egress outside the tested processes. `sandbox-exec` is deprecated and needs maintenance/target-platform review.
2. The OS policy allows localhost IP; it does not deny a local proxy that could relay traffic outside the sandbox. Proving full airgap requires isolation at the network/system boundary or auditing trusted local proxy surfaces, plus network monitoring.
3. FFmpeg portable decoder, bundled Python, Worker and YOLO functionality were exercised, but source provenance, independent trustworthy release manifest pin, Native Torchvision Mach-O portability on clean macOS, and legal approvals are still incomplete.
4. Model weights are external, the fixture was generated with host tooling beforehand, and the application installer remains fail-closed.
5. H7B needs review of platform network policy, negative cases and release evidence before it can claim complete certification. Do not merge solely on the above E2E evidence.

**State:** H7B LAB PARTIAL / RELEASE BLOCKED. No tag, release, merge, deploy, production or model redistribution authorized.
