"""Offline integration check with the installed Codex CLI and a loopback fixture."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

source = Path(__file__).resolve().parents[1] / "codex_client.py"
spec = importlib.util.spec_from_file_location("client", source)
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
model = "gpt-6.1-sol"
seen = []


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        assert self.headers.get("Authorization") == "Bearer fixture-client-key"
        assert self.path.startswith("/v1/models")
        body = json.dumps({"object": "list", "data": [{"id": model}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        assert self.path == "/v1/responses"
        assert self.headers.get("Authorization") == "Bearer fixture-client-key"
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert body["model"] == model
        seen.append(self.path)
        item = {"id": "msg_fixture", "type": "message", "status": "completed", "role": "assistant",
                "content": [{"type": "output_text", "text": "CLIENT_ROUTE_OK", "annotations": []}]}
        response = {"id": "resp_fixture", "object": "response", "created_at": 1, "status": "completed",
                    "model": model, "output": [item],
                    "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
        events = [
            {"type": "response.created", "response": {**response, "status": "in_progress", "output": []}},
            {"type": "response.output_item.added", "output_index": 0, "item": {**item, "content": []}},
            {"type": "response.output_text.delta", "item_id": item["id"], "output_index": 0,
             "content_index": 0, "delta": "CLIENT_ROUTE_OK"},
            {"type": "response.output_item.done", "output_index": 0, "item": item},
            {"type": "response.completed", "response": response},
        ]
        data = "".join(f"event: {event['type']}\ndata: {json.dumps({**event, 'sequence_number': i})}\n\n"
                       for i, event in enumerate(events)).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def run_cli(command, env):
    assert env["KEYRELAY_API_KEY"] == "fixture-client-key"
    assert "fixture-client-key" not in " ".join(command)
    assert "OPENAI_API_KEY" not in env
    result = subprocess.run(command + ["exec", "--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only",
                                       "Reply with CLIENT_ROUTE_OK. Do not use tools."],
                            env=env, text=True, encoding="utf-8", capture_output=True, timeout=45)
    if result.returncode:
        raise AssertionError(result.stderr)
    assert "CLIENT_ROUTE_OK" in result.stdout, result.stdout
    return result.returncode


server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
threading.Thread(target=server.serve_forever, daemon=True).start()
try:
    with tempfile.TemporaryDirectory(prefix="keyrelay-client-check-") as folder:
        answers = iter([f"http://127.0.0.1:{server.server_port}/v1", "1", str(source.parents[1])])
        with patch.object(sys, "argv", [str(source), "--runtime-root", folder]), \
                patch("builtins.input", lambda _: next(answers)), \
                patch.object(client.getpass, "getpass", return_value="fixture-client-key"), \
                patch.object(client.subprocess, "call", side_effect=run_cli):
            assert client.main() == 0
        preferences = (Path(folder) / "client-preferences.json").read_text()
        assert "fixture-client-key" not in preferences
    assert seen == ["/v1/responses"], seen
    print("PASS: real Codex used only the selected fixture endpoint, with its key in the environment.")
finally:
    server.shutdown()
    server.server_close()
