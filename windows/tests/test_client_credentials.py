"""Credential prompt regressions; these checks never call a model provider."""

from contextlib import redirect_stdout
import importlib.util
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

source = Path(__file__).resolve().parents[1] / "codex_client.py"
spec = importlib.util.spec_from_file_location("client", source)
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)


class CredentialTests(unittest.TestCase):
    def test_local_file_is_bound_to_this_server(self):
        with tempfile.TemporaryDirectory() as folder:
            runtime = Path(folder)
            (runtime / "CLIENT-API-KEY.txt").write_text("\ufeffck_fixture_local\n", encoding="utf-8")
            self.assertEqual(client.load_local_client_key(runtime, "http://127.0.0.1:8319/v1"),
                             "ck_fixture_local")
            for endpoint in ("https://example.com/v1", "http://127.0.0.1:8320/v1",
                             "http://127.0.0.1:8319/other", "http://127.0.0.1.example.com:8319/v1"):
                with self.subTest(endpoint=endpoint):
                    self.assertIsNone(client.load_local_client_key(runtime, endpoint))

    def test_invalid_local_file_is_not_used(self):
        with tempfile.TemporaryDirectory() as folder, redirect_stdout(io.StringIO()):
            runtime = Path(folder)
            for value in ("KR_fixture_management", "ck_first\nck_second", ""):
                (runtime / "CLIENT-API-KEY.txt").write_text(value)
                self.assertIsNone(client.load_local_client_key(runtime, "http://127.0.0.1:8319/v1"))

    def test_enter_uses_local_key_without_printing_it(self):
        output = io.StringIO()
        with patch.object(client.getpass, "getpass", return_value=""), redirect_stdout(output):
            self.assertEqual(client.prompt_client_key("ck_fixture_local"), "ck_fixture_local")
        self.assertNotIn("ck_fixture_local", output.getvalue())

    def test_empty_or_invalid_input_retries(self):
        with patch.object(client.getpass, "getpass", side_effect=["", "KR-management", "ck_a\nck_b", " ck_valid "]) as ask, \
                redirect_stdout(io.StringIO()):
            self.assertEqual(client.prompt_client_key(), "ck_valid")
            self.assertEqual(ask.call_count, 4)

    def test_manual_key_overrides_file(self):
        with patch.object(client.getpass, "getpass", return_value="ck_manual"), redirect_stdout(io.StringIO()):
            self.assertEqual(client.prompt_client_key("ck_fixture_local"), "ck_manual")

    def test_rejected_file_key_requires_new_input_before_launch(self):
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as folder:
            runtime = Path(folder)
            (runtime / "CLIENT-API-KEY.txt").write_text("ck_old")
            responses = [urllib.error.HTTPError("http://127.0.0.1:8319/v1/models", 401, "Unauthorized", {}, None),
                         {"data": [{"id": "fixture-model"}]}, {}]
            with patch.object(sys, "argv", [str(source), "--runtime-root", folder]), \
                    patch.object(client, "private_directory"), \
                    patch.object(client, "find_codex", return_value="fixture-codex"), \
                    patch("builtins.input", side_effect=["http://127.0.0.1:8319/v1", "1", folder]), \
                    patch.object(client.getpass, "getpass", side_effect=["", "", "ck_new"]), \
                    patch.object(client, "get_json", side_effect=responses) as requests, \
                    patch.object(client.subprocess, "check_output", return_value="codex-cli 0.163.0"), \
                    patch.object(client.subprocess, "call", return_value=0) as launch, redirect_stdout(output):
                self.assertEqual(client.main(), 0)
            self.assertEqual([call.args[1] for call in requests.call_args_list], ["ck_old", "ck_new", "ck_new"])
            self.assertEqual(launch.call_args.kwargs["env"]["KEYRELAY_API_KEY"], "ck_new")
            self.assertNotIn("ck_old", output.getvalue())
            self.assertNotIn("ck_new", output.getvalue())
            self.assertNotIn("ck_new", (runtime / "client-preferences.json").read_text())


if __name__ == "__main__":
    unittest.main()
