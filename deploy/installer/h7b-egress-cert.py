#!/usr/bin/env python3
"""H7B opt-in macOS laboratory outbound-IP denial check.

Uses a child-process sandbox, NOT a global firewall or a release certification.
Only loopback TCP is intentionally permitted. All probes use RFC5737 TEST-NET
and a local ephemeral server; no remote Internet endpoint is contacted.
"""
import argparse
import errno
import json
import os
from pathlib import Path
import socket
import subprocess
import sys

POLICY = '''(version 1)
(allow default)
; H7B Mac laboratory process-tree policy. Rules are intentionally ordered:
; first deny every outbound IP destination; then allow localhost for RTSP.
; Not a complete macOS network security boundary or production firewall.
(deny network-outbound (remote ip "*:*"))
(allow network-outbound (remote ip "localhost:*"))
'''
POLICY_PATH = Path(__file__).with_name("mac-h7b-loopback-egress.sb")
SANDBOX_EXEC = "/usr/bin/sandbox-exec"
TEST_NET_IPV4 = "203.0.113.1"
TEST_NET_IPV6 = "2001:db8::1"
DENIED_ERRNOS = (errno.EPERM, errno.EACCES)


def policy_matches():
    # Keep the audited repository file in sync with the embedded immutable
    # policy. Never pass this mutable file path to sandbox-exec (TOCTOU).
    try:
        return (not POLICY_PATH.is_symlink() and POLICY_PATH.is_file() and
                POLICY_PATH.read_text() == POLICY)
    except OSError:
        return False


def sandboxed_command(command):
    # -p loads the fixed audited policy from this process's memory, rather
    # than allowing a file replacement after policy_matches() returned true.
    return [SANDBOX_EXEC, "-p", POLICY, *command]


def outbound_tcp_result():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(1)
        return sock.connect_ex((TEST_NET_IPV4, 9))


def outbound_udp_result():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.settimeout(1)
        try:
            sock.sendto(b"h7b-denial-probe", (TEST_NET_IPV4, 9))
            return 0
        except OSError as exc:
            return exc.errno


def outbound_ipv6_tcp_result():
    with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as sock:
        sock.settimeout(1)
        return sock.connect_ex((TEST_NET_IPV6, 9))


def outbound_ipv6_udp_result():
    with socket.socket(socket.AF_INET6, socket.SOCK_DGRAM) as sock:
        sock.settimeout(1)
        try:
            sock.sendto(b"h7b-denial-probe", (TEST_NET_IPV6, 9))
            return 0
        except OSError as exc:
            return exc.errno


def loopback_result(port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(2)
        return sock.connect_ex(("127.0.0.1", port))


def probe(port):
    tcp = outbound_tcp_result()
    udp = outbound_udp_result()
    ipv6_tcp = outbound_ipv6_tcp_result()
    ipv6_udp = outbound_ipv6_udp_result()
    local = loopback_result(port)
    return {
        "tcp_test_net_errno": tcp,
        "udp_test_net_errno": udp,
        "localhost_errno": local,
        "ipv6_tcp_test_net_errno": ipv6_tcp,
        "ipv6_udp_test_net_errno": ipv6_udp,
        "tcp_egress_denied": tcp in DENIED_ERRNOS,
        "udp_egress_denied": udp in DENIED_ERRNOS,
        "ipv6_tcp_egress_denied": ipv6_tcp in DENIED_ERRNOS,
        "ipv6_udp_egress_denied": ipv6_udp in DENIED_ERRNOS,
        "localhost_allowed": local == 0,
    }


def probe_passes(result):
    return bool(
        result.get("tcp_egress_denied") and
        result.get("udp_egress_denied") and
        result.get("ipv6_tcp_egress_denied") and
        result.get("ipv6_udp_egress_denied") and
        result.get("localhost_allowed")
    )


def child_main(port, grandchild=False):
    result = probe(port)
    if not grandchild:
        proc = subprocess.run(
            [sys.executable, "-I", str(Path(__file__).resolve()),
             "--grandchild", str(port)],
            text=True, capture_output=True, timeout=8,
            check=False,
        )
        nested = {}
        if proc.returncode == 0:
            try:
                nested = json.loads(proc.stdout)
            except json.JSONDecodeError:
                pass
        result["grandchild_returncode"] = proc.returncode
        result["grandchild_probe"] = nested
        result["grandchild_policy_inherited"] = (
            proc.returncode == 0 and probe_passes(nested)
        )
    print(json.dumps(result, sort_keys=True))
    return 0 if probe_passes(result) and (
        grandchild or result["grandchild_policy_inherited"]
    ) else 1


def mac_preflight():
    if sys.platform != "darwin":
        return "macOS sandbox-exec is unavailable on this platform"
    if not Path(SANDBOX_EXEC).is_file():
        return "sandbox-exec executable is unavailable"
    if not policy_matches():
        return "sandbox policy differs from the audited loopback-only policy"
    return ""


def self_test():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as server:
        server.bind(("127.0.0.1", 0))
        server.listen(8)
        port = server.getsockname()[1]
        try:
            run = subprocess.run(
                sandboxed_command([
                    sys.executable, "-I", str(Path(__file__).resolve()),
                    "--child", str(port),
                ]),
                capture_output=True, text=True, timeout=20, check=False,
                env=audited_environment(),
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            return {"status": "NOT_VERIFIED", "reason": type(exc).__name__}
        result = {}
        try:
            result = json.loads(run.stdout)
        except json.JSONDecodeError:
            pass
        return {
            "status": "BOUNDARY_PROBE_PASS" if
            run.returncode == 0 and probe_passes(result) and
            result.get("grandchild_policy_inherited") else "NOT_VERIFIED",
            "sandbox_returncode": run.returncode,
            "child": result,
            "note": "OS sandbox probe only; not a certified clean-machine airgap",
        }


def audited_environment():
    # Opt-in laboratory command: pass only expected fixture/tooling paths and
    # minimum OS defaults. Never inherit credentials, proxies, host PYTHONPATH,
    # dynamically injected libraries, or developer Homebrew PATH fallbacks.
    allowed = {
        "HOME", "USER", "TMPDIR", "LANG", "LC_ALL",
        "H6D_LAB_PACK", "H6D_LAB_MODELS", "H6D_LAB_H264",
        "H6D_LAB_DEVICE", "GOCACHE", "GOMODCACHE",
    }
    env = {key: value for key, value in os.environ.items()
           if key in allowed}
    env.update({
        "PATH": "/usr/bin:/bin",
        "PIP_NO_INDEX": "1", "GOPROXY": "off", "GOSUMDB": "off",
        "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1",
    })
    return env


def main():
    if len(sys.argv) == 3 and sys.argv[1] in ("--child", "--grandchild"):
        return child_main(int(sys.argv[2]), sys.argv[1] == "--grandchild")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--require-marker", default="",
                        help="literal marker required in sandboxed command output")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    report = {
        "hito": "H7B", "platform": sys.platform,
        "runtime_ready": False, "inference_ready": False,
        "model_redistribution_approved": False,
        "status": "NOT_VERIFIED",
    }
    failure = mac_preflight()
    if failure:
        report["reason"] = failure
        print(json.dumps(report, sort_keys=True))
        return 2
    boundary = self_test()
    report["boundary"] = boundary
    if boundary["status"] != "BOUNDARY_PROBE_PASS":
        print(json.dumps(report, sort_keys=True))
        return 1
    command = args.command
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        report["status"] = "BOUNDARY_PROBE_PASS_NOT_AIRGAP_CERTIFIED"
        print(json.dumps(report, sort_keys=True))
        return 0
    if not Path(command[0]).is_absolute():
        report["reason"] = "sandboxed executable must have an absolute path"
        print(json.dumps(report, sort_keys=True))
        return 2
    if not args.require_marker:
        report["reason"] = (
            "a nonempty --require-marker is required for functional evidence"
        )
        print(json.dumps(report, sort_keys=True))
        return 2
    try:
        run = subprocess.run(
            sandboxed_command(command),
            capture_output=True, text=True, timeout=150,
            check=False, env=audited_environment(),
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        report["reason"] = type(exc).__name__
        print(json.dumps(report, sort_keys=True))
        return 1
    matched = not args.require_marker or args.require_marker in (
        run.stdout + run.stderr
    )
    report["sandboxed_command_exit"] = run.returncode
    report["required_marker_found"] = bool(matched)
    report["status"] = (
        "LAB_EGRESS_EVIDENCE_PASS_NOT_CERTIFIED"
        if run.returncode == 0 and matched else "NOT_VERIFIED"
    )
    # Deliberately do not echo test output: it may contain local camera URIs.
    print(json.dumps(report, sort_keys=True))
    return 0 if run.returncode == 0 and matched else 1


if __name__ == "__main__":
    raise SystemExit(main())
