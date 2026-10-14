#!/usr/bin/env python3
"""Synthetic H7B policy validation, safe on all supported CI platforms."""
import importlib.util
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
            "HTTPS_PROXY": "http://host:8080",
            "GOPROXY": "https://proxy.invalid",
        }):
            env = h7b.audited_environment()
        for key in ("DYLD_INSERT_LIBRARIES", "PYTHONPATH", "HTTP_PROXY", "HTTPS_PROXY"):
            self.assertNotIn(key, env)
        self.assertEqual(env["GOPROXY"], "off")
        self.assertEqual(env["PIP_NO_INDEX"], "1")
        self.assertEqual(env["HF_HUB_OFFLINE"], "1")

    def test_denied_errno_requires_kernel_permission_failure(self):
        self.assertIn(1, h7b.DENIED_ERRNOS)
        self.assertNotIn(0, h7b.DENIED_ERRNOS)
        # Connection refusal / lack of route must never count as enforcement.
        import errno
        self.assertNotIn(errno.ECONNREFUSED, h7b.DENIED_ERRNOS)
        self.assertNotIn(errno.ENETUNREACH, h7b.DENIED_ERRNOS)


if __name__ == "__main__":
    unittest.main()
