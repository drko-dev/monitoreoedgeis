#!/usr/bin/env python3
"""Synthetic H7B policy validation, safe on all supported CI platforms."""
import importlib.util
import io
import contextlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

MODULE = Path(__file__).with_name("h7b-egress-cert.py")
spec = importlib.util.spec_from_file_location("h7b_egress_cert", MODULE)
h7b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h7b)


class H7BPolicyTests(unittest.TestCase):
    def test_profile_exact_and_ordered(self):
        self.assertTrue(h7b.policy_matches())
        deny = h7b.POLICY.index('(deny network-outbound (remote ip "*:*"))')
        allow = h7b.POLICY.index('(allow network-outbound (remote ip "localhost:*"))')
        self.assertLess(deny, allow)
        self.assertNotIn("network-outbound (remote ip \"*:*\"))\n(allow default)", h7b.POLICY)

    def test_modified_profile_fails_closed(self):
        with tempfile.TemporaryDirectory() as root:
            with mock.patch.object(h7b, "POLICY_PATH", Path(root, "missing")):
                self.assertFalse(h7b.policy_matches())
            changed = Path(root, "changed.sb")
            changed.write_text(h7b.POLICY.replace("(deny network-outbound", "(allow network-outbound"))
            with mock.patch.object(h7b, "POLICY_PATH", changed):
                self.assertFalse(h7b.policy_matches())

    def test_probes_require_both_protocols_and_loopback(self):
        good = {
            "tcp_egress_denied": True, "udp_egress_denied": True,
            "ipv6_tcp_egress_denied": True, "ipv6_udp_egress_denied": True,
            "localhost_allowed": True,
        }
        self.assertTrue(h7b.probe_passes(good))
        for key in good:
            broken = dict(good, **{key: False})
            self.assertFalse(h7b.probe_passes(broken))

    def test_proxy_and_dynamic_loader_environment_not_inherited(self):
        with mock.patch.dict(os.environ, {
            "DYLD_INSERT_LIBRARIES": "/host/malicious.dylib",
            "PYTHONPATH": "/host/packages", "HTTP_PROXY": "http://host:8080",
            "HTTPS_PROXY": "http://host:8080", "ALL_PROXY": "socks5://host:1080",
            "FTP_PROXY": "http://host:8080", "NO_PROXY": "localhost",
            "http_proxy": "http://host:8080", "https_proxy": "http://host:8080",
            "all_proxy": "socks5://host:1080", "no_proxy": "localhost",
            "CURL_CA_BUNDLE": "/host/evil.crt", "SSL_CERT_FILE": "/host/evil.crt",
            "RES_OPTIONS": "nameserver:8.8.8.8", "HOSTALIASES": "/host/hosts",
            "GOPROXY": "https://proxy.invalid",
        }):
            env = h7b.audited_environment()
        for key in (
            "DYLD_INSERT_LIBRARIES", "PYTHONPATH", "HTTP_PROXY", "HTTPS_PROXY",
            "ALL_PROXY", "FTP_PROXY", "NO_PROXY", "http_proxy", "https_proxy",
            "all_proxy", "no_proxy", "CURL_CA_BUNDLE", "SSL_CERT_FILE",
            "RES_OPTIONS", "HOSTALIASES",
        ):
            self.assertNotIn(key, env)
        self.assertEqual(env["GOPROXY"], "off")
        self.assertEqual(env["PIP_NO_INDEX"], "1")
        self.assertEqual(env["HF_HUB_OFFLINE"], "1")

    def test_sandbox_launch_uses_fixed_policy_not_a_reopenable_file(self):
        cmd = h7b.sandboxed_command(["/usr/bin/true"])
        self.assertEqual(cmd, ["/usr/bin/sandbox-exec", "-p",
                               h7b.POLICY, "/usr/bin/true"])
        self.assertNotIn("-f", cmd)

    def test_symlink_to_even_correct_policy_fails_closed(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root, "real.sb")
            link = Path(root, "link.sb")
            target.write_text(h7b.POLICY)
            link.symlink_to(target)
            with mock.patch.object(h7b, "POLICY_PATH", link):
                self.assertFalse(h7b.policy_matches())

    def test_no_functional_evidence_without_marker(self):
        argv = ["h7b-egress-cert.py", "--", "/usr/bin/true"]
        with (
            mock.patch.object(h7b.sys, "argv", argv),
            mock.patch.object(h7b, "mac_preflight", return_value=""),
            mock.patch.object(h7b, "self_test", return_value={
                "status": "BOUNDARY_PROBE_PASS"}),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            self.assertEqual(h7b.main(), 2)
        self.assertIn("require-marker", out.getvalue())
        self.assertIn("NOT_VERIFIED", out.getvalue())

    def test_env_allowlist_filters_secret_tokens_and_host_path(self):
        with mock.patch.dict(os.environ, {
            "H6D_LAB_PACK": "/test/pack", "H6D_LAB_MODELS": "/test/models",
            "PATH": "/opt/homebrew/bin:/usr/local/bin",
            "GITHUB_TOKEN": "not-for-worker", "HF_TOKEN": "not-for-worker",
            "GEOCAM_SECRET": "not-for-worker", "SSH_AUTH_SOCK": "/host/sock",
            "DYLD_INSERT_LIBRARIES": "/host/inject.dylib",
        }):
            env = h7b.audited_environment()
        self.assertEqual(env["PATH"], "/usr/bin:/bin")
        self.assertEqual(env["H6D_LAB_PACK"], "/test/pack")
        self.assertEqual(env["H6D_LAB_MODELS"], "/test/models")
        for name in ("GITHUB_TOKEN", "HF_TOKEN", "GEOCAM_SECRET",
                     "SSH_AUTH_SOCK", "DYLD_INSERT_LIBRARIES"):
            self.assertNotIn(name, env)

    def test_denied_errno_requires_kernel_permission_failure(self):
        self.assertIn(1, h7b.DENIED_ERRNOS)
        self.assertNotIn(0, h7b.DENIED_ERRNOS)
        # Connection refusal / lack of route must never count as enforcement.
        import errno
        self.assertNotIn(errno.ECONNREFUSED, h7b.DENIED_ERRNOS)
        self.assertNotIn(errno.ENETUNREACH, h7b.DENIED_ERRNOS)


if __name__ == "__main__":
    unittest.main()
