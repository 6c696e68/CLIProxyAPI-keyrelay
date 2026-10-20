"""Relay ownership, credential isolation and readiness regressions."""

from contextlib import redirect_stdout
import base64
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import relay_manager as manager


class RelayTests(unittest.TestCase):
    def test_child_environment_does_not_inherit_debug_or_rerouting_settings(self):
        with patch.dict(os.environ, {"CH_DUMP": "private-prompts", "CH_ADMIN_OPEN": "1",
                                     "CH_LIGHT_MODEL": "other-model", "HTTPS_PROXY": "bad-proxy",
                                     "OPENAI_API_KEY": "unused-fixture", "CH_TLS_PIN": "0"}):
            env = manager.runtime_env(Path("fixture-runtime"))
        for name in ("CH_DUMP", "CH_ADMIN_OPEN", "HTTPS_PROXY", "OPENAI_API_KEY", "CH_TLS_PIN"):
            self.assertNotIn(name, env)
        self.assertEqual(env["CH_LIGHT_MODEL"], "")
        self.assertEqual(env["CH_TRIM_CHARS"], "0")
        self.assertEqual(env["CH_STRIP_REASONING"], "off")
        self.assertEqual(env["CH_PORT"], "0")

    def test_stop_rejects_a_pid_reused_by_another_process(self):
        with tempfile.TemporaryDirectory() as folder:
            relay = manager.Relay(Path(folder))
            relay.runtime.mkdir()
            relay.pid_file.write_text("1234")
            raw = json.dumps({"ProcessId": 1234, "ExecutablePath": "unrelated.exe", "CommandLine": ""})
            with patch.object(manager.subprocess, "check_output", return_value=raw), \
                    patch.object(manager.subprocess, "run") as stop:
                with self.assertRaisesRegex(RuntimeError, "another process"):
                    relay.stop()
                stop.assert_not_called()

    def test_new_install_preserves_an_existing_client_key(self):
        with tempfile.TemporaryDirectory() as folder, redirect_stdout(io.StringIO()):
            relay = manager.Relay(Path(folder))
            relay.runtime.mkdir()
            path = relay.runtime / "CLIENT-API-KEY.txt"
            path.write_text("ck_fixture_existing")
            with patch.object(manager, "http_json") as api:
                relay.ensure_client_key()
                api.assert_not_called()
            self.assertEqual(path.read_text(), "ck_fixture_existing")

    def test_readiness_checks_token_plan_and_expiry_not_only_filename(self):
        with tempfile.TemporaryDirectory() as folder, redirect_stdout(io.StringIO()):
            relay = manager.Relay(Path(folder))
            auth_dir = relay.runtime / "auth"
            auth_dir.mkdir(parents=True)
            auth_path = auth_dir / "codex-fixture-plus.json"
            for plan, expiry, expected in (("free", time.time() + 600, False),
                                           ("plus", time.time() - 600, False),
                                           ("plus", time.time() + 600, True)):
                payload = {"https://api.openai.com/auth": {"chatgpt_plan_type": plan}, "exp": expiry}
                encoded = base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip("=")
                auth_path.write_text(json.dumps({"type": "codex", "access_token": "header." + encoded + ".signature"}))
                with patch.object(relay, "health"):
                    result = relay.status()
                self.assertEqual(result["plus_ready"], expected)
                self.assertNotEqual(result["status"], "READY")

    def test_live_check_requires_login_before_a_model_request(self):
        with redirect_stdout(io.StringIO()):
            relay = manager.Relay(Path("fixture"))
            with patch.object(relay, "status", return_value={"plus_ready": False}), \
                    patch.object(manager.urllib.request, "Request") as request:
                with self.assertRaisesRegex(RuntimeError, "Sign in first"):
                    relay.check()
                request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
