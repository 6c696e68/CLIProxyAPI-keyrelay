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

Model access is two layers. The global policy is plugin-wide, not per key. `model_access_mode: full` allows every model. `custom` allows the configured alias, or the upstream `name` when alias is empty. Matching is case-insensitive and keeps a client thinking suffix such as `(high)`. For a `ck_` key, the host checks this policy after global model mapping and before provider routing. An allowed alias is rewritten to its upstream name, with the thinking suffix preserved, before routing. A model outside the custom list returns HTTP 403 `model_not_allowed` before routing and before `Store.Admit()`, so it creates no token reservation. It still consumes one request-quota unit. A known customer key gets a rejected request row. Once the request limit is reached, later calls return HTTP 429. Operator keys do not enter this policy. `/v1/models` does not run the interceptor.

A model group is a second layer. For a grouped `ck_` key the host calls `keyshop.resolve_model` before provider routing and does not consult the global YAML list. The match is the group's client-visible name: the per-group alias when set, otherwise the upstream name. A bare upstream name is not accepted when that entry has an alias. An allowed alias is rewritten to the upstream name, and a thinking suffix such as `(high)` is kept. `all_models` allows the call and leaves the model unchanged. An unknown group fails closed. A key with an empty `group_id` keeps the legacy global-policy behavior. An unknown key does too. The interceptor still checks the group as defense in depth and accepts either the alias or the upstream name. A group denial is the same HTTP 403 `model_not_allowed`: a rejected request row, no token reservation. Editing a group takes effect on the next request.

`request.complete` stores transport from the completion payload. `Stream` is true for HTTP SSE and for Responses websocket turns, and `Metadata.request_path` is `/v1/responses` for both. The payload has no websocket-only field, so a websocket turn is stored as `http` and carries no websocket transport label. The row is `websocket` only when metadata sets `websocket`.

`usage.handle` writes tokens, latency, effort, and account facts into `usage_notes`. The sweeper drops notes for terminal rows, and notes older than retention, but it leaves the note for a still-pending request. `Finalize` copies that note onto the request row and then deletes it. A note deleted between `usage.handle` and `request.complete` would leave zeros and bill nothing.

The Request log Account column is `auth_id`, the host `AuthID` for that call. It is the upstream auth id, not necessarily an email. The customer page does not show it.

Opening a database from the previous plugin only adds columns and tables: `keys.group_id` (default empty), request and usage-note columns, indexes, and `model_groups` (including the `all-models` seed). An existing `model_groups` table gains `aliases_json` (`TEXT NOT NULL DEFAULT '{}'`) and keeps `models_json` as an array of upstream names, so the previous plugin can still read the group. It does not rebuild or drop existing tables. The previous `key-shop.so` names its columns, so it still runs on a migrated database. Restoring that library does not require restoring the database.

`request.intercept_after` must stay a no-op for admission. The host calls it with the same request id. A second insert used to fail the SQLite primary key and return `key_store_unavailable`.

Auth runs first and cannot set an HTTP status. A key that is already expired, disabled, or exhausted by its token cap therefore returns 401. A key exhausted only by its request cap still authenticates so `request.intercept_before` can return HTTP 429 `request_limit_exceeded`. If both caps are exhausted, the request-limit error takes precedence. The interceptor returns 402 or 403 only when auth still saw remaining tokens and the reserve transaction then denies, including two requests racing for the last tokens.

Every recorded call consumes request quota, including failed and rejected attempts, so error spam cannot avoid the cap. Admitted requests consume it inside the admission transaction, before upstream completion. Rejected attempts consume it when the rejection row is written. Pending rows are finalized exactly once. The sweeper marks an expired pending request canceled and does not refund the request quota. Legacy databases keep `request_count` and initialize `request_quota_used` from that historical total so old calls are not granted back as free quota. Outcome counters are backfilled only from request rows still retained.

`/v1/models` does not run the interceptor. Listing models does not reserve tokens. Generation paths do.

## Host changes that must stay

- `GET /client` and `GET /client/api/session` rewrite onto the plugin resource and then abort the Gin context.
- `GET /v0/resource/plugins/key-shop/dashboard` without `X-Key-Shop-Alias: 1` redirects to `/client` before the plugin is called.
- `NewUsageReporter` uses the lifecycle request id from `usage.WithExecutionRequestID` when the handler set it. Without that, usage and completion cannot share an id, and every success bills 0.

These are recorded in `AGENTS.md`. Do not drop them when taking upstream infrastructure.

## What is intentionally out of scope

Prices, RPM limits, bulk import, payment, installer archives, child keys, and injecting mapped names into `/v1/models`. Operator keys in `access.api-keys` stay unlimited. Realtime, live, and alpha-search paths are unsupported for `ck_` keys because they select Codex OAuth without the interceptor.

## Tunnels

The admin **Tunnels** tab manages Cloudflare tunnels created by this plugin. It does not discover or signal tunnels that belong to the device.

Config keys, both optional:

- `cloudflared_path`: binary path. Empty looks up `cloudflared` on `PATH`. A missing binary is not a startup error. Start returns HTTP 409 `cloudflared not found`.
- `tunnels_enabled`: default true. `false` makes the tunnel routes return 404 and no child is spawned.

`cloudflared` is required only to run a tunnel. The plugin does not call the Cloudflare API, install a system service, or read `~/.cloudflared`.

Isolation:

- The plugin signals only a process it spawned, or a recorded pid whose `/proc/<pid>/environ` contains `KEYSHOP_TUNNEL_ID=<id>` on Linux. Other operating systems clear the recorded pid and signal nothing.
- Child environment is only `PATH`, `KEYSHOP_TUNNEL_ID`, `HOME=<data dir>/tunnels/<id>`, and `TUNNEL_TOKEN` for a token tunnel. The token is never placed on argv, in API JSON, or in logs.
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

Creating a key can also ask for a quick tunnel. `POST /keys` and `POST /keys/bulk` accept optional `tunnel: {enabled, target_url}`. When `enabled` is false or the field is absent, the response is unchanged. When enabled, one quick tunnel is created after the keys (one shared tunnel for a bulk batch). The response keeps `secret` or `keys` and adds `tunnel: {id, name, status, public_url, endpoint, error}`. `endpoint` is the public URL plus `/v1`. A tunnel failure does not roll back the keys: the HTTP status stays 200 and `tunnel.error` says why. Optional config `tunnel_target_url` is the default target. If that and the request target are empty, the port comes from the request `Host`, or `8317` when the host has no port. A quick URL changes when the tunnel restarts.

## Port 8330 note

The GL-MT6000 service on port `8319` is a different process from `8330`. `8330` is `/root/.cliproxyapi-v8030`. Installing key-shop there requires that instance's binary, its `key-shop.so`, and a `plugins.configs.key-shop` block. Do not copy those files onto `8319` unless that deploy was requested. Never replace `config.yaml` or `auth/` as part of a binary copy. Add only the plugin config keys, with a backup first.
