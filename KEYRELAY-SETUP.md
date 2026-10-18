# KEYRELAY setup

Runbook for a user or an AI agent to install one CLIProxyAPI process with the `ch-relay-vippro` and `key-shop` plugins.

This file is the procedure. It does not contain production passwords, operator keys, or management secrets. After a successful install, write those to `<install-root>/ACCESS.txt` with mode `0600` and print the same block. Do not commit that file.

The reference install that produced this file is already running on the authoring Mac. Do not reinstall over it unless the operator asks.

| Item | Value |
| --- | --- |
| Install root | `~/.keyrelay` |
| Service | LaunchAgent `com.linhtran.keyrelay` |
| Listen | `127.0.0.1:8319` |
| Date verified | 2026-10-18 |
| Left untouched | `~/.cliproxyapi` on `127.0.0.1:8317`, LaunchAgent `com.linhtran.cliproxyapi` |

## Sample this file now uses

The reference sample is the operator-edited install at `~/.keyrelay`, snapshotted on 2026-10-18 after the key-shop policy was set. Do not add an AI Provider Codex block.

| Sample | Path |
| --- | --- |
| Config | `examples/keyrelay/config.example.yaml` |
| key-shop database | `examples/keyrelay/key-shop.sample.db` |
| Codex auth | not in git. Each machine must log in through Auth Files |

The sample database has one active customer key, label `key-vippro`, customer `vippro`, no expiry, unlimited tokens, unlimited requests. The plaintext `ck_` secret is not in git. Copying the database keeps that key only if the operator still has the secret shown at create time (`ck_` prefix `TZNZ`, last 4 `B9Ck`).

## Mandatory before the stack can be used

Codex Auth Files login is required. Do not report the install ready without it.

1. Open the management panel, menu **Auth Files**, and complete **Codex** login. Or run `"$INSTALL_ROOT/cli-proxy-api" --codex-login --config "$INSTALL_ROOT/config.yaml"`.
2. The new file must land in `<install-root>/auth/`. `plus-preference` is `only`, so the account must be Plus. A filename ending in `-plus.json` is the required signal. A free account does not count.
3. Do not copy auth JSON from the reference machine or from the router. Tokens stay on the machine that logged in.
4. Change the bind address only if other machines must reach this process. The verified install listens on `127.0.0.1`.

## Tóm tắt cho người vận hành

Cài một CPA mới, không đụng CPA đang chạy ở `~/.cliproxyapi` cổng `8317` và không ghi đè router.

Điều kiện bắt buộc trước khi cài: tool `ch-relay` đã chạy và `http://127.0.0.1:11500/v1` trả lời. `GET /healthz` phải là `200 {"ok":true}`. `GET /v1/models` không có key trả `401` là đúng, vì tool yêu cầu auth.

Mẫu config và database là bản local `~/.keyrelay` với policy key-shop. Không thêm AI Provider Codex. Không lấy lại config router hay VPS để ghi đè mẫu này.

`ch-relay-vippro.base-url` mặc định là `http://127.0.0.1:11500/v1`.

Bắt buộc login Codex trong **Auth Files** trước khi dùng. `plus-preference` là `only`, nên account phải là Plus. Không copy file auth từ máy mẫu.

Sau khi cài, thông tin đăng nhập nằm ở `~/.keyrelay/ACCESS.txt`. Panel: `http://127.0.0.1:8319/management.html`.

## Agent rules

Follow this section exactly on a new machine. Do not improvise a second install root or a second plugin id.

1. Stop if `ch-relay` is not already healthy at `http://127.0.0.1:11500/v1`. Do not install a stack that points at a dead relay, and do not start installing `ch-relay` from this file.
2. Install into a new root. Never replace `~/.cliproxyapi`, `/root/.cliproxyapi`, `/opt/cliproxyapi`, or `/opt/cliproxyapi-v8030` unless the operator explicitly says that path is the target and a backup already exists. Never replace `config.yaml` or `auth/` on the router.
3. Generate a new operator API key and a new management password. Do not copy production keys. Do not use `your-api-key-1`, `your-api-key-2`, or `your-api-key-3`. Those values disable the proxy API.
4. Copy `examples/keyrelay/key-shop.sample.db` to `<install-root>/key-shop/key-shop.db` before the first start. Do not start with an empty database and call that the sample.
5. Do not copy Codex auth JSON from another machine. Login on this machine is mandatory. The install is not usable until a Plus Codex file exists.
6. Do not commit `ACCESS.txt`, live `config.yaml`, or auth JSON. The sample database in `examples/keyrelay/` is the only key-shop database that belongs in git.
7. If `<install-root>` already has a running service from this runbook, print its access block and stop. Do not rebuild over operator edits.
8. After verify, write the access block. If Codex Plus login is missing, status is `NOT READY`. Do not claim the stack can be used.

## 1. Prerequisite: ch-relay

Required API URL: `http://127.0.0.1:11500/v1`.

On the authoring Mac this is Docker container `ch-relay` (`ch-relay-local:latest`), publishing `127.0.0.1:11500` to container port `12500`. The plugin does not have its own upstream API key. It sends a Codex access token as `Authorization: Bearer`.

```bash
curl -fsS http://127.0.0.1:11500/healthz
# expect: {"ok":true}

curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:11500/v1/models
# expect: 401
# connection refused, timeout, or 000 means stop
```

The sample `base-url` is always `http://127.0.0.1:11500/v1`. Do not copy any other relay address into a new install.

## 2. Choose paths

| Platform | Install root | Plugin dir | Service |
| --- | --- | --- | --- |
| macOS, verified | `~/.keyrelay` | `plugins/darwin/arm64/*.dylib` | LaunchAgent `com.linhtran.keyrelay` |
| Linux user | `~/.keyrelay` | `plugins/linux/<arch>/*.so` | systemd user unit, or the system unit below if root |
| Linux server | `/opt/keyrelay` | `plugins/linux/<arch>/*.so` | `/etc/systemd/system/keyrelay.service` |
| Router GL-MT6000 | do not use this runbook to deploy | `plugins/linux/arm64/*.so` | existing `/etc/init.d/cliproxyapi` |

Default listen address is `127.0.0.1`. Default port is `8319` if it is free. If `8319` is taken, pick another port and record it. Do not kill the process that owns the port. On the authoring Mac, `8317` is the other CPA and must stay.

`plugins.dir` is the parent `plugins` directory. The loader reads `plugins/<GOOS>/<GOARCH>/` first. The file stem must be `ch-relay-vippro` or `key-shop`. Extension is `.dylib` on darwin, `.so` on linux, `.dll` on windows.

Set `oauth.auth-dir` and `key-shop.data_file` to absolute paths under the install root. On a router the process home can be `/`, so a default `~/.cliproxyapi/...` path is wrong there.

## 3. Build

Run from this repository, branch `feat/model-mapping`. Verified with Go 1.27.0 on darwin/arm64. `CGO_ENABLED=1` is required for `-buildmode=c-shared`. Delete the generated `.h` files. Do not commit the libraries.

```bash
INSTALL_ROOT="$HOME/.keyrelay"
mkdir -p "$INSTALL_ROOT/plugins/darwin/arm64" \
  "$INSTALL_ROOT/auth" "$INSTALL_ROOT/static" \
  "$INSTALL_ROOT/logs" "$INSTALL_ROOT/key-shop"
chmod 700 "$INSTALL_ROOT" "$INSTALL_ROOT/auth" "$INSTALL_ROOT/key-shop"

go build -o "$INSTALL_ROOT/cli-proxy-api" ./cmd/server

(
  cd examples/plugin/ch-relay-vippro/go
  CGO_ENABLED=1 go build -buildmode=c-shared \
    -o "$INSTALL_ROOT/plugins/darwin/arm64/ch-relay-vippro.dylib" .
  rm -f ch-relay-vippro.h "$INSTALL_ROOT/plugins/darwin/arm64/ch-relay-vippro.h"
)

(
  cd examples/plugin/key-shop/go
  CGO_ENABLED=1 go build -buildmode=c-shared \
    -o "$INSTALL_ROOT/plugins/darwin/arm64/key-shop.dylib" .
  rm -f key-shop.h "$INSTALL_ROOT/plugins/darwin/arm64/key-shop.h"
)
```

Linux, same machine as the build target, replace `darwin/arm64` and `.dylib` with `linux/amd64` or `linux/arm64` and `.so`.

Linux arm64 for a glibc 2.27 host is the router method. It was not re-run for this local install. Do not use it to replace the live router unless the operator asks, and then follow the fork rule in `AGENTS.md`: backup first, never replace `config.yaml` or `auth`, do not ship `CGO_ENABLED=0`.

```bash
CC='zig cc -target aarch64-linux-gnu.2.27' CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
  go build -ldflags '-s -w' -buildmode=c-shared -o ch-relay-vippro.so .
```

If `readelf -l` shows interpreter `/lib/ld-linux-aarch64.so.1`, rewrite that exact string to `/opt/ld-linux-aarch64.so.1`. Do not pass `--dynamic-linker` to zig. `LD_PRELOAD` must be unset. Rebuild the `.so` against the host it will load into. A config reload does not load a newly copied library. Restart the process.

Windows uses the same two plugin directories and `.dll`. Service installation on Windows was not verified. The config, prerequisite check, and HTTP checks stay the same.

## 4. Panel

`disable-auto-update-panel: true` is required so upstream does not replace the fork panel.

Build the sibling repo `Cli-Proxy-API-Management-Center` when `dist/index.html` is missing or older than the branch you are installing. Copy the single-file build:

```bash
cp ../Cli-Proxy-API-Management-Center/dist/index.html \
  "$INSTALL_ROOT/static/management.html"
```

The verified copy is `3372354` bytes, built 2026-10-18. The panel is served from the directory that contains `config.yaml`, at `static/management.html`, unless `MANAGEMENT_STATIC_PATH` or `WRITABLE_PATH` is set. Do not set `WRITABLE_PATH` to `~/.cliproxyapi`.

## 5. Secrets and config

Generate both secrets before writing config. Restrict the files to `0600`.

```bash
python3 - <<'PY'
import secrets, pathlib
root = pathlib.Path.home() / ".keyrelay"
mgmt = "KR-" + secrets.token_urlsafe(18)
api = "kr_" + secrets.token_hex(24)
path = root / ".secrets"
path.write_text(f"MGMT={mgmt}\nAPI={api}\n", encoding="utf-8")
path.chmod(0o600)
PY
```

Copy `examples/keyrelay/config.example.yaml` to `$INSTALL_ROOT/config.yaml`. That file is the reference, taken from `~/.keyrelay/config.yaml`. Substitute:

| Placeholder | Meaning |
| --- | --- |
| `__INSTALL_ROOT__` | Absolute install root |
| `__HOST__` | `127.0.0.1` unless the operator asked to expose it |
| `__PORT__` | `8319` if free |
| `__OPERATOR_API_KEY__` | the generated `kr_...` key |
| `__MANAGEMENT_PASSWORD__` | the generated `KR-...` password, plaintext |

```bash
python3 - <<'PY'
from pathlib import Path
import os
root = Path(os.environ["INSTALL_ROOT"])
text = Path("examples/keyrelay/config.example.yaml").read_text()
repl = {
    "__INSTALL_ROOT__": str(root),
    "__HOST__": os.environ.get("HOST", "127.0.0.1"),
    "__PORT__": os.environ.get("PORT", "8319"),
    "__OPERATOR_API_KEY__": os.environ["OPERATOR_API_KEY"],
    "__MANAGEMENT_PASSWORD__": os.environ["MANAGEMENT_PASSWORD"],
}
for key, value in repl.items():
    text = text.replace(key, value)
dest = root / "config.yaml"
dest.write_text(text)
dest.chmod(0o600)
PY
```

Do not add `api-keys.codex`. `ch-relay-vippro.base-url` stays `http://127.0.0.1:11500/v1`.

The first start hashes `management.secret-key` in memory. This host also rewrote `config.yaml` and stored the bcrypt hash. Keep the plaintext in `.secrets` and `ACCESS.txt`. The panel login is the plaintext password, not the hash.

`prompt-rules` exists on the router and is disabled. It is not part of this stack. Do not copy that plugin unless asked.

`chmod 600` the config after writing it.

## 5b. Seed the key-shop database

Copy the sample before the first start. If the process already created an empty database, stop it, replace the file, then start again.

```bash
mkdir -p "$INSTALL_ROOT/key-shop"
cp examples/keyrelay/key-shop.sample.db "$INSTALL_ROOT/key-shop/key-shop.db"
chmod 600 "$INSTALL_ROOT/key-shop/key-shop.db"
```

Do not copy `key-shop.db-wal` or `key-shop.db-shm`. The sample file is a consistent snapshot. Do not copy a customer database from the router or from `91.98.173.76`.

## 6. Start the service

Libraries must already be in the plugin directory before the first start.

macOS, verified:

```bash
cat > "$HOME/Library/LaunchAgents/com.linhtran.keyrelay.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.linhtran.keyrelay</string>
    <key>ProgramArguments</key>
    <array>
        <string>${INSTALL_ROOT}/cli-proxy-api</string>
        <string>--config</string>
        <string>${INSTALL_ROOT}/config.yaml</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${INSTALL_ROOT}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${INSTALL_ROOT}/logs/launchd.log</string>
    <key>StandardErrorPath</key>
    <string>${INSTALL_ROOT}/logs/launchd.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$(id -u)/com.linhtran.keyrelay" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.linhtran.keyrelay.plist"
```

Restart after a binary, plugin, or config change:

```bash
launchctl kickstart -k "gui/$(id -u)/com.linhtran.keyrelay"
```

Linux systemd is the VPS unit shape, adapted to this root. It was not started during the macOS verification. Enable it only on the machine being installed.

```ini
[Unit]
Description=KEYRELAY CLIProxyAPI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/keyrelay
ExecStart=/opt/keyrelay/cli-proxy-api --config /opt/keyrelay/config.yaml
Restart=on-failure
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now keyrelay.service
```

Foreground debug, any platform:

```bash
unset LD_PRELOAD
exec "$INSTALL_ROOT/cli-proxy-api" --config "$INSTALL_ROOT/config.yaml"
```

## 6b. Codex Auth Files login

This step is mandatory. `plus-preference: only` will not send a request until a Plus Codex account is logged in on this machine.

1. Open `http://__HOST__:__PORT__/management.html` and log in with the management password.
2. Open **Auth Files** and start Codex login. Finish the browser OAuth prompt.
3. Confirm `<install-root>/auth/` contains a file whose name ends with `-plus.json`.
4. Do not upload the reference file `codex-44a03b43-axel.mattia12+bqvlk@googlemail.com-plus.json`. That token belongs to the reference machine.

CLI equivalent, if the panel login is not available:

```bash
"$INSTALL_ROOT/cli-proxy-api" --codex-login --config "$INSTALL_ROOT/config.yaml"
```

The install is `NOT READY` until the check in the next section sees one eligible Plus Codex file.

## 7. Verify

Substitute the generated secrets. Every check below passed on 2026-10-18 against `127.0.0.1:8319`.

```bash
curl -fsS http://127.0.0.1:11500/healthz

curl -fsS http://127.0.0.1:8319/v1/models \
  -H "Authorization: Bearer $OPERATOR_API_KEY"

curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8319/management.html
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8319/client
curl -fsS -o /dev/null -w '%{http_code}\n' \
  http://127.0.0.1:8319/v0/resource/plugins/ch-relay-vippro/dashboard
curl -fsS -o /dev/null -w '%{http_code}\n' \
  http://127.0.0.1:8319/v0/resource/plugins/key-shop/admin

curl -fsS http://127.0.0.1:8319/v0/management/plugins \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD"
curl -fsS http://127.0.0.1:8319/v0/management/ch-relay-vippro/stats \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD"
curl -fsS http://127.0.0.1:8319/v0/management/plugins/key-shop/stats \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD"

curl -fsS "http://127.0.0.1:8319/v8/management/credentials?page_size=50" \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD"
```

Pass conditions:

- Log contains `pluginhost: plugin loaded` for `ch-relay-vippro` and `key-shop`, then `plugin registered`.
- `/v1/models` includes the `ch-relay-vippro` models: `vippro/gpt-6-sol`, `vippro/gpt-6-astra`, `vippro/gpt-6.1-sol`, `vippro/gpt-6-luna`, `vippro/gpt-5.6-terra`, `vippro/gpt-5.6-luna`, `vippro/gpt-5.6-sol`. Do not require aliases that only existed because of an AI Provider Codex loopback.
- Panel, `/client`, VipPro dashboard, and key-shop admin return `200`.
- Plugin list has both ids `effective_enabled: true`.
- key-shop stats return `keys_total` of at least 1 after the sample database is loaded. The sample key label is `key-vippro`.
- Credentials include at least one file with `provider` `codex`, `disabled` false, `unavailable` not true, and `name` ending in `-plus.json`. This is the ready gate. Zero Plus files means `NOT READY`.
- The previous CPA, if any, is still listening on its own port.

Management auth accepts `Authorization: Bearer <password>` or `X-Management-Key`. After the host rewrites config, the plaintext still works. The bcrypt hash in `config.yaml` is not the login password.

Do not copy a Codex auth file from the reference install to satisfy the ready gate.

## 8. Export access

Write `<install-root>/ACCESS.txt` mode `0600` and print the same text to the operator. Use the real values, not the placeholders.

```text
KEYRELAY access
install_root: <absolute path>
service: <launchd label or systemd unit>
config: <path>
log: <path>
auth_dir: <path>
api_base: http://<host>:<port>/v1
operator_api_key: <kr_...>
management_password: <plaintext>
management_panel: http://<host>:<port>/management.html
vippro_dashboard: http://<host>:<port>/v0/resource/plugins/ch-relay-vippro/dashboard
key_shop_admin: http://<host>:<port>/v0/resource/plugins/key-shop/admin
customer_page: http://<host>:<port>/client
ch_relay: http://127.0.0.1:11500/v1
codex_auth: Auth Files Codex login required
codex_plus_ready: true|false
key_shop_db: <path>
sample_key_label: key-vippro
status: READY|NOT READY
models:
  vippro/gpt-6-sol
  vippro/gpt-6-astra
  vippro/gpt-6.1-sol
  vippro/gpt-6-luna
  vippro/gpt-5.6-terra
  vippro/gpt-5.6-luna
  vippro/gpt-5.6-sol
```

`status` is `READY` only when `codex_plus_ready` is true. Otherwise print `NOT READY` and the Auth Files login URL. Do not omit that line.

Customer clients use `api_base` plus a `ck_` key. The operator key is not quota-limited. Do not enable exclusive frontend auth. That would disable the operator key.

## 9. Where the config data came from

Read on 2026-10-18. No remote file was modified.

| Block | Source | Sample rule |
| --- | --- | --- |
| Config | `examples/keyrelay/config.example.yaml`, copied from `~/.keyrelay/config.yaml` | Use that file. Redact secrets. Force `base-url` to `http://127.0.0.1:11500/v1` |
| `ch-relay-vippro` | same file, except `base-url` | Always `http://127.0.0.1:11500/v1` |
| AI Provider Codex | not used | Leave `api-keys.codex` empty. Do not recreate it |
| `key-shop` policy | same file, `model_access_mode: custom` | Keep the alias list |
| key-shop database | `examples/keyrelay/key-shop.sample.db`, snapshotted from `~/.keyrelay/key-shop/key-shop.db` | Copy before first start. Do not copy router or VPS databases |
| Codex auth | logged in on the reference machine | Do not copy. Each install must login through Auth Files |
| Panel | `Cli-Proxy-API-Management-Center/dist/index.html` | Copied to `static/management.html` |

The router key-shop block is `enabled: false`. It was not used. The VPS also has an older CPA at `/opt/cliproxyapi` on port `8317`. That process is not the sample.

## 10. Stop this install only

macOS:

```bash
launchctl bootout "gui/$(id -u)/com.linhtran.keyrelay"
```

That does not stop `com.linhtran.cliproxyapi`. Deleting `~/.keyrelay` removes this install. Do not delete `~/.cliproxyapi`.

## 11. Refresh the sample later

When the operator says the local config is the sample:

1. Read `<install-root>/config.yaml`.
2. Refresh `examples/keyrelay/config.example.yaml` from it.
3. Put placeholders back over the operator key, management secret, install root, host, and port.
4. Keep `base-url: "http://127.0.0.1:11500/v1"`. Do not copy another relay address into the sample. Leave `api-keys.codex` empty.
5. If the operator also refreshed the key-shop database, snapshot it again with `sqlite3 "$INSTALL_ROOT/key-shop/key-shop.db" ".backup examples/keyrelay/key-shop.sample.db"`.
6. Do not paste `ACCESS.txt`, auth JSON, or token fields into this document. Codex login stays a required step, not a copied file.
