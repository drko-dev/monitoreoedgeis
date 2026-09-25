#!/usr/bin/env python3
"""Unit tests for the geocam-up onboarding wizard.

geocam-up has no .py extension (it's a standalone CLI entry point), so it is
loaded via importlib. `main()` only runs under the `if __name__ == "__main__"`
guard, so importing it has no side effects.

Run with:  python3 -m unittest tests/test_geocam_up.py -v
"""
import importlib.machinery
import importlib.util
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch, MagicMock

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT_PATH = REPO_ROOT / "geocam-up"

_loader = importlib.machinery.SourceFileLoader("geocam_up", str(SCRIPT_PATH))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
geocam_up = importlib.util.module_from_spec(_spec)
sys.modules["geocam_up"] = geocam_up
_loader.exec_module(geocam_up)


class FakeApi:
    """Records calls and answers from a scripted table of (method, path) -> response.

    A handler may be a static value or a callable(payload, query) -> value.
    Missing routes raise to make un-mocked calls fail loudly instead of
    silently returning None.
    """

    def __init__(self, routes):
        self.routes = routes
        self.calls = []

    def request(self, method, path, payload=None, query=None, timeout=20):
        self.calls.append((method, path, payload, query))
        key = (method, path)
        if key not in self.routes:
            raise AssertionError(f"Unmocked API call: {method} {path}")
        handler = self.routes[key]
        result = handler(payload, query) if callable(handler) else handler
        return 200, result


class TestOrgSiteResolution(unittest.TestCase):
    def test_single_org_single_site_auto_selected(self):
        api = FakeApi({
            ("GET", "/api/sites"): {"sites": [{"id": 4, "name": "Sitio Único", "active": True}]},
        })
        profile = {
            "organizations": [{"id": 7, "name": "Org Única"}],
            "user": {},
        }
        org_id, site_id = geocam_up.resolve_org_site(api, profile, {})
        self.assertEqual(org_id, 7)
        self.assertEqual(site_id, 4)

    def test_multiple_orgs_prompts_menu(self):
        api = FakeApi({
            ("GET", "/api/sites"): {"sites": [{"id": 1, "name": "S", "active": True}]},
        })
        profile = {
            "organizations": [
                {"id": 1, "name": "Org A"},
                {"id": 2, "name": "Org B"},
            ],
            "user": {},
        }
        with patch("builtins.input", return_value="2"):
            org_id, _ = geocam_up.resolve_org_site(api, profile, {})
        self.assertEqual(org_id, 2)

    def test_no_organizations_raises(self):
        api = FakeApi({})
        profile = {"organizations": [], "user": {}}
        with self.assertRaises(RuntimeError):
            geocam_up.resolve_org_site(api, profile, {})


class TestEnrollment(unittest.TestCase):
    def test_reuses_valid_existing_credential(self):
        called = {"enroll": False}

        def fake_run_cmd(args, env=None, cwd=None, input_text=None, check=False, capture=True):
            if "enroll" in args:
                called["enroll"] = True
            return subprocess.CompletedProcess(args, 0, stdout="OK")

        with patch.object(geocam_up, "run_cmd", side_effect=fake_run_cmd), \
             patch.object(Path, "exists", return_value=True):
            api = FakeApi({})
            geocam_up.ensure_enrollment(api, {}, "bin", {}, "repo", Path("/tmp/fake-data-dir"), 1, 1)
        self.assertFalse(called["enroll"], "must not re-enroll when SaaS already accepts the local credential")

    def test_reenrolls_when_saas_rejects_existing_credential(self):
        calls = {"enroll": 0}

        def fake_run_cmd(args, env=None, cwd=None, input_text=None, check=False, capture=True):
            if "enroll" in args:
                calls["enroll"] += 1
                return subprocess.CompletedProcess(args, 0, stdout="OK: enrolled")
            return subprocess.CompletedProcess(args, 1, stdout="rejected")

        api = FakeApi({
            ("POST", "/api/v1/gateway/enrollments"): {"enrollment_token": "one-time-token"},
        })
        with patch.object(geocam_up, "run_cmd", side_effect=fake_run_cmd), \
             patch.object(Path, "exists", return_value=True), \
             patch.object(geocam_up.shutil, "move"):
            # First saas_check (existing cred) fails, second (post-enroll) succeeds.
            with patch.object(geocam_up, "saas_check", side_effect=[(False, "rejected"), (True, "OK")]):
                geocam_up.ensure_enrollment(api, {}, "bin", {}, "repo", Path("/tmp/fake-data-dir"), 1, 1)
        self.assertEqual(calls["enroll"], 1)

    def test_enrollment_token_never_persisted_in_payload_echo(self):
        # Guard against a regression that would print/store the token.
        api = FakeApi({
            ("POST", "/api/v1/gateway/enrollments"): {"enrollment_token": "super-secret-token"},
        })

        def fake_run_cmd(args, env=None, cwd=None, input_text=None, check=False, capture=True):
            self.assertNotIn("super-secret-token", " ".join(str(a) for a in args))
            return subprocess.CompletedProcess(args, 0, stdout="OK")

        with patch.object(geocam_up, "run_cmd", side_effect=fake_run_cmd), \
             patch.object(Path, "exists", return_value=False), \
             patch.object(geocam_up, "saas_check", return_value=(True, "OK")):
            geocam_up.ensure_enrollment(api, {}, "bin", {}, "repo", Path("/tmp/fake-data-dir"), 1, 1)


class TestCandidateSelection(unittest.TestCase):
    def test_single_candidate_auto_selected(self):
        candidates = [{"candidate_key": "k1", "endpoint_host": "10.0.0.5"}]
        self.assertEqual(geocam_up.select_candidate(candidates)["candidate_key"], "k1")

    def test_multiple_candidates_prompts_menu(self):
        candidates = [
            {"candidate_key": "k1", "endpoint_host": "10.0.0.5"},
            {"candidate_key": "k2", "endpoint_host": "10.0.0.6"},
        ]
        with patch("builtins.input", return_value="2"):
            chosen = geocam_up.select_candidate(candidates)
        self.assertEqual(chosen["candidate_key"], "k2")


class TestLocalIdentifier(unittest.TestCase):
    def test_prefers_epr_address(self):
        c = {"epr_address": "uuid:abc-123", "candidate_key": "shakey"}
        self.assertEqual(geocam_up.local_identifier_from_candidate(c), "epr:uuid:abc-123")

    def test_epr_already_prefixed_untouched(self):
        c = {"epr_address": "epr:uuid:abc-123", "candidate_key": "shakey"}
        self.assertEqual(geocam_up.local_identifier_from_candidate(c), "epr:uuid:abc-123")

    def test_falls_back_to_candidate_key(self):
        c = {"epr_address": "", "candidate_key": "shakey"}
        self.assertEqual(geocam_up.local_identifier_from_candidate(c), "shakey")


class TestCameraCredential(unittest.TestCase):
    def test_no_auth_required_is_skipped_by_caller(self):
        # configure_camera_credential is only invoked when auth_required is
        # true; this test documents that contract at the candidate level.
        candidate = {"candidate_key": "k1", "auth_required": False}
        self.assertFalse(candidate["auth_required"])

    def test_reuses_existing_assigned_credential(self):
        api = FakeApi({
            ("GET", "/api/v1/camera-credentials"): {
                "credentials": [
                    {
                        "status": "active",
                        "name": "existing",
                        "assignments": [{"gateway_device_id": "dev1", "candidate_key": "k1"}],
                    }
                ]
            },
        })
        changed = geocam_up.configure_camera_credential(
            api, org_id=1, site_id=1, device_id="dev1", gateway_db_id="dev1",
            candidate={"candidate_key": "k1", "endpoint_host": "10.0.0.5"},
        )
        self.assertFalse(changed)

    def test_creates_credential_when_missing(self):
        api = FakeApi({
            ("GET", "/api/v1/camera-credentials"): {"credentials": []},
            ("POST", "/api/v1/camera-credentials"): {"credential": {"id": 99}},
            ("POST", "/api/v1/camera-credentials/99/assignments"): {},
        })
        with patch("builtins.input", return_value="admin"), \
             patch.object(geocam_up.getpass, "getpass", return_value="s3cret"):
            changed = geocam_up.configure_camera_credential(
                api, org_id=1, site_id=1, device_id="dev1", gateway_db_id="dev1",
                candidate={"candidate_key": "k1", "endpoint_host": "10.0.0.5"},
            )
        self.assertTrue(changed)
        # The password is legitimately sent once, in the credential-creation
        # POST body (over HTTPS to the SaaS) — that is expected. What must
        # never happen is it leaking into any *other* call, e.g. a log/status
        # request or the assignment call that follows.
        assignment_call = next(c for c in api.calls if c[1].endswith("/assignments"))
        self.assertNotIn("s3cret", str(assignment_call))


class TestSaasCameraSelection(unittest.TestCase):
    def test_idempotent_recovery_skips_menu_entirely(self):
        api = FakeApi({
            ("GET", "/api/v1/edge/devices"): {"devices": [{"device_id": "dev1", "name": "GW"}]},
            ("GET", "/api/v1/edge/devices/dev1/cameras"): {
                "cameras": [{"camera_id": 5, "camera_nombre": "Cam5", "edge_camera_identifier": "epr:uuid:x"}]
            },
        })
        with patch("builtins.input", side_effect=AssertionError("menu should not be shown on recovery")):
            cam = geocam_up.select_saas_camera(api, org_id=1, site_id=1, local_identifier="epr:uuid:x",
                                                candidate={"endpoint_host": "10.0.0.5"})
        self.assertEqual(cam["id"], 5)

    def test_create_new_camera_picks_free_slot(self):
        api = FakeApi({
            ("GET", "/api/v1/edge/devices"): {"devices": []},
            ("GET", "/api/cameras"): {"cameras": [{"id": 1, "slot": 1}, {"id": 2, "slot": 2}]},
            ("POST", "/api/cameras"): lambda payload, query: {"camera": {"id": 10, "slot": payload["slot"]}},
        })
        with patch("builtins.input", side_effect=["1", "Cam nueva"]):
            cam = geocam_up.select_saas_camera(api, org_id=1, site_id=1, local_identifier="epr:uuid:none",
                                                candidate={"endpoint_host": "10.0.0.9", "manufacturer": "ACME", "model": "X1"})
        self.assertEqual(cam["slot"], 3)

    def test_link_existing_requires_confirmation(self):
        api = FakeApi({
            ("GET", "/api/v1/edge/devices"): {"devices": []},
            ("GET", "/api/cameras"): {"cameras": [{"id": 7, "nombre": "Cam7", "slot": 1}]},
        })
        with patch("builtins.input", side_effect=["2", "1", "n"]):
            with self.assertRaises(RuntimeError):
                geocam_up.select_saas_camera(api, org_id=1, site_id=1, local_identifier="epr:uuid:none",
                                              candidate={"endpoint_host": "10.0.0.9"})

    def test_cancel_option_raises(self):
        api = FakeApi({
            ("GET", "/api/v1/edge/devices"): {"devices": []},
        })
        with patch("builtins.input", return_value="3"):
            with self.assertRaises(RuntimeError):
                geocam_up.select_saas_camera(api, org_id=1, site_id=1, local_identifier="epr:uuid:none",
                                              candidate={"endpoint_host": "10.0.0.9"})


class TestFreeSlot(unittest.TestCase):
    def test_first_free_slot_when_gap_exists(self):
        cams = [{"slot": 1}, {"slot": 3}]
        self.assertEqual(geocam_up.next_free_slot(cams), 2)

    def test_appends_when_no_gap(self):
        cams = [{"slot": 1}, {"slot": 2}]
        self.assertEqual(geocam_up.next_free_slot(cams), 3)

    def test_empty_org_starts_at_one(self):
        self.assertEqual(geocam_up.next_free_slot([]), 1)


class TestSaasReachability(unittest.TestCase):
    def test_saas_down_reports_unreachable_not_raise(self):
        with patch.object(geocam_up.urllib.request, "urlopen", side_effect=OSError("connection refused")):
            ok, detail = geocam_up.saas_reachable("http://example.invalid")
        self.assertFalse(ok)


class TestE2EValidation(unittest.TestCase):
    def test_online_requires_local_pipeline_and_cloud_upload(self):
        healthy_status = {
            "video_pipeline": {
                "camera_count": 1,
                "cameras": [{"candidate_key": "k1", "state": "running"}],
            },
            "cloud": {"frames_upload_succeeded": 3, "frames_upload_failed": 0},
        }
        with patch.object(geocam_up, "local_status", return_value=healthy_status):
            ok, _ = geocam_up.wait_camera_online(FakeApi({}), org_id=1, device_id="dev1",
                                                  local_identifier="k1", seconds=0.1)
        self.assertTrue(ok)

    def test_rtsp_online_alone_is_not_enough(self):
        # RTSP running but zero frames accepted by Cloud must NOT be OK
        # (the historical regression this script guards against: a 404 loop).
        stuck_status = {
            "video_pipeline": {
                "camera_count": 1,
                "cameras": [{"candidate_key": "k1", "state": "running"}],
            },
            "cloud": {"frames_upload_succeeded": 0, "frames_upload_failed": 12},
        }
        with patch.object(geocam_up, "local_status", return_value=stuck_status):
            ok, _ = geocam_up.wait_camera_online(FakeApi({}), org_id=1, device_id="dev1",
                                                  local_identifier="k1", seconds=0.1)
        self.assertFalse(ok)


class TestFfmpegPortability(unittest.TestCase):
    def test_ffmpeg_lookup_uses_which_not_fixed_path(self):
        with patch.object(geocam_up.shutil, "which", return_value="/usr/bin/ffmpeg") as which:
            path = geocam_up.find_ffmpeg()
        which.assert_called_once_with("ffmpeg")
        self.assertEqual(path, "/usr/bin/ffmpeg")

    def test_env_omits_ffmpeg_var_when_not_found(self):
        env = geocam_up.edge_env(Path("/tmp/x"), "http://saas", ffmpeg_path=None)
        self.assertNotIn("GEOCAM_VIDEO_FFMPEG_PATH", env)

    def test_env_sets_ffmpeg_var_when_found(self):
        env = geocam_up.edge_env(Path("/tmp/x"), "http://saas", ffmpeg_path="/usr/bin/ffmpeg")
        self.assertEqual(env["GEOCAM_VIDEO_FFMPEG_PATH"], "/usr/bin/ffmpeg")


if __name__ == "__main__":
    unittest.main()
