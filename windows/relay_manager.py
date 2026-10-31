"""Install and manage the local, token-authenticated ch-relay for KEYRELAY."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
import zipfile
import base64

from codex_client import private_directory

RELAY_REPO = "https://github.com/lemicoffee2024-collab/ch-relay.git"
RELAY_COMMIT = "aa0b7cf1cbb254f9a692adc44303ee4fdc5df6dd"
BUN_VERSION = "1.4.3"
BUN_SHA256 = "d7a7d3eb216614544aab0cde2d8d9f2e4e87839bf5a30a1cd11d077f02d84e0c"
API = "http://127.0.0.1:8319"


def http_json(url, key=None, data=None, method=None, etag=None):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args):
            return None
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    if etag:
        headers["If-Match"] = etag
    request = urllib.request.Request(url, headers=headers, method=method,
                                     data=None if data is None else json.dumps(data).encode())
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(request, timeout=20) as response:
        return json.load(response), response.headers.get("ETag")


def runtime_env(runtime):
    env = os.environ.copy()
    # Keep the new runtime isolated from global proxy/provider settings.
    for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy",
                 "OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_ACCESS_TOKEN", "CH_DUMP", "CH_ADMIN_OPEN"):
        env.pop(name, None)
    for name in list(env):
        if name.startswith("CH_"):
            env.pop(name)
    env.update({"CH_HOME": str(runtime / "ch-relay"), "CODEX_HOME": str(runtime / "relay-codex-home"),
                "CH_PORT": "0", "CH_SHARE_PORT": "11500", "CH_ADMIN_PORT": "11504",
                "CH_AUTOCUT_CHARS": "off", "CH_TRIM_CHARS": "0", "CH_CLEAN_OUTPUTS": "0",
                "CH_METATRIM": "0", "CH_STRIP_REASONING": "off", "CH_LIGHT_MODEL": "",
                "CH_LIGHT_COLLAB": "0", "CH_SHARE_EFFORT": "", "CH_CACHE_MS": "0",
                "CH_QUOTA_POLL_MS": "0", "CH_INPUT_AUDIT": "0",
                "CH_UPSTREAM_HDR_MS": "180000", "CH_NO_FIRST_BYTE_MS": "180000"})
    return env


class Relay:
    def __init__(self, root):
        self.root = root.resolve()
        self.runtime = self.root / ".keyrelay-runtime"
        self.tools = self.root / ".keyrelay-tools"
        self.source = self.tools / "ch-relay-source"
        self.bun = self.tools / "bun-1.4.3/bun-windows-x64-baseline/bun.exe"
        self.manifest = self.runtime / "relay-install.json"
        self.pid_file = self.runtime / "relay.pid"

    def management_key(self):
        return (self.runtime / "ADMIN-PASSWORD.txt").read_text(encoding="utf-8-sig").strip()

    def management(self, path, **kwargs):
        return http_json(API + "/v8/management/" + path, self.management_key(), **kwargs)

    def health(self):
        result, _ = http_json("http://127.0.0.1:11500/healthz")
        if result.get("ok") is not True:
            raise RuntimeError("Relay health check did not return ok=true")

    def install(self):
        if os.name != "nt":
            raise RuntimeError("These portable launchers target Windows x64")
        if not (self.runtime / "config.yaml").is_file():
            raise RuntimeError("Run windows/SETUP.cmd first to prepare KEYRELAY")
        private_directory(self.runtime)
        private_directory(self.runtime / "ch-relay")
        self.tools.mkdir(exist_ok=True)
        if not self.bun.exists():
            archive = self.tools / "bun-windows-x64-baseline-1.4.3.zip"
            if not archive.exists():
                url = ("https://github.com/oven-sh/bun/releases/download/bun-v1.4.3/"
                       "bun-windows-x64-baseline.zip")
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open(url, timeout=60) as response, archive.open("wb") as output:
                    shutil.copyfileobj(response, output)
            if hashlib.sha256(archive.read_bytes()).hexdigest() != BUN_SHA256:
                raise RuntimeError("Bun archive checksum mismatch")
            target = self.bun.parents[1].resolve()
            with zipfile.ZipFile(archive) as bundle:
                for name in bundle.namelist():
                    if not (target / name).resolve().is_relative_to(target):
                        raise RuntimeError("Unsafe archive member")
                bundle.extractall(target)
        if not self.source.exists():
            subprocess.run(["git", "-c", "http.proxy=", "-c", "http.sslBackend=openssl", "clone",
                            "--no-checkout", RELAY_REPO, str(self.source)], check=True)
            subprocess.run(["git", "-C", str(self.source), "checkout", "--detach", RELAY_COMMIT], check=True)
        revision = subprocess.check_output(["git", "-C", str(self.source), "rev-parse", "HEAD"], text=True).strip()
        if revision != RELAY_COMMIT:
            raise RuntimeError("Existing relay source is not the audited pinned commit; it was left unchanged")
        env = runtime_env(self.runtime)
        env["BUN_INSTALL_CACHE_DIR"] = str(self.tools / "bun-cache")
        env["TEMP"] = env["TMP"] = str(self.tools / "relay-test-temp")
        Path(env["TEMP"]).mkdir(exist_ok=True)
        subprocess.run([str(self.bun), "install", "--frozen-lockfile", "--ignore-scripts", "--production"],
                       cwd=self.source, env=env, check=True)
        with (self.tools / "ch-relay-tests.log").open("w", encoding="utf-8") as output:
            subprocess.run([str(self.bun), "test"], cwd=self.source, env=env, check=True,
                           stdout=output, stderr=subprocess.STDOUT)
        print("Relay test suite passed (see .keyrelay-tools/ch-relay-tests.log).")
        # Admin credentials are read from a private file, never passed on the command line.
        admin_module = (self.source / "src/admin/auth.ts").as_uri()
        env["KEYRELAY_PASSWORD_FILE"] = str(self.runtime / "ADMIN-PASSWORD.txt")
        code = (f"import {{addAdminUser,listAdminUsers}} from {json.dumps(admin_module)};"
                "import {readFileSync} from 'node:fs';"
                "if (!listAdminUsers().some(u=>u.username==='localadmin')) "
                "await addAdminUser('localadmin',readFileSync(process.env.KEYRELAY_PASSWORD_FILE,'utf8').trim());"
                "console.log('Relay admin account ready.');")
        subprocess.run([str(self.bun), "-e", code], cwd=self.source, env=env, check=True)
        self.manifest.write_text(json.dumps({"source": RELAY_REPO, "commit": RELAY_COMMIT,
                                            "bun": BUN_VERSION, "bun_archive_sha256": BUN_SHA256}, indent=2))
        self.start()
        config, etag = self.management("config/plugins/configs/ch-relay-vippro")
        if not isinstance(config, dict) or config.get("base-url") != "http://127.0.0.1:11500/v1":
            raise RuntimeError("Existing plugin endpoint differs from the local relay; config was left unchanged")
        backup = self.runtime / "backups"
        backup.mkdir(exist_ok=True)
        shutil.copy2(self.runtime / "config.yaml", backup / ("config-before-relay-" + time.strftime("%Y%m%d-%H%M%S") + ".yaml"))
        self.management("config/plugins/configs/ch-relay-vippro", data={"enabled": True}, method="PATCH", etag=etag)
        self.ensure_client_key()
        print("Local relay installed and plugin enabled. Plus login is still required.")
        self.status()

    def process_info(self):
        if not self.pid_file.exists():
            return None
        recorded = self.pid_file.read_text().strip()
        if not recorded.isdecimal():
            # An empty or non-numeric pid file is stale; nothing may be looked up or stopped.
            return None
        process_id = int(recorded)
        code = (f"Get-CimInstance Win32_Process -Filter 'ProcessId = {process_id}' | "
                "Select-Object ProcessId,ExecutablePath,CommandLine | ConvertTo-Json -Compress")
        raw = subprocess.check_output(["powershell", "-NoProfile", "-Command", code], text=True).strip()
        if not raw:
            return None
        info = json.loads(raw)
        if (os.path.normcase(info.get("ExecutablePath") or "") != os.path.normcase(str(self.bun))
                or str(self.source / "src/cli.ts") not in (info.get("CommandLine") or "")):
            # The recorded PID now belongs to another process, so the entry is stale.
            return None
        return info

    def start(self):
        if not self.manifest.exists():
            raise RuntimeError("Run windows/SETUP-RELAY.cmd first")
        info = self.process_info()
        if info:
            self.health()
            print("Local relay is already running.")
            return
        if self.pid_file.exists():
            # The recorded PID is stale: never stop that process, just start a new relay.
            self.pid_file.unlink()
        # Refuse occupied ports instead of replacing another application's service.
        import socket
        for port in (11500, 11504):
            with socket.socket() as probe:
                try:
                    probe.bind(("127.0.0.1", port))
                except OSError:
                    raise RuntimeError(f"Port {port} is occupied; no process was stopped") from None
        logs = self.runtime / "logs"
        logs.mkdir(exist_ok=True)
        command = [str(self.bun), str(self.source / "src/cli.ts"), "start", "--port", "0",
                   "--share-port", "11500", "--admin-port", "11504"]
        with (logs / "relay-out.log").open("ab") as out, (logs / "relay-error.log").open("ab") as err:
            child = subprocess.Popen(command, cwd=self.source, env=runtime_env(self.runtime),
                                     stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                     creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
        self.pid_file.write_text(str(child.pid))
        for _ in range(40):
            if child.poll() is not None:
                raise RuntimeError("Relay exited; see .keyrelay-runtime/logs/relay-error.log")
            try:
                self.health()
                print("Local token-authenticated relay is healthy on 127.0.0.1:11500.")
                return
            except (OSError, ValueError, RuntimeError):
                time.sleep(0.25)
        raise RuntimeError("Relay health check failed; see its private log files")

    def stop(self):
        info = self.process_info()
        if info:
            subprocess.run(["powershell", "-NoProfile", "-Command",
                            f"Stop-Process -Id {int(info['ProcessId'])} -ErrorAction Stop"], check=True)
        if self.pid_file.exists():
            self.pid_file.unlink()
        print("Local relay stopped; its data was preserved.")

    def status(self):
        result = {"relay_healthy": False, "plus_ready": False, "status": "NOT READY"}
        try:
            self.health()
            result["relay_healthy"] = True
        except (OSError, ValueError):
            pass
        for path in (self.runtime / "auth").glob("*.json"):
            try:
                auth = json.loads(path.read_text(encoding="utf-8-sig"))
                token = auth.get("access_token", "")
                parts = token.split(".")
                claims = json.loads(base64.urlsafe_b64decode(parts[1] + "=" * (-len(parts[1]) % 4))) if len(parts) == 3 else {}
                plan = claims.get("https://api.openai.com/auth", {}).get("chatgpt_plan_type")
                if (auth.get("type", auth.get("provider")) == "codex" and not auth.get("disabled")
                        and token and path.name.endswith("-plus.json") and plan == "plus"
                        and claims.get("exp", 0) > time.time()):
                    result["plus_ready"] = True
            except (OSError, ValueError):
                pass
        proof = self.runtime / "relay-model-check.json"
        if result["relay_healthy"] and result["plus_ready"]:
            result["status"] = "LOGIN COMPLETE; MODEL CHECK REQUIRED"
            if proof.exists():
                verified = json.loads(proof.read_text())
                if verified.get("ok") and time.time() - verified.get("checked_at", 0) < 3600:
                    result["status"] = "READY"
        (self.runtime / "relay-status.json").write_text(json.dumps(result, indent=2))
        access_path = self.runtime / "ACCESS.txt"
        if access_path.exists():
            import re
            access = access_path.read_text(encoding="utf-8")
            access = re.sub(r"(?m)^status:.*$", "status: " + result["status"], access)
            access = re.sub(r"(?m)^codex_plus_ready:.*$", "codex_plus_ready: " + str(result["plus_ready"]).lower(), access)
            marker = "\n--- Current relay status ---"
            access = access.split(marker, 1)[0].split("\n\n", 1)[0]
            access += "\n\nUse ADMIN-PASSWORD.txt for management and CLIENT-API-KEY.txt for CMD.\n"
            access += "Relay console: http://127.0.0.1:11504 (localadmin; initial management password).\n"
            access += "Run CHECK-RELAY.cmd for a current live model check. Subscription limits still apply.\n"
            access += marker + "\n" + json.dumps(result, indent=2) + "\n"
            access_path.write_text(access, encoding="utf-8")
        print(json.dumps(result, indent=2))
        return result

    def check(self):
        if not self.status()["plus_ready"]:
            raise RuntimeError("Sign in first with LOGIN-PLUS.cmd; a model request was not sent")
        (self.runtime / "relay-model-check.json").write_text(json.dumps({"ok": False}))
        self.ensure_client_key()
        key = (self.runtime / "CLIENT-API-KEY.txt").read_text().strip()
        payload = {"model": "gpt-6-astra", "instructions": "Reply briefly. Do not use tools.",
                   "input": [{"role": "user", "content": "Reply exactly KEYRELAY_OK."}],
                   "reasoning": {"effort": "low"}, "stream": True}
        request = urllib.request.Request(API + "/v1/responses", data=json.dumps(payload).encode(),
                                         headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        completed = False
        text = ""
        with opener.open(request, timeout=180) as response:
            for line in response:
                if not line.startswith(b"data:"):
                    continue
                try:
                    event = json.loads(line[5:])
                except ValueError:
                    continue
                if event.get("type") == "response.output_text.delta":
                    text += event.get("delta", "")
                if event.get("type") == "response.completed":
                    completed = True
                if event.get("type") in ("response.failed", "error"):
                    raise RuntimeError("The model reported a streaming error; see the private relay logs")
        if not completed or "KEYRELAY_OK" not in text:
            raise RuntimeError("The model check did not complete with the expected reply")
        (self.runtime / "relay-model-check.json").write_text(json.dumps({
            "ok": True, "checked_at": time.time(), "model": "gpt-6-astra"}, indent=2))
        print("PASS: gpt-6-astra completed a real streamed response through KEYRELAY.")
        self.status()

    def ensure_client_key(self):
        path = self.runtime / "CLIENT-API-KEY.txt"
        if path.exists():
            print("Existing CMD client key preserved.")
            return
        # Use the local management API, never edit the live SQLite database.
        url = API + "/v0/management/plugins/key-shop/keys"
        result, _ = http_json(url, self.management_key(), data={
            "label": "Codex CMD local", "customer": "Local owner", "ttl_seconds": 604800,
            "unlimited_tokens": True, "unlimited_requests": True})
        secret = result.get("secret", "")
        if not secret.startswith("ck_"):
            raise RuntimeError("Server did not issue a client key")
        path.write_text(secret + "\n", encoding="utf-8")
        print("New 7-day CMD key saved privately to CLIENT-API-KEY.txt.")

    def login(self, wait=True):
        result, _ = self.management("oauth/auth-url?provider=codex&is_webui=true")
        url = result.get("url", "")
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "https" or parsed.hostname != "auth.openai.com":
            raise RuntimeError("Unexpected login URL; browser was not opened")
        pending = {"state": result["state"], "url": url, "created_at": time.time()}
        (self.runtime / "plus-login-pending.json").write_text(json.dumps(pending))
        (self.runtime / "PLUS-LOGIN-URL.txt").write_text(url + "\n", encoding="utf-8")
        print("Sign in with your Plus account in the browser. No API key or API balance is required here.")
        print("If the browser did not open, use the URL in .keyrelay-runtime/PLUS-LOGIN-URL.txt.")
        webbrowser.open(url)
        if wait:
            self.finish_login()

    def finish_login(self):
        pending = json.loads((self.runtime / "plus-login-pending.json").read_text())
        deadline = pending["created_at"] + 300
        while time.time() < deadline:
            result, _ = self.management("oauth/status?" + urllib.parse.urlencode({"state": pending["state"]}))
            if result.get("status") == "ok":
                print("Browser login completed.")
                self.check()
                return
            if result.get("status") == "error":
                raise RuntimeError("Login failed: " + str(result.get("error", "see panel Auth Files")))
            time.sleep(2)
        raise RuntimeError("Login expired. Run LOGIN-PLUS.cmd to try again")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "start", "stop", "status", "check", "login", "login-start", "login-finish"))
    parser.add_argument("--workspace-root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    relay = Relay(args.workspace_root)
    if args.action == "login-start":
        relay.login(wait=False)
    elif args.action == "login-finish":
        relay.finish_login()
    else:
        getattr(relay, args.action)()


if __name__ == "__main__":
    try:
        main()
    except (KeyboardInterrupt, EOFError):
        sys.exit(130)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
