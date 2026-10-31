# key-shop

Operator and design notes for the `key-shop` plugin. The install and API reference is [examples/plugin/key-shop/README.md](../examples/plugin/key-shop/README.md). The original design is [docs/superpowers/specs/2026-10-17-key-shop-design.md](superpowers/specs/2026-10-17-key-shop-design.md).

## Place in the fork

`key-shop` is a custom flow on `feat/model-mapping`. It must survive an upstream sync. The host alias is small and lives in `internal/api/key_shop_public.go`. Policy and SQLite live in the plugin so a later host upgrade does not require rewriting the product rules.

Do not copy code from `LingyeNBird/cpa2pool`. That repository is AGPL-3.0. The usage-statistics repositories only record requests after they finish. They cannot reject an expired or exhausted key before upstream.

## Request path

```text
ck_ secret
  -> frontend_auth.authenticate
  -> host keyshop.resolve_model for a grouped key (global YAML list is not consulted)
  -> request.intercept_before checks a limited group; the all-models group and ungrouped keys use the global model policy, then reserves tokens
  -> upstream
  -> usage.handle stages the token snapshot in usage_notes
  -> request.complete copies that note, bills, and releases the reservation
```

Model access is two layers. The global policy is plugin-wide, not per key. `model_access_mode: full` allows every model. `custom` allows the configured alias, or the upstream `name` when alias is empty. Matching is case-insensitive and keeps a client thinking suffix such as `(high)`. For a `ck_` key, the host checks this policy after global model mapping and before provider routing. An allowed alias is rewritten to its upstream name, with the thinking suffix preserved, before routing. A model outside the custom list returns HTTP 403 `model_not_allowed` before routing and before `Store.Admit()`, so it creates no token reservation. It still consumes one request-quota unit. A known customer key gets a rejected request row. Once the request limit is reached, later calls return HTTP 429. Operator keys do not enter this policy. For a `ck_` key, `/v1/models` (OpenAI, Claude, and Codex `client_version`) lists only the models the key's group or the custom YAML policy allows, and an alias is shown under the alias. Operator keys see the full list. Listing reserves no tokens and uses no request quota. `/v1beta/models` and Home-mode lists are not filtered.

A model group is a second layer. For a grouped `ck_` key the host calls `keyshop.resolve_model` before provider routing and does not consult the global YAML list. The match is the group's client-visible name: the per-group alias when set, otherwise the upstream name. A bare upstream name is not accepted when that entry has an alias. An allowed alias is rewritten to the upstream name, and a thinking suffix such as `(high)` is kept. `all_models` allows the call and leaves the model unchanged. An unknown group fails closed. A key with an empty `group_id` keeps the legacy global-policy behavior. An unknown key does too. The interceptor still checks the group as defense in depth and accepts either the alias or the upstream name. A group denial is the same HTTP 403 `model_not_allowed`: a rejected request row, no token reservation. Editing a group takes effect on the next request.

`request.complete` stores transport from the completion payload. `Stream` is true for HTTP SSE and for Responses websocket turns, and `Metadata.request_path` is `/v1/responses` for both. The payload has no websocket-only field, so a websocket turn is stored as `http` and carries no websocket transport label. The row is `websocket` only when metadata sets `websocket`.

`usage.handle` writes tokens, latency, effort, and account facts into `usage_notes`. The sweeper drops notes for terminal rows, and notes older than retention, but it leaves the note for a still-pending request. `Finalize` copies that note onto the request row and then deletes it. A note deleted between `usage.handle` and `request.complete` would leave zeros and bill nothing.

The Request log Account column is `auth_id`, the host `AuthID` for that call. It is the upstream auth id, not necessarily an email. The customer page does not show it.

Opening a database from the previous plugin only adds columns and tables: `keys.group_id` (default empty), request and usage-note columns, indexes, and `model_groups` (including the `all-models` seed). An existing `model_groups` table gains `aliases_json` (`TEXT NOT NULL DEFAULT '{}'`) and keeps `models_json` as an array of upstream names, so the previous plugin can still read the group. It does not rebuild or drop existing tables. The previous `key-shop.so` names its columns, so it still runs on a migrated database. Restoring that library does not require restoring the database.

`request.intercept_after` must stay a no-op for admission. The host calls it with the same request id. A second insert used to fail the SQLite primary key and return `key_store_unavailable`.

Auth runs first and cannot set an HTTP status. A key that is already expired, disabled, or exhausted by its token cap therefore returns 401. A key exhausted only by its request cap still authenticates so `request.intercept_before` can return HTTP 429 `request_limit_exceeded`. If both caps are exhausted, the request-limit error takes precedence. The interceptor returns 402 or 403 only when auth still saw remaining tokens and the reserve transaction then denies, including two requests racing for the last tokens.

Every recorded call consumes request quota, including failed and rejected attempts, so error spam cannot avoid the cap. Admitted requests consume it inside the admission transaction, before upstream completion. Rejected attempts consume it when the rejection row is written. Pending rows are finalized exactly once. The sweeper marks an expired pending request canceled and does not refund the request quota. Legacy databases keep `request_count` and initialize `request_quota_used` from that historical total so old calls are not granted back as free quota. Outcome counters are backfilled only from request rows still retained.

For a `ck_` key, listing follows the same group or custom policy: it shows the alias and reserves no tokens. Generation paths still enforce the gate.

## Host changes that must stay

- `GET /client` and `GET /client/api/session` rewrite onto the plugin resource and then abort the Gin context.
- `GET /v0/resource/plugins/key-shop/dashboard` without `X-Key-Shop-Alias: 1` redirects to `/client` before the plugin is called.
- `GET /keyshop/partner/v1/health` and `/plans`, `POST /keyshop/partner/v1/{mint,confirm,void,status,refs}` rewrite to `/v0/management/plugins/key-shop/partner/v1/<op>` and call `pluginHost.ServeManagementHTTP` directly, without management auth. Inbound `X-Key-Shop-Partner*` headers are stripped, `X-Key-Shop-Partner: 1` and `X-Key-Shop-Client-IP` are set, and every other `/keyshop/` path is 404.
- `NewUsageReporter` uses the lifecycle request id from `usage.WithExecutionRequestID` when the handler set it. Without that, usage and completion cannot share an id, and every success bills 0.

These are recorded in `AGENTS.md`. Do not drop them when taking upstream infrastructure.

## Partner API

The public partner API lives at `/keyshop/partner/v1`: `GET health`, `GET plans`, and `POST mint|confirm|void|status|refs`. The plugin must keep those routes in `management.register`. A partner authenticates with `Authorization: Bearer ksp_...`; only the SHA-256 of the secret is stored, and the secret is returned once on create or rotate. `mint` is idempotent by `ref`, creates `ck_` keys from a plan, and is capped per day. `confirm` and `void` are bookkeeping: a minted key is active immediately. Plans and partners are managed under `/v0/management/plugins/key-shop/{plans,partners}...`; the admin UI edits the partner config keys. Money stays outside key-shop. Integrator contract: [examples/plugin/key-shop/docs/partner-api.md](../examples/plugin/key-shop/docs/partner-api.md). Design: [docs/superpowers/specs/2026-10-30-key-shop-partner-sales-design.md](superpowers/specs/2026-10-30-key-shop-partner-sales-design.md).

## What is intentionally out of scope

Prices, RPM limits, bulk import, payment, installer archives, child keys, and injecting mapped names into `/v1/models`. Operator keys in `access.api-keys` stay unlimited. Realtime, live, and alpha-search paths are unsupported for `ck_` keys because they select Codex OAuth without the interceptor.

## Tunnels

The admin **Tunnels** tab manages Cloudflare tunnels created by this plugin. It does not discover or signal tunnels that belong to the device.

Config keys, all optional:

- `cloudflared_path`: binary path. Empty looks up `cloudflared` on `PATH`. A missing binary is not a startup error. Start returns HTTP 409 `cloudflared not found`.
- `tunnels_enabled`: default true. `false` makes the tunnel routes return 404, no child is spawned and an owned live pid is stopped.
- `tunnel_keep_alive`: default true. `false` restores the old behavior where a plugin reconfigure, a plugin reload or a host restart ends every child. The flag takes effect at the next plugin reload or CPA restart.
- `tunnel_watch_interval`: Go duration, default `2s`. How often a kept tunnel is checked for a dead child and its log is read for a new quick URL. An invalid or non-positive value keeps the default.
- `tunnel_log_max_kb`: integer, default `1024` (1 MiB). Cap for `cloudflared.log` in KiB. A non-positive value keeps the default. The watcher truncates the file to 0 when it grows past the cap; the child writes through an O_APPEND fd, so its next line lands at offset 0.

`cloudflared` is required only to run a tunnel. The plugin does not call the Cloudflare API or read `~/.cloudflared`. With keep-alive on Linux it starts the child in a transient systemd scope (`systemd-run --scope --unit=keyshop-tunnel-<id>-<random>`), not an installed system service. `systemctl stop 'keyshop-tunnel-*.scope'` ends every kept scope.

Keep-alive is on by default and needs Linux, where a recorded pid can be checked. A config save, a plugin reload and a `systemctl restart` leave the same cloudflared process and the same quick URL running: the next manager adopts the recorded pid instead of spawning a second child, watches it, and restarts it when it dies. Only Stop, Delete, an update of a running tunnel, `tunnels_enabled: false` and `tunnel_keep_alive: false` end a tunnel.

Surviving a CPA restart needs Linux, root and systemd, because only the transient scope detaches the child from the service control group. Without root or systemd the plugin starts the child again after the restart, and a quick URL changes. A quick URL changes only when the cloudflared process itself restarts: a crash, a reboot, a stop/start or an update of a running tunnel. Limitation: a tunnel that is dead or in backoff during a plugin hot reload can get a second, unrecorded child; stop it with `systemctl stop 'keyshop-tunnel-<id>-*.scope'`. Stop tunnels before removing the plugin.

Isolation:

- The plugin signals only a process it spawned, or a recorded pid whose `/proc/<pid>/environ` contains `KEYSHOP_TUNNEL_ID=<id>` on Linux, re-checked immediately before each signal. On a keep-alive reload it adopts that pid and watches it instead of spawning a duplicate. Other operating systems clear the recorded pid and signal nothing.
- Child environment is only `PATH`, `KEYSHOP_TUNNEL_ID`, `HOME=<data dir>/tunnels/<id>`, and `TUNNEL_TOKEN` for a token tunnel. The token is never placed on argv, in the systemd unit name, in API JSON, or in the logs returned by the API.
- Child stdout and stderr go to `<data dir>/tunnels/<id>/cloudflared.log` (0600, O_APPEND). The watcher truncates it to 0 when it grows past `tunnel_log_max_kb`, and the Logs route returns the tail with the token redacted, including for an adopted child and after a reload. The in-memory ring is gone.
- Delete stops that child, removes only `<data dir>/tunnels/<id>`, then removes the row.

Routes under `/v0/management/plugins/key-shop`:

| Action | Method | Path | Body or query |
| --- | --- | --- | --- |
| List | GET | `/tunnels` | `{tunnels, cloudflared:{found,path,version}}` |
| Create | POST | `/tunnels` | `{name,kind,target_url,token,autostart}` |
| Update | POST | `/tunnels/update` | `{id,name?,target_url?,token?,autostart?}`. Empty `token` keeps the stored token. Kind does not change. A running tunnel restarts. |
| Delete | POST | `/tunnels/delete` | `{id}` → `{ok:true}` |
| Start | POST | `/tunnels/start` | `{id}` |
| Stop | POST | `/tunnels/stop` | `{id}` |
| Logs | GET | `/tunnels/logs` | `id`, `lines` (default 200) → `{lines}` |

`kind` is `token` (named tunnel, token required on create) or `quick` (`--url`, target required). Responses include `has_token` and `token_hint` (last 4 characters) and never the token. Status is `running`, `starting`, `stopped`, or `failed`.

Creating a key can also ask for a quick tunnel. `POST /keys` and `POST /keys/bulk` accept optional `tunnel: {enabled, target_url}`. When `enabled` is false or the field is absent, the response is unchanged. When enabled, one quick tunnel is created after the keys (one shared tunnel for a bulk batch). The response keeps `secret` or `keys` and adds `tunnel: {id, name, status, public_url, endpoint, error}`. `endpoint` is the public URL plus `/v1`. A tunnel failure does not roll back the keys: the HTTP status stays 200 and `tunnel.error` says why. Optional config `tunnel_target_url` is the default target. If that and the request target are empty, the port comes from the request `Host`, or `8317` when the host has no port. A quick URL changes only when the cloudflared process itself restarts.

## Port 8330 note

The GL-MT6000 service on port `8319` is a different process from `8330`. `8330` is `/root/.cliproxyapi-v8030`. Installing key-shop there requires that instance's binary, its `key-shop.so`, and a `plugins.configs.key-shop` block. Do not copy those files onto `8319` unless that deploy was requested. Never replace `config.yaml` or `auth/` as part of a binary copy. Add only the plugin config keys, with a backup first.
