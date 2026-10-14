# H6D — Network-isolated Go Worker and FFmpeg closure investigation (2026-10-14)

**PARTIAL / NOT OFFLINE-CERTIFIED.** All activity restricted to local Mac M4 worktree; no model weights or binaries committed.

## Network-denied Go/Python integration

- Plain `sandbox-exec` profile `(version 1)(deny network*)(allow default)` denied the real Python Worker's Unix-domain socket bind with `PermissionError: [Errno 1]`.
- Narrower `(version 1)(allow default)(deny network-outbound)` blocked outgoing TCP to 1.1.1.1:80 with `PermissionError: [Errno 1]`, **but it also blocked AF_UNIX socket connect** during a separate local bind/listen/connect probe, although AF_UNIX bind succeeded. The Go Worker printed that it was listening; a ready/infer result did not materialize.
- The Go/real-Worker handshake and inference already PASS **without** sandbox network-denial (previous H6D experiment). Single-process Python inference on real Matplotlib sample already PASS with blanket network denial. **Combined Go socket + enforced denial remains blocked on macOS sandbox policy design**. Do not claim H7B PASS; devise and independently verify a network isolation strategy that explicitly permits AF_UNIX while denying IPv4/IPv6 egress.

## FFmpeg/FFprobe dependency audit

Homebrew FFmpeg 8.1 has `--enable-gpl --enable-libx264`. A read-only recursive `otool -L` traversal from both ffmpeg/ffprobe found **27 /opt/homebrew paths (including the two executables)**, **zero missing resolved paths** and **zero `@loader_path`/`@rpath` references** within that observed traversal. macOS platform libraries are additional system dependencies (202 occurrences across enumerated entries, not distinct). This is a dependency inventory **only**; it is neither a portable bundle nor signature/license/code-sign/codesign/relocation or airgap certification. Host binary cannot simply be copied into a release pack.

The machine-readable raw inventory was generated in untracked `runtime-lab/ffmpeg-dependency-inventory.json` and **not** committed because it embeds host-specific paths. No files moved or downloaded.

## Next gates

1. Implement tested sandbox networking configuration that permits Unix sockets but denies TCP/UDP egress; validate deny-connection canary from the *same subprocess tree*.
2. Create a relocatable, legally appropriate, hash-pinned FFmpeg dependency closure in a separate lab-only artifact, then run against a restricted environment without Homebrew library paths.
3. Run real RTSP lab and accurate detections through Go, certification H7B and physical H8. Keep `RUNTIME_READY=false`, `INFERENCE_READY=false` and redistribution restrictions.
