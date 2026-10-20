"""Interactive Codex CLI client for an existing KEYRELAY endpoint."""

import argparse
import getpass
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


def endpoint_url(value):
    value = value.strip().rstrip("/")
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("Use a complete URL, for example http://127.0.0.1:8319/v1")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("The endpoint must not contain credentials, query parameters or a fragment")
    if parsed.hostname.lower() in ("api.openai.com", "chatgpt.com"):
        raise ValueError("Enter your KEYRELAY endpoint")
    for suffix in ("/responses", "/chat/completions", "/models"):
        if value.endswith(suffix):
            value = value[:-len(suffix)]
            break
    return value if urllib.parse.urlsplit(value).path else value + "/v1"


def get_json(url, key):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(url, headers={"Authorization": "Bearer " + key})
    with opener.open(request, timeout=15) as response:
        return json.load(response)


def find_codex():
    shim = shutil.which("codex.cmd") or shutil.which("codex")
    if shim:
        parent = Path(shim).parent
        for package in (parent / "node_modules/@openai/codex", parent.parent / "@openai/codex"):
            matches = list(package.glob("vendor/*/bin/codex.exe"))
            matches += list(package.glob("node_modules/@openai/codex-win32-*/vendor/*/bin/codex.exe"))
            matches += list(package.parent.glob("codex-win32-*/vendor/*/bin/codex.exe"))
            if matches:
                return str(matches[0])
    executable = shutil.which("codex.exe")
    if executable:
        return executable
    raise RuntimeError("Install Codex CLI first: npm install -g @openai/codex@0.163.0")


def private_directory(path):
    path.mkdir(parents=True, exist_ok=True)
    if os.name == "nt":
        import csv
        identity = subprocess.check_output(["whoami", "/user", "/fo", "csv", "/nh"], text=True)
        sid = next(csv.reader(identity.strip().splitlines()))[1]
        subprocess.run(["icacls", str(path), "/inheritance:r", "/grant:r", f"*{sid}:(OI)(CI)F"],
                       check=True, stdout=subprocess.DEVNULL)
    else:
        path.chmod(0o700)


def client_command(executable, endpoint, model, project):
    settings = {
        "model_provider": "keyrelay",
        "model_providers.keyrelay.name": "KEYRELAY",
        "model_providers.keyrelay.base_url": endpoint,
        "model_providers.keyrelay.env_key": "KEYRELAY_API_KEY",
        "model_providers.keyrelay.wire_api": "responses",
        "model_providers.keyrelay.requires_openai_auth": False,
        "model_providers.keyrelay.supports_websockets": False,
    }
    command = [executable, "--model", model, "--cd", str(project)]
    for name, value in settings.items():
        command += ["-c", name + "=" + json.dumps(value)]
    return command


def prompt(label, default=""):
    return input(f"{label}" + (f" [{default}]" if default else "") + ": ").strip() or default


def load_local_client_key(runtime, endpoint):
    # This file belongs to this installation, never to an arbitrary endpoint.
    parsed = urllib.parse.urlsplit(endpoint)
    if (parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost", "::1")
            or parsed.port != 8319 or parsed.path != "/v1"):
        return None
    try:
        key = (runtime / "CLIENT-API-KEY.txt").read_text(encoding="utf-8-sig").strip()
    except FileNotFoundError:
        return None
    if not key.startswith("ck_") or any(char.isspace() for char in key):
        print("CLIENT-API-KEY.txt does not contain one client key; enter a key manually.")
        return None
    return key


def prompt_client_key(local_key=None):
    if local_key:
        print("Press Enter to use CLIENT-API-KEY.txt for this local server, or paste another key.")
    while True:
        key = getpass.getpass("Client API key ck_... (hidden; characters do not appear): ").strip()
        if not key and local_key:
            print("Using the local client key from CLIENT-API-KEY.txt.")
            return local_key
        if not key:
            print("No key entered. Paste the client key and press Enter (Ctrl+C to cancel).")
        elif key.startswith("KR-"):
            print("This is a management password. Enter the client ck_ key instead.")
        elif any(char.isspace() for char in key):
            print("The key must be one line without spaces. Please paste it again.")
        else:
            return key


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-root", type=Path,
                        default=Path(__file__).resolve().parents[1] / ".keyrelay-runtime")
    args = parser.parse_args()
    runtime = args.runtime_root.resolve()
    private_directory(runtime)
    executable = find_codex()
    preferences = runtime / "client-preferences.json"
    try:
        saved = json.loads(preferences.read_text(encoding="utf-8"))
    except (FileNotFoundError, ValueError):
        saved = {}
    print("Codex CLI -> your KEYRELAY endpoint\n")
    endpoint = endpoint_url(prompt("Endpoint", saved.get("endpoint", "http://127.0.0.1:8319/v1")))
    local_key = load_local_client_key(runtime, endpoint)
    models = []
    while True:
        key = prompt_client_key(local_key)
        try:
            catalog = get_json(endpoint + "/models", key)
            models = sorted({item["id"] for item in catalog.get("data", [])
                             if isinstance(item, dict) and isinstance(item.get("id"), str)})
        except urllib.error.HTTPError as error:
            if error.code in (401, 403):
                print(f"Key rejected: HTTP {error.code}. Rotate invalidates the previous key; enter the newest key.")
                local_key = None
                continue
            print(f"Model listing returned HTTP {error.code}; enter a model manually")
        except (urllib.error.URLError, TimeoutError) as error:
            raise RuntimeError("The endpoint is unavailable. Start the server on this computer first") from error
        break
    for number, name in enumerate(models, 1):
        print(f"  {number}. {name}")
    if not models:
        print("No active models were advertised. A client key does not configure the model backend.")
    selected = prompt("Model number or exact name", saved.get("model", "gpt-6.1-sol"))
    if selected.isdecimal() and 1 <= int(selected) <= len(models):
        selected = models[int(selected) - 1]
    project = Path(prompt("Project directory", saved.get("project", str(Path.cwd())))).expanduser().resolve()
    if not project.is_dir():
        raise ValueError("The project directory does not exist")
    preferences.write_text(json.dumps({"endpoint": endpoint, "model": selected, "project": str(project)},
                                      indent=2), encoding="utf-8")
    codex_home = runtime / "codex-home"
    codex_home.mkdir(exist_ok=True)
    env = os.environ.copy()
    env["CODEX_HOME"] = str(codex_home)
    env["KEYRELAY_API_KEY"] = key
    for name in ("OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"):
        env.pop(name, None)
    command = client_command(executable, endpoint, selected, project)
    try:
        version = subprocess.check_output([executable, "--version"], text=True).strip().split()[-1]
        catalog = get_json(endpoint + "/models?" + urllib.parse.urlencode({"client_version": version}), key)
        if isinstance(catalog.get("models"), list) and catalog["models"]:
            for entry in catalog["models"]:
                entry["use_responses_lite"] = False
                entry["multi_agent_version"] = "v1"
            catalog_path = runtime / "client-models.json"
            catalog_path.write_text(json.dumps(catalog), encoding="utf-8")
            command += ["-c", "model_catalog_json=" + json.dumps(str(catalog_path))]
    except (urllib.error.URLError, TimeoutError, ValueError):
        pass
    print("\nStarting Codex. Model responses require a configured and healthy model backend.\n")
    return subprocess.call(command, env=env)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (KeyboardInterrupt, EOFError):
        sys.exit(130)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
