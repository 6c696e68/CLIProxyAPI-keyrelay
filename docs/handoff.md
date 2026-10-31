# keyrelay handoff

This is the entry point for the custom CLIProxyAPI deployment ("keyrelay"). It covers what runs,
which repositories build it, every fork-only behaviour that must survive an upstream sync, the
HTTP surface, configuration, build, deploy, and operations.

Per-feature references are linked in [Documentation map](#15-documentation-map). Read this page
first, then open the reference for the part you are changing.

## 1. What actually runs

Four artifacts, three of them built from two repositories:

| Artifact | Built from | Lives on the host at |
| --- | --- | --- |
| `cli-proxy-api` | `CLIProxyAPI/cmd/server` | `/root/.cliproxyapi/cli-proxy-api` |
| `key-shop.so` | `CLIProxyAPI/examples/plugin/key-shop/go` | `plugins/linux/arm64/key-shop.so` |
| `ch-relay-vippro.so` | `CLIProxyAPI/examples/plugin/ch-relay-vippro/go` | `plugins/linux/arm64/ch-relay-vippro.so` |
| `management.html` | `Cli-Proxy-API-Management-Center` | `/root/.cliproxyapi/static/management.html` |

Operator data is not in git and is never replaced by a deploy: `config.yaml`, `auth/`, the key-shop
SQLite file, and the `ch-relay-vippro` stats file.

```text
customer  -- /v1  (Bearer ck_...)  --+
operator  -- panel (management key) --+--> cli-proxy-api :8319
                                          |
                                          +-- routing.model-mapping      (before routing)
                                          +-- key-shop plugin            (auth, quota, billing,
                                          |                               groups, tunnels, partner API)
                                          +-- ch-relay-vippro plugin     (upstream executor)
                                          |
                                          +--> upstream: ChatGPT Codex OAuth, or the vippro relay
```

Three distinct audiences reach the same process:

- **Operator** uses `management.secret-key` for `/v8/management` and the panel.
- **Customer** uses a `ck_` secret for `/v1`, `/v1/models`, and `/client`.
- **Partner** uses a `ksp_` secret for `/keyshop/partner/v1`.

Operator keys in `access.api-keys` keep working and are never quota-limited. Do not enable
exclusive frontend auth: it would disable the operator keys.

## 2. Repositories, branches, remotes

| Repository | Role | Branch |
| --- | --- | --- |
| `lantrinh1999/CLIProxyAPI` | `origin`, the working fork | `feat/model-mapping` |
| `6c696e68/CLIProxyAPI-keyrelay` | public handoff of the same tree | `main` = `feat/model-mapping` |
| `router-for-me/CLIProxyAPI` | `upstream`, sync source only | `main` |
| `lantrinh1999/Cli-Proxy-API-Management-Center` | `origin`, the panel fork | `feat/model-mapping` |
| `6c696e68/Cli-Proxy-API-Management-Center` | public handoff of the panel | `main` = `feat/model-mapping` |
| `router-for-me/Cli-Proxy-API-Management-Center` | panel `upstream` | `main` |

Rules:

- Push only when asked. `origin` uses the `lantrinh1999` GitHub account; the `6c696e68` handoff
  repos use the `6c696e68` account. The active `gh` account is `lantrinh1999`, so a handoff push
  needs an explicit credential (see [§10.5](#105-publishing-the-handoff-repositories)).
- Sync source is `upstream/main` only. Never merge old `origin/feat/model-mapping` history.
- `main` on the handoff repos is a fast-forward of the feature branch. Never force-push `main`.
- Both repositories stay on the same branch; a panel change and a backend change that depend on
  each other ship together.

## 3. Fork-only behaviour that must survive an upstream sync

Take upstream infrastructure when it is newer (keepalive, ETag, shadow-copy plugin loading,
`CurrentConfig`, `persistLocked`, new quota cards). Keep the decisions below. If a custom
implementation has gone stale, rewrite it against the new upstream code; do not drop the flow.

| Area | Contract | Entry point |
| --- | --- | --- |
| Global model mapping | Runs before provider routing. First enabled rule wins. Thinking suffixes stay attached. `force-mapping` echoes the client model. | `internal/modelmapping/`, `sdk/api/handlers/handlers_model_mapping.go` |
| Subagent blocking | `client.codex.disable-subagents` rejects spawned Codex threads, then strips spawn tool namespaces, on HTTP Responses and the Responses websocket. | `sdk/api/handlers/openai/` |
| Websocket routing | Websocket auth, passthrough, and compaction use `StreamRouteModel`, never the raw client model. | `sdk/api/handlers/handlers_context.go` |
| ch-relay-vippro | Forwards through host auth and `host.http`; the plugin never receives or refreshes Codex tokens. | `examples/plugin/ch-relay-vippro/go` |
| key-shop | `ck_` keys with expiry, token and request caps, model groups, partner sales, tunnels, and the `/client` pages. | `examples/plugin/key-shop/go`, `internal/api/key_shop_public.go` |
| Config reads | Request paths read `CurrentConfig()`. Management writes hold `h.mu` and call `persistLocked`. | `internal/api/` |
| Payload rules | User payload rules stay the last semantic barrier in every executor. | `internal/runtime/executor/` |
| Usage id | `NewUsageReporter` uses the lifecycle request id from `usage.WithExecutionRequestID`. Without it usage and completion cannot share an id and every success bills 0. | `internal/runtime/executor/helps` |
| Admission | `request.intercept_after` must not reserve again. A second insert used to fail the SQLite primary key and return `key_store_unavailable`. | key-shop plugin |

## 4. Global model mapping

Rewrites the client-requested model **before** provider routing. It is not `oauth-model-alias` and
not a per-credential `models[].alias`.

```yaml
routing:
  model-mapping:
    - match: "gpt-5.4"
      target: "gpt-5.5"
    - match: "gpt-5.*"
      target: "gpt-5.5"
      mode: wildcard
    - match: "^gpt-5\\.4(-.*)?$"
      target: "gpt-5.5$1"
      mode: regex
```

- First enabled matching rule wins.
- `mode` is `exact`, `wildcard`, or `regex`. Empty infers `wildcard` when the match contains `*`,
  otherwise `exact`. Exact is case-insensitive; `*` means any substring.
- Match runs on the base model after `thinking.ParseSuffix`. `gpt-5.4(high)` becomes `gpt-5.5(high)`.
  A target that already carries a suffix wins.
- Prefixed credentials (`teamA/gpt-5.4`) match the full string; use `*/gpt-5.4` to catch them.
- `force-mapping` defaults to `true`, so the response still shows the name the client sent.
- Mapped names are **not** added to `/v1/models`.
- Invalid regex is dropped and logged; it does not crash the server.

Management: `GET/PUT/PATCH/DELETE /v8/management/routing/model-mapping`, or the generic path
`/v8/management/config/routing/model-mapping`. There is no `/v0/management` route for this. The TUI
has a **Model Mapping** tab. The canonical YAML key is `routing.model-mapping`; a legacy root
`model-mapping:` list still loads, and a v8 write migrates it.

Never implement per-executor remaps. One hook serves OpenAI, Claude, Gemini, Codex, streaming,
token counting, and websockets.

## 5. ch-relay-vippro

A plugin executor that forwards Chat Completions and OpenAI Responses to one OpenAI-compatible
HTTPS relay, using a Codex OAuth access token from the host auth store as the bearer. It has no
`api-key`, no `keys`, and no token refresh.

Client model shape: `vippro/gpt-5.4` (prefix plus alias). `frontend_auth` is untouched; the plugin
claims the model in `ModelRouter`.

| Field | Meaning |
| --- | --- |
| `enabled` | `false` claims no models and rejects requests. |
| `base-url` | HTTPS origin plus optional path. `http` is allowed only for localhost, private IPs such as `192.168.8.1`, and `100.64.0.0/10` (Tailscale). Userinfo, query, and fragment are rejected. |
| `prefix` | Client model prefix, trimmed of slashes. |
| `headers` | Extra upstream headers. `$NAME` copies that client header; `$CPA-SESSION-ID` uses the executor session. |
| `request-retry` | Extra attempts on the next account with the same upstream model. Default `3`, max `32`. |
| `fallback-retry` | Extra attempts using the row `fallback` names after a model refusal. Default `0`, max `3`. |
| `rotation-mode` | `round-robin`, `fill-first`, `least-recent`, or `sticky`. |
| `plus-preference` | `off`, `prefer`, or `only`. Plus means the bearer JWT says `chatgpt_plan_type: plus`. |
| `free-models` | Upstream names, never aliases, that may also use non-Plus accounts. |
| `sticky-seconds` | Sticky session lifetime. Default `300`, max `86400`. |
| `model-cooldown-minutes` | Per-account, per-model cooldown after an upstream model refusal. Default `1440`, max `10080`. |
| `stats-file` | Hourly buckets kept for 90 days, plus a `.cooldowns.json` sidecar. |
| `models` | Rows of `name`, optional `alias`, `display-name`, `fallback`, and OpenAI model fields. |

Behaviour worth remembering:

- A 2xx body or stream is returned unchanged. The plugin never rewrites response bytes, so a
  fallback response shows the fallback model name.
- Client errors (400/404/409/413/415/422 that are not a model refusal) are returned at once: no
  retry, no penalty, no pin change.
- A model refusal cools that account and access token for that model only. Other models keep using
  the account.
- Response bytes are not inspected for account switching, only for the model-refusal cooldown that
  can arrive inside a 2xx stream.

Endpoints:

- `GET /v0/management/ch-relay-vippro/stats` returns the cached stats without calling
  `host.auth.list`.
- `POST /v0/management/ch-relay-vippro/accounts/check` starts a background scan and returns the
  same JSON.
- `GET /v0/resource/plugins/ch-relay-vippro/dashboard` is the plugin-served dashboard. It is a
  plugin resource, so the host does not require the management key for it.

Error codes: `disabled`, `unknown_model`, `invalid_payload`, `unsupported_format`,
`no_plus_account`, `no_account`, `accounts_cooling`, `auth_unavailable`, `upstream_error`.

## 6. key-shop

`key-shop` issues customer API keys for one process. The plugin id, library stem, and
`plugins.configs` key are all `key-shop`. Secrets are `ck_...` and are never written into
`config.yaml` or `auth/`.

### 6.1 Key lifecycle

- Defaults: 7-day expiry, 100,000,000 token cap, no request cap, a 32,768-token reservation held
  until usage arrives or 15 minutes pass, and 30-day request log retention.
- Billed tokens are the upstream `total_tokens`, falling back to input + output. A failed,
  rejected, or canceled request is logged and bills `0`.
- Every recorded call consumes one request-quota unit, including failed and rejected attempts, so
  error spam cannot avoid the cap.
- `request_limit` denials are `429 request_limit_exceeded`. Token denials are
  `402 insufficient_quota` / `token_limit_exceeded`.
- Auth runs first and cannot set a status, so an already expired, disabled, or token-exhausted key
  returns `401`. A key exhausted only by its request cap authenticates so the interceptor can
  return `429`.

### 6.2 Enforcement order

```text
ck_ secret
  -> frontend_auth.authenticate
  -> host keyshop.resolve_model         (grouped key: global YAML policy is not consulted)
  -> request.intercept_before           (group or global policy, then reserve tokens)
  -> provider routing
  -> upstream
  -> usage.handle                       (token snapshot staged in usage_notes)
  -> request.complete                   (bills and releases the reservation)
```

A model outside the allowed list returns `403 model_not_allowed` before routing and before
admission: no token reservation, but one request-quota unit and a rejected request row.

These paths bypass the interceptor, so every `ck_` key is rejected there: `/v1/live`,
`/v1/realtime`, `/v1/alpha/search`, `/backend-api/codex/alpha/search`.

### 6.3 Model groups

A group is `{id, name, description, all_models, models[]}` where each model is `{name, alias}`.
The built-in `all-models` group always exists and cannot be edited or deleted.

- For a grouped key the host calls `keyshop.resolve_model` before routing and does not consult the
  global YAML policy. The client-visible name is the per-group alias, or the upstream name when the
  alias is empty. A bare upstream name is rejected when that entry has an alias.
- Allowed aliases are rewritten to the upstream name with any thinking suffix preserved.
- An unknown group fails closed. A key with an empty `group_id` keeps the legacy global-policy
  behaviour.
- A group that keys still use cannot be deleted (`409` with `key_count`).

### 6.4 Storage

SQLite via `modernc.org/sqlite`, WAL, `busy_timeout=5000`, file mode `0600` in a `0700` directory.
Only the SHA-256 of a secret is stored, plus a 4-character prefix and the last 4 characters.

Tables: `keys`, `model_groups`, `reservations`, `requests`, `usage_notes`. Opening a database from
the previous plugin only adds columns and tables; it never rebuilds or drops one.

Default path is `~/.cliproxyapi/key-shop/key-shop.db`. On the router the process home may be `/`,
so set an absolute `data_file`.

### 6.5 Partner sales

Partners mint `ck_` keys over an authenticated API. key-shop takes no money; the shop debits only
after a mint succeeds.

- Public base: `/keyshop/partner/v1`. Auth: `Authorization: Bearer ksp_...`.
- Routes: `GET health`, `GET plans`, `POST mint|confirm|void|status|refs`.
- Requires `partner_api_enabled: true`. While off, every call returns `503 partner_api_disabled`.
- `mint` is idempotent by `ref` and capped per UTC day. `confirm` and `void` are bookkeeping: a
  minted key is active immediately.
- The host strips inbound `X-Key-Shop-Partner*` headers and sets `X-Key-Shop-Partner: 1` and
  `X-Key-Shop-Client-IP` itself. Every other `/keyshop/` path returns 404.
- Plans and partners are managed under `/v0/management/plugins/key-shop/{plans,partners}...`.

Integrator contract, error codes, and idempotency rules:
[partner-api.md](../examples/plugin/key-shop/docs/partner-api.md).

### 6.6 Tunnels

The Tunnels tab manages Cloudflare tunnels created by the plugin itself. It never discovers or
signals a device tunnel.

- `kind` is `token` (named tunnel, token only in the child environment) or `quick` (`--url`, public
  `trycloudflare.com` URL parsed from the child log).
- The plugin signals only a process it spawned, or a recorded pid whose `/proc/<pid>/environ`
  contains `KEYSHOP_TUNNEL_ID=<id>`, re-checked immediately before each signal.
- Child `HOME` is `<data dir>/tunnels/<id>`, so the device `~/.cloudflared` is not used.
- Keep-alive is on by default and needs Linux. With root and systemd the child runs in a transient
  scope `keyshop-tunnel-<id>-<random>`, so it survives a plugin reload and a service restart.
  `systemctl stop 'keyshop-tunnel-*.scope'` ends every kept scope.
- Config: `cloudflared_path`, `tunnels_enabled` (default true), `tunnel_keep_alive` (default true),
  `tunnel_watch_interval` (default `2s`), `tunnel_log_max_kb` (default 1024).

A tunnel without an explicit `target_url` falls back to `tunnel_target_url`, then to the
`host_base_url` the host sends on register.

### 6.7 Customer pages

- `GET /client` serves the customer dashboard.
- `GET /client/api/session` returns status, limits, `group`, `model_access_mode`, allowed `models`,
  `base_url`, and a bounded request page.
- `/key-dashboard` and `/key-dashboard/api/session` redirect to the same pages.
- A direct `GET /v0/resource/plugins/key-shop/dashboard` without `X-Key-Shop-Alias: 1` redirects to
  `/client`.

Model listing for a `ck_` key follows the same group or global policy, shows the alias, reserves no
tokens, and uses no request quota. Operator keys see the full list.

## 7. HTTP surface

| Prefix | Auth | Purpose |
| --- | --- | --- |
| `/v8/management` | `management.secret-key` | Panel API, including `routing/model-mapping` |
| `/v0/management/plugins/key-shop` | management secret | Keys, groups, plans, partners, tunnels, requests, stats |
| `/v0/management/ch-relay-vippro` | management secret | Relay stats and account check |
| `/v0/resource/plugins/<id>/...` | none | Plugin-served pages: key-shop admin, vippro dashboard |
| `/v1`, `/v1beta` | `ck_` or operator key | Proxy API |
| `/client`, `/key-dashboard` | `ck_` | Customer pages |
| `/keyshop/partner/v1` | `ksp_` partner secret | Partner sales API |

`/v0/management` is deprecated upstream and is not maintained. The fork keeps only the plugin
routes listed above plus the panel's two vippro endpoints. Do not add new `/v0/management` routes.
Do not edit the deprecated surface except to make it compile.

Key-shop management routes (abbreviated; full table in the README):

| Action | Method | Path |
| --- | --- | --- |
| Create / bulk create | POST | `/keys`, `/keys/bulk` |
| List | GET | `/keys` |
| Update / bulk update | POST | `/keys/update`, `/keys/bulk-update` |
| Rotate / reset usage / delete | POST | `/keys/rotate`, `/keys/reset-usage`, `/keys/delete` |
| Groups | GET/POST | `/groups`, `/groups/update`, `/groups/delete` |
| Requests / stats / usage | GET | `/requests`, `/stats`, `/stats/usage` |
| Tunnels | GET/POST | `/tunnels`, `/tunnels/{update,delete,start,stop}`, `/tunnels/logs` |

## 8. Configuration

A minimal working configuration for this fork:

```yaml
config-version: 8

server:
  port: 8319

management:
  secret-key: "<management password>"
  allow-remote: true
  disable-auto-update-panel: true

access:
  api-keys:
    - "<operator key>"

routing:
  model-mapping:
    - match: "gpt-5.4"
      target: "gpt-5.5"

client:
  codex:
    disable-subagents: true

plugins:
  enabled: true
  dir: "plugins"
  configs:
    key-shop:
      enabled: true
      priority: 100
      data_file: "/root/.cliproxyapi/key-shop/key-shop.db"
      model_access_mode: full
      partner_api_enabled: true
      public_base_url: "https://cpa.example.com"
      partner_daily_key_cap: 300
    ch-relay-vippro:
      enabled: true
      priority: 1
      base-url: "https://relay.example.com/v1"
      prefix: "vippro"
      rotation-mode: round-robin
      plus-preference: prefer
      request-retry: 3
      models:
        - name: "gpt-5.4"
          alias: "gpt-5.4"
```

Notes:

- `plugins.enabled` is false in `config.example.yaml`. Turn it on in the config you actually run.
- A newly copied `.so` needs a process restart; a config reload alone does not load it.
- Empty `data_file` uses `~/.cliproxyapi/key-shop/key-shop.db`. On the router, always set an
  absolute path.
- `.env` in the working directory is auto-loaded.

## 9. Build, test, run

Server, from the repository root:

```bash
gofmt -w .
go build -o cli-proxy-api ./cmd/server
go test ./...
go test -v -run TestName ./path/to/pkg
go run ./cmd/server --config config.yaml
```

Plugins:

```bash
cd examples/plugin/key-shop/go
go build -buildmode=c-shared -o key-shop.dylib . && rm -f key-shop.h
go test ./... -count=1

cd examples/plugin/ch-relay-vippro/go
go build -buildmode=c-shared -o ch-relay-vippro.dylib . && rm -f ch-relay-vippro.h
go test ./... -count=1
```

Panel:

```bash
cd ../Cli-Proxy-API-Management-Center
bun install --frozen-lockfile
bun run dev          # http://localhost:5173
bun run test         # bun test
bun run lint
bun run verify       # test + lint + build
```

Known panel failures at the current tip: seven tests in `tests/visualConfigRebase.test.ts` and
`tests/configDocumentSemantics.test.ts` fail identically on a clean checkout of the pushed commit.
They concern raw YAML payload-rule drafts and are unrelated to the plugin config work. `lint` and
`build` pass.

`go build ./cmd/server` must succeed after any Go change; that is the minimum gate before a commit.
Run `gofmt -w .` before it.

## 10. Deploy

### 10.1 Router facts

- Host: GL-MT6000, `root@192.168.8.1`, musl OpenWrt plus Entware glibc.
- Service: `/etc/init.d/cliproxyapi`, port `8319`.
- Start script: `/root/.cliproxyapi/run-protected.sh`, which execs the binary directly.
- Plugin directory: `/root/.cliproxyapi/plugins/linux/arm64/`.
- Panel asset: `/root/.cliproxyapi/static/management.html`.
- `cliproxyapi-v8030` on port `8330` is a different instance. Do not touch it unless asked.

### 10.2 Build the binaries

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC='zig cc -target aarch64-linux-gnu.2.27' \
  go build -o cli-proxy-api-linux-arm64 ./cmd/server

cd examples/plugin/ch-relay-vippro/go
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC='zig cc -target aarch64-linux-gnu.2.27' \
  go build -ldflags '-s -w' -buildmode=c-shared -o ch-relay-vippro.so .
rm -f ch-relay-vippro.h

cd ../../key-shop/go
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC='zig cc -target aarch64-linux-gnu.2.27' \
  go build -ldflags '-s -w' -buildmode=c-shared -o key-shop.so .
rm -f key-shop.h
```

Rules that break the deploy if ignored:

- Never pass `--dynamic-linker` to zig.
- Never build with `CGO_ENABLED=0`. A static binary cannot load plugins.
- If the ELF interpreter is `/lib/ld-linux-aarch64.so.1`, rewrite that exact string to
  `/opt/ld-linux-aarch64.so.1`. Verify with `readelf -l`. `LD_PRELOAD` must be unset at runtime.
- Plugin `.so` files must target GLIBC ABI <= 2.27. Rebuild the `.so` against the new host binary.
- Test locally before copying.

### 10.3 Panel

```bash
cd ../Cli-Proxy-API-Management-Center
bun run build            # dist/index.html, single file
```

The release artifact is `dist/index.html` renamed to `management.html`.

### 10.4 Replace files on the router

Back up first, with a date suffix:

```bash
ssh root@192.168.8.1 'cd /root/.cliproxyapi && \
  cp -p cli-proxy-api cli-proxy-api.bak-$(date +%Y%m%d) && \
  cp -p static/management.html static/management.html.bak-$(date +%Y%m%d) && \
  cp -p plugins/linux/arm64/ch-relay-vippro.so plugins/linux/arm64/ch-relay-vippro.so.bak-$(date +%Y%m%d)'
```

Copy only these files:

1. `/root/.cliproxyapi/cli-proxy-api`
2. `/root/.cliproxyapi/plugins/linux/arm64/key-shop.so`
3. `/root/.cliproxyapi/plugins/linux/arm64/ch-relay-vippro.so`
4. `/root/.cliproxyapi/static/management.html`

Never replace `config.yaml` or `auth/`. Never touch `cliproxyapi-v8030`.

Then restart and verify:

```bash
ssh root@192.168.8.1 '/etc/init.d/cliproxyapi restart'
ssh root@192.168.8.1 'logread | tail -50'      # or the service log file
```

Confirm in the log that `key-shop` and `ch-relay-vippro` loaded, and that the served
`management.html` size matches the artifact. A plugin that fails to load usually reports a symbol
or GLIBC error, not a config error.

### 10.5 Publishing the handoff repositories

```bash
# origin, as lantrinh1999 (the default gh account)
git push origin feat/model-mapping

# handoff, as 6c696e68
TOKEN=$(gh auth token --user 6c696e68)
AUTH=$(printf 'x-access-token:%s' "$TOKEN" | base64)
git -c http.extraheader="Authorization: Basic $AUTH" \
  push https://github.com/6c696e68/CLIProxyAPI-keyrelay.git HEAD:refs/heads/main
git -c http.extraheader="Authorization: Basic $AUTH" \
  push https://github.com/6c696e68/CLIProxyAPI-keyrelay.git HEAD:refs/heads/feat/model-mapping
```

The panel repository uses the same two commands against
`https://github.com/6c696e68/Cli-Proxy-API-Management-Center.git`.

A shallow clone cannot be pushed to a new remote: the pack lacks ancestors and the server rejects
it with `remote unpack failed: index-pack failed`. Run `git fetch --unshallow origin` first.

## 11. Panel

The panel is a React 19 + TypeScript + Vite single-page app. It talks only to `/v8/management`
(plus the plugin resource and declared plugin routes) with `Authorization: Bearer <management key>`.
The production artifact is a single `dist/index.html` with JS, CSS, and assets inlined.

Routes: `/dashboard`, `/ai-providers`, `/quick-start`, `/auth-files`, `/oauth`, `/model-mapping`,
`/quota`, `/plugins`, `/plugins/:pluginId`, `/plugin-store`, `/config`, `/logs`, `/system`.

Custom flows that must survive an upstream sync:

- Model Mapping tab reads and writes `/v8/management/routing/model-mapping`, with the grouped,
  searchable target select.
- Auth Files and Quota sort by plan: Plus/Pro before Free, filename-suffix fallback, unknown last.
  Put that sort on the current upstream cards, not on a copy of the old page.
- Plugin config editing stays in `PluginConfigSheet`, including `ch-relay-vippro` model editing and
  validation. Do not move the editor back into `PluginsPage`.
- Plugin menus open `/plugins/:pluginId` through `PluginResourcePage`. Do not mount `VipProPage` in
  `MainRoutes`. VipPro stats stay on the two `/v0/management/ch-relay-vippro` endpoints.
- New i18n keys go into all six locales: `en`, `vi`, `zh-CN`, `zh-TW`, `ru`, `ko`. Fallback is
  `zh-CN`.
- Restyle custom UI onto the current upstream layout; do not restore the old shell.

The panel's own handoff notes are in
`Cli-Proxy-API-Management-Center/docs/handoff.md`.

## 12. Upstream sync

1. Back up both repositories: a `feat/model-mapping-pre-sync-<date>` branch and a bundle under
   `/Volumes/SSD/Developments/fork/backups/`. Never skip this.
2. `git fetch upstream`, then rebase or merge `upstream/main` only.
3. Resolve conflicts by taking upstream infrastructure and keeping the custom decision. Rewrite a
   stale custom implementation; do not delete the flow.
4. Rebuild everything, including both `.so` files, against the new host.
5. Re-run the verification in [§9](#9-build-test-run), including the panel `bun run verify`.
6. Smoke-test on a disposable copy. Never point a smoke test at live `config.yaml` or `auth/`.
7. Push only when asked, to `origin` with `--force-with-lease` on `feat/model-mapping`. Never force
   `main`.

After a sync, re-read `AGENTS.md` in both repositories. Their "must survive" lists are the
authoritative conflict rule.

## 13. Operations and troubleshooting

| Symptom | Likely cause | Check |
| --- | --- | --- |
| Plugin missing from the panel | `.so` not loaded | Service log for the plugin id, file stem, architecture |
| `key_store_unavailable` | Database lock or a second admission in `intercept_after` | `busy_timeout`, plugin version |
| Every success bills 0 | Usage id mismatch | `usage.WithExecutionRequestID` on the handler |
| `401` for a key that should work | Auth rejects expired, disabled, or token-exhausted keys | Key status, expiry, `tokens_used` vs limit |
| `429` before upstream | Request cap reached | `request_limit`, `request_quota_used` |
| `403 model_not_allowed` | Global policy or group does not list the model | Group models, `model_access_mode` |
| `no_plus_account` | `plus-preference` is `prefer`/`only` and no Plus account is eligible | Codex auth plans, cooldowns |
| Quick tunnel URL changed | The cloudflared process restarted | Tunnel log, `KEYSHOP_TUNNEL_ID` |
| Panel shows an outdated backend error | v8 login 404 | Backend version, `legacyBackendProbe` |

Useful local checks:

```bash
go build -o test-output ./cmd/server && rm test-output
go test ./internal/api -run TestKeyShop -count=1
cd examples/plugin/key-shop/go && go test ./keyshop -count=1
```

Do not print secrets in logs or reports. key-shop stores only hashes; the partner and key secrets
are shown once.

## 14. Intentionally out of scope

- Prices, payment, RPM limits, bulk import, installer archives, and child keys in key-shop.
- Injecting mapped model names into `/v1/models`.
- Quota-limiting operator keys in `access.api-keys`.
- Realtime, live, and alpha-search for `ck_` keys: those paths select Codex OAuth without the
  interceptor.
- Per-executor model remaps.

## 15. Documentation map

| Topic | Document |
| --- | --- |
| Global model mapping | [model-mapping.md](model-mapping.md) |
| key-shop operations and design | [key-shop.md](key-shop.md) |
| key-shop install, API, tunnels | [examples/plugin/key-shop/README.md](../examples/plugin/key-shop/README.md) |
| Partner integrator contract | [examples/plugin/key-shop/docs/partner-api.md](../examples/plugin/key-shop/docs/partner-api.md) |
| ch-relay-vippro install and behaviour | [examples/plugin/ch-relay-vippro/README.md](../examples/plugin/ch-relay-vippro/README.md) |
| v8 management API | [management-api-v8.md](management-api-v8.md) |
| SDK access and embedding | [sdk-access.md](sdk-access.md), [sdk-advanced.md](sdk-advanced.md), [sdk-usage.md](sdk-usage.md) |
| Panel handoff | `../Cli-Proxy-API-Management-Center/docs/handoff.md` |
| Fork rules | `AGENTS.md` in both repositories |

## Tóm tắt tiếng Việt

- Hệ thống gồm 1 binary `cli-proxy-api`, 2 plugin (`key-shop`, `ch-relay-vippro`) và 1 file
  `management.html`. Chỉ thay đúng 4 file này khi deploy; không đụng `config.yaml` và `auth/`.
- Ba loại người dùng: operator (`/v8/management`, mật khẩu management), khách (`ck_`, `/v1` và
  `/client`), đối tác (`ksp_`, `/keyshop/partner/v1`).
- `routing.model-mapping` đổi tên model trước khi chọn provider; giữ hậu tố `(high)`; mặc định
  `force-mapping: true` nên client vẫn thấy tên cũ.
- `ch-relay-vippro` chuyển request sang relay upstream bằng token Codex lấy từ host; có
  `plus-preference`, `free-models`, `fallback`, cooldown theo model.
- `key-shop` cấp key `ck_`, có hạn dùng, hạn token, hạn request, nhóm model, trang khách `/client`,
  API bán key cho đối tác và Cloudflare tunnel.
- Build router: `CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC='zig cc -target aarch64-linux-gnu.2.27'`,
  không dùng `--dynamic-linker`, không build `CGO_ENABLED=0`.
- Đồng bộ upstream: backup trước, chỉ sync từ `upstream/main`, giữ các luồng ở mục 3, build lại cả
  hai `.so`, chạy lại toàn bộ test.
