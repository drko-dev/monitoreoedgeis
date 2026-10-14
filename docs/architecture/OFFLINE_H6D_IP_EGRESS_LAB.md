# H6D — Mac M4 Go / real Worker under IP-egress denial (2026-10-14)

**LAB TEST PASS; NOT FULL AIRGAP OR PRODUCT READY.** All tests were on the authorized Mac using existing local weights read-only. The app/SaaS and shipping runtime were not modified.

## Policy and canaries

Committed lab-only profile: `deploy/installer/mac-lab-deny-ip-egress.sb`

The macOS sandbox policy `(deny network-outbound (remote ip "*:*"))` with `(allow default)` **permits AF_UNIX socket bind/listen/connect** (local canary PASS) while **denying outbound TCP to public IPv4 (1.1.1.1:80) and IPv6 (2606:4700:4700::1111:80)** with PermissionError / EPERM (both PASS). Previously tested `(deny network*)` and `(deny network-outbound)` also blocked Unix-domain socket connections, preventing Worker health checks; they should not be used for this IPC topology.

Run under the same profile:
```sh
HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 YOLO_CONFIG_DIR=/tmp/geocam-h6d-yolo-lab \
  /usr/bin/sandbox-exec -f deploy/installer/mac-lab-deny-ip-egress.sb \
  go run ./runtime-lab/go-worker-repeat-cpu.go
```
The harness and pinned Python/wheels reside in local **untracked** `runtime-lab` and are not shipped. Real `internal/vision.Worker` started the Python `worker.py` process, connected through Unix socket, reported `GO_WORKER_READY ready cpu`, and completed **5/5 synthetic JPEG inference round trips**. Observed latency: 373.2, 33.9, 40.6, 34.2, and 39.5 ms. Empty synthetic frames had zero detections; no accuracy claims.

## Remaining certification limits

This *targeted IP-egress sandbox* is not proof of complete denial of all network mechanisms, DNS, UDP, all sub-process behavior, access to local HTTP services, traffic from independent processes, or full app airgap. An explicit packet / UDP / DNS negative suite and process-tree verification remain for H7B. No RTSP video, hermetic FFmpeg/FFprobe distribution, Wails production integration, trusted release pin, or reboot validation was performed.

Keep `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`; do not treat this lab profile as shipping security policy.
