# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Fork rule

This checkout is the custom fork. Before any upstream sync, panel change, plugin change, or router deploy, follow this section. Do not drop a custom flow because upstream moved.

- Branch: `feat/model-mapping`. Sync source is `upstream/main` only. Do not merge old `origin/feat/model-mapping` history.
- Push only when asked, to `origin` (`lantrinh1999/CLIProxyAPI` and `lantrinh1999/Cli-Proxy-API-Management-Center`). Git HTTPS may be logged in as `6c696e68`; use the `lantrinh1999` GitHub account. After a rebase, `--force-with-lease` that feature branch only. Never force `main`.
- Panel repo: `../Cli-Proxy-API-Management-Center`, same branch. Keep both repos in step.
- Backup before sync or deploy: branch `feat/model-mapping-pre-sync-<date>` plus a bundle under `/Volumes/SSD/Developments/fork/backups/`. Never touch live `~/.cliproxyapi/config.yaml` or `auth/` except a disposable copy for smoke.

Custom flows that must survive:

- Global model mapping runs before provider routing. First enabled rule wins. Thinking suffixes stay attached. Default `force-mapping` echoes the client model. v8 key is `routing.model-mapping`. No `/v0/management` route, no `/v1/models` injection, no per-executor remap.
- `disable-subagents` rejects spawned Codex threads, then strips spawn tool namespaces, on HTTP Responses and the Responses websocket.
- Websocket auth, passthrough, and compaction use `StreamRouteModel`, not the raw client model.
- `ch-relay` and `ch-relay-vippro` forward through host relay callbacks. The plugin does not receive or refresh Codex tokens.
- VipPro management stays `GET /v0/management/ch-relay-vippro/stats` and `POST /v0/management/ch-relay-vippro/accounts/check`. The dashboard is a plugin resource iframe, not a public status route.
- Request paths read config with `CurrentConfig()`. Management writes hold `h.mu` and call `persistLocked`. Payload rules stay the last semantic barrier in executors.
- `key-shop` issues `ck_` client keys with expiry and token caps. Public pages are `GET /key-dashboard` and `GET /key-dashboard/api/session`, aliased onto the plugin resource. A direct dashboard resource URL redirects there. `request.intercept_after` must not reserve again. Usage records must keep the lifecycle request id. Docs: `examples/plugin/key-shop/README.md` and `docs/key-shop.md`. Do not drop the alias when syncing upstream.

Panel flows that must survive:

- Model Mapping tab reads and writes `/v8/management/routing/model-mapping`, with the grouped searchable target select.
- Auth Files and Quota keep sort-by-plan: Plus/Pro before Free, filename-suffix fallback, unknown last. Put that sort on the current upstream cards, not a copy of the old page.
- Plugin config editing stays in `PluginConfigSheet`, including `ch-relay-vippro` model editing and validation. Do not move the editor back into `PluginsPage`.
- Plugin menus open `/plugins/:pluginId` (`PluginResourcePage` iframe). Do not mount `VipProPage` in `MainRoutes`.
- Restyle custom UI onto the current upstream layout. Do not restore the old shell. Add new i18n keys to all six locales.

Conflict rule: take upstream infrastructure (keepalive, ETag, shadow-copy plugin loading, `CurrentConfig`, `persistLocked`, new quota cards). Keep the custom decision. If the custom implementation is outdated, rewrite it and keep the flow. Do not edit `/v0/management` except to compile.

Router, only when asked to deploy:

- GL-MT6000 `root@192.168.8.1`, service `/etc/init.d/cliproxyapi`, port `8319`, start script `/root/.cliproxyapi/run-protected.sh`. That script execs the binary directly. Build `CGO_ENABLED=1 GOOS=linux GOARCH=arm64` with `CC='zig cc -target aarch64-linux-gnu.2.27'`. Do not pass `--dynamic-linker` to zig. If the ELF interpreter is `/lib/ld-linux-aarch64.so.1`, rewrite that exact string to `/opt/ld-linux-aarch64.so.1`. `LD_PRELOAD` must be unset. Do not ship `CGO_ENABLED=0`.
- Replace only `/root/.cliproxyapi/cli-proxy-api`, `/root/.cliproxyapi/plugins/linux/arm64/ch-relay-vippro.so`, and `/root/.cliproxyapi/static/management.html`. Date-suffix backups first. Never replace `config.yaml` or auth. Do not touch `cliproxyapi-v8030`. `disable-auto-update-panel` is already true; do not change other config keys.
- Rebuild the `.so` against the new host. Restart the init script. Confirm the log says `ch-relay-vippro` loaded and `management.html` size matches the artifact. Test locally before any router copy.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)
- `model-mapping` is the global request rewrite layer. See `docs/model-mapping.md`. Do not confuse it with `oauth-model-alias` or per-credential `models[].alias`.

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/modelmapping/` — Global client-model rewrite applied **before** provider routing. First enabled rule wins (`exact` / `wildcard` / `regex`). Thinking suffixes such as `(high)` are preserved. Default `force-mapping: true` echoes the original client model in responses. Hook: `sdk/api/handlers/handlers_model_mapping.go` from execution, stream, count, and `PrepareStreamModelRoute`. v8 YAML is `routing.model-mapping` (legacy root `model-mapping` still loads). Management: `/v8/management/routing/model-mapping`. TUI tab: Model Mapping. This is **not** `oauth-model-alias` and **not** `*-api-key.models[].alias`.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/home/` — CLIProxyAPIHome control plane integration (bootstrap, RESP communication, dispatch coordination)
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Payload configuration MUST be the final semantic barrier before sending requests in every executor, including streaming, WebSocket, continuation, retry/fallback, image, and token-count paths where applicable. Complete all built-in payload translation, normalization, injection, and cleanup first, then evaluate and apply user payload rules exactly once to the final business payload. No subsequent logic may overwrite configured values or restore filtered fields. Only necessary transport framing, serialization, signature calculation, and read-only validation may follow without changing business payload semantics. Add regression tests when introducing or changing request-building paths to enforce this ordering.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- Note: if modifying features that involve CLIProxyAPIHome, check if corresponding updates are needed in the CLIProxyAPIHome repository.
- Endpoints under the `/v0/management` base URL are deprecated and no longer maintained. For any feature changes, do not modify endpoints under `/v0/management` unless necessary to fix compilation errors.
- When a client asks for a retired or hardcoded model name (for example Zeroclaw sending `gpt-5.4`), use `model-mapping`, not `oauth-model-alias`. Keep rewrite in `internal/modelmapping` + the handler hook; do not scatter per-executor remaps. Do not add mapped names to `/v1/models` unless explicitly requested. Web control-panel tab lives in `router-for-me/Cli-Proxy-API-Management-Center`, not this repo.
