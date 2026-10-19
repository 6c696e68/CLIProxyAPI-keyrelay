# key-shop

`key-shop` issues customer API keys for one CLIProxyAPI process. Each key has an expiry and a token cap. The operator creates keys in the management panel. A customer opens `/client`, pastes only their own key, and sees usage.

The plugin id, shared-library stem, and `plugins.configs` key are all `key-shop`. Secrets use the prefix `ck_`. They are not written into `config.yaml` or `auth/`.

Existing `access.api-keys` keep working and are not quota-limited. Do not enable exclusive frontend auth. That would disable the operator keys.

## What a customer uses

- Base URL: `http://<host>:<port>/v1`
- API key: the `ck_...` secret shown once at create or rotate
- Dashboard: `http://<host>:<port>/client`

Send the key in `Authorization: Bearer <key>`, `X-Api-Key`, or `X-Goog-Api-Key`. Do not put the key in the URL. Query `key` and `auth_token` are ignored.

Codex and other OpenAI-compatible clients point at `/v1` and use the customer key. The proxy still uses the operator's upstream credentials. The customer key only decides whether the request is allowed and how it is billed.

## Defaults

| Setting | Default | Meaning of `0` / empty |
| --- | --- | --- |
| Expiry | 7 days (`168h`) | `never_expires: true` means no expiry |
| Token cap | `100000000` | `unlimited_tokens: true` stores `0` and never exhausts |
| Request cap | unlimited (`0`) | `unlimited_requests: true` or `request_limit: 0` never exhausts |
| Reservation | `32768` tokens | Held until usage arrives or 15 minutes pass |
| Request log retention | 30 days | Does not reduce `tokens_used` |

Billed tokens are `input_tokens + output_tokens`. Reasoning and cache are shown and are not added again. A failed, rejected, or canceled request is logged and bills `0`.

Every recorded call consumes one request-quota unit, including successful, failed, canceled, and rejected attempts. This stops a key from avoiding the cap by sending failing or disallowed calls. Raising `request_limit` allows more calls without resetting the counters. A request-limit denial is HTTP `429` with code `request_limit_exceeded`. Token limits stay separate.

## Enable

`plugins.enabled` is false in `config.example.yaml`. Turn plugins on in the config you run.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    key-shop:
      enabled: true
      priority: 100
      data_file: ""
      default_ttl: 168h
      default_token_limit: 100000000
      default_request_limit: 0
      reserve_tokens: 32768
      reservation_ttl: 15m
      retention_days: 30
      deleted_retention_days: 90
      model_access_mode: full # full or custom
      models:
        - name: gpt-5-codex
          alias: codex-latest
```

Empty `data_file` uses `~/.cliproxyapi/key-shop/key-shop.db`. On a router, set an absolute path such as `/root/.cliproxyapi-v8030/key-shop/key-shop.db`. The process home may be `/`, so the default path is wrong there. The database file is mode `0600`. The parent directory is mode `0700`.

Restart the host after adding the `.so`. A config reload is not enough for a newly copied library.

## Build

From `examples/plugin/key-shop/go`:

```bash
go test ./keyshop -count=1
```

macOS:

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o key-shop.dylib .
rm -f key-shop.h
```

Linux arm64 for a glibc 2.27 host:

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC='zig cc -target aarch64-linux-gnu.2.27' \
  go build -ldflags '-s -w' -buildmode=c-shared -o key-shop.so .
rm -f key-shop.h
```

If the ELF interpreter is `/lib/ld-linux-aarch64.so.1` and the host uses `/opt/ld-linux-aarch64.so.1`, rewrite that exact string before copying. Do not pass `--dynamic-linker` to zig. Do not build with `CGO_ENABLED=0`.

Copy the library to `plugins/<os>/<arch>/key-shop.so`. The file stem must stay `key-shop`.

The public alias `/client` lives in the host, not only in the plugin. A binary built before that alias will still load the plugin, but customers will only have `/v0/resource/plugins/key-shop/dashboard`. Rebuild the server when you need the short URL.

## Operator UI

Open the management panel and the plugin menu **API Keys**. The page is also at:

`GET /v0/resource/plugins/key-shop/admin`

It uses the management password already stored by the panel when that password was saved in the same browser. Otherwise paste the management login password, not a customer `ck_` key and not the bcrypt hash from `config.yaml`.

The **Customer model access** section controls the global policy for all `ck_` keys. `full` allows every model. `custom` uses the model list below it; clients may call the configured alias, or the upstream `name` when no alias is set. Thinking suffixes such as `(high)` remain valid. The embedded page and the management panel edit the same `plugins.configs.key-shop` values. That policy is not a model group.

The admin page has four screens: Dashboard (`#/`), Model groups (`#/groups`), Keys (`#/keys`), and Request log (`#/requests`).

A model group is `{id, name, description, all_models, models[]}`. Each model is `{name, alias}`. `name` is the upstream model, such as `vippro/gpt-6-sol`. `alias` is optional and is the name the client sends. Create and update also accept a legacy `string[]` of upstream names. Responses always return `{name, alias}`. Names and aliases must be unique in the group, ignoring case. An alias must not equal a different model's name. The built-in group `all-models` (`is_system`, `all_models: true`) always exists and cannot be edited or deleted. A specific-model group needs at least one model. A group that keys still use cannot be deleted (HTTP 409 with `key_count`). Editing a group takes effect on the next request.

A key with an empty `group_id` keeps the legacy global-policy behavior. New keys, including bulk create, require `group_id`. Update and bulk update can move a key to another group but cannot clear `group_id`.

Create accepts an empty name and generates a stable label such as `key-7f4a2c10`. A single key keeps the label exactly as entered. Bulk creation adds a short unique suffix to an entered label, such as `CUSTOMER-a3f2` and `CUSTOMER-7c91`. A later disable, enable, expiry, or secret rotation does not change the key ID or saved label.

| Action | Method | Path | Body |
| --- | --- | --- | --- |
| Create | POST | `/v0/management/plugins/key-shop/keys` | `group_id` (required) plus `label`, `customer`, `note`, `ttl_seconds`, `never_expires`, `token_limit`, `unlimited_tokens`, `request_limit`, `unlimited_requests` |
| Bulk create | POST | `/v0/management/plugins/key-shop/keys/bulk` | Same fields plus `count` (1–500); `group_id` is required; returns each secret once |
| List | GET | `/v0/management/plugins/key-shop/keys` | query `q`, `status`, `page`, `page_size`, `group` (a group id, or `none` for legacy keys with an empty group) |
| Update | POST | `/v0/management/plugins/key-shop/keys/update` | `id` plus only the fields to change. Optional `group_id` cannot be cleared. |
| Bulk update | POST | `/v0/management/plugins/key-shop/keys/bulk-update` | `ids` (1–500) plus only the fields present: `group_id`, `status` (`active` or `disabled`), `customer`, `note`, `ttl_seconds`, `never_expires`, `token_limit`, `unlimited_tokens`, `request_limit`, `unlimited_requests`. Returns `{updated: n}`. One unknown or deleted id returns 404 and changes nothing. |
| Rotate | POST | `/v0/management/plugins/key-shop/keys/rotate` | `id` |
| Reset usage | POST | `/v0/management/plugins/key-shop/keys/reset-usage` | `id` |
| Delete | POST | `/v0/management/plugins/key-shop/keys/delete` | `id` |
| Bulk delete | POST | `/v0/management/plugins/key-shop/keys/bulk-delete` | `ids` (1–500). Returns `{deleted: n}`. One unknown or deleted id returns 404 and deletes nothing. |
| List groups | GET | `/v0/management/plugins/key-shop/groups` | none. `{groups}` ordered system first, then by name. |
| Create group | POST | `/v0/management/plugins/key-shop/groups` | `name`, optional `description`, `all_models`, `models`. Returns `{group}`. |
| Update group | POST | `/v0/management/plugins/key-shop/groups/update` | `id`, `name`, optional `description`, `all_models`, `models` (full replace). Returns `{group}`. |
| Delete group | POST | `/v0/management/plugins/key-shop/groups/delete` | `id`. `{deleted: true}`. HTTP 409 `{error, key_count}` when keys still use it. HTTP 403 for `all-models`. |
| Requests | GET | `/v0/management/plugins/key-shop/requests` | query `id`, `customer`, `group`, `status`, `model`, `account`, `effort`, `code`, `q`, `from`, `to` (RFC3339), `limit` (default 25, max 100), `cursor`. `total` and `summary` cover the whole filter, not only the page. |
| Stats | GET | `/v0/management/plugins/key-shop/stats` | none |
| Usage | GET | `/v0/management/plugins/key-shop/stats/usage` | query `from`, `to` (RFC3339), `bucket` (`hour` or `day`). Omitted range is the last 24 hours. Omitted `bucket` is `day`. |

Omitted `ttl_seconds` means 7 days. `never_expires: true` ignores `ttl_seconds`. Omitted `token_limit` means 100000000. `unlimited_tokens: true` ignores `token_limit`. Omitted `request_limit` uses `default_request_limit`, which is unlimited. `unlimited_requests: true` or an explicit `0` stores no request cap. Update `status` accepts only `active` or `disabled`. Reset usage clears billed tokens only; it does not restore request quota.

Create and rotate return `secret` once. List responses contain `display` such as `ck_abcd…wxyz` and `GroupID` (empty for a legacy key). They never contain `secret` or `secret_hash`.

Group and bulk errors are JSON `{"error": "message"}`. Validation failures are HTTP 400 (`group_id is required`, `unknown model group`, `name must be 1-80 characters`, `choose at least one model`, `name already exists`, `names and aliases must be unique within the group`). Missing `group_id` on create is 400 before a key is written.

These routes require the management secret because they sit under `/v0/management`. Paths cannot contain `:`.

## Customer dashboard

`GET /client` serves the page. `GET /client/api/session` returns that key's lifecycle, limits, and a bounded request page. Send `Authorization: Bearer ck_...`; use `limit` and `cursor` to load more.

A direct `GET /v0/resource/plugins/key-shop/dashboard` redirects to `/client`. Unknown paths such as `/client/missing` return 404.

The session JSON includes `base_url`, which is the request host plus `/v1`, and `total`, the number of stored requests for that key. It also includes `group` (`{id, name, all_models}`, or `null` when the key has no group) and `model_access_mode`. `models` is the list the key may call. For a limited group that list is the group's client-visible names (alias if set, otherwise the upstream name). `model_access_mode` is `full` only when neither the global policy nor the group limits the key.

## Enforcement

Auth accepts a `ck_` key only when it is active, unexpired, and has remaining tokens after open reservations. Expired, disabled, and exhausted keys are not accepted. The plugin auth adapter cannot set an HTTP status, so those clients see 401.

These paths do not pass the request interceptor, so every `ck_` key is rejected there:

- `/v1/live` and `/v1/live/*`
- `/v1/realtime` and `/v1/realtime/*`
- `/v1/alpha/search`
- `/backend-api/codex/alpha/search`

`request.intercept_before` is the second gate for chat, responses, streaming, count-tokens, and websocket turns that reach `ExecuteStreamWithAuthManager`. It returns:

- `402` `insufficient_quota` / `token_limit_exceeded`
- `403` `invalid_request_error` / `key_expired`, `key_disabled`, or `model_not_allowed`
- `429` `insufficient_quota` / `request_limit_exceeded`

`model_not_allowed` is returned before provider routing and before admission. It does not reserve tokens, but it does consume one request-quota unit. A known `ck_` key gets a rejected request row with that reason. Once the request limit is reached, later calls return `429` instead. The global check uses the client model, including a thinking suffix, and applies only to `ck_` keys. An allowed alias is rewritten to the configured upstream name before routing; `codex-latest(high)` becomes `gpt-5-codex(high)` when that alias is configured.

A grouped `ck_` key is resolved by `keyshop.resolve_model` before provider routing. The host rewrites a per-group alias to the upstream name and does not consult the global YAML list for that key. The interceptor still checks the group and accepts either the alias or the upstream name. See [docs/key-shop.md](../../../docs/key-shop.md).

Count-token paths and `generate=false` use the same denial. An allowed non-billable request reserves `0`.

`request.intercept_after` does not reserve again. Reserving twice on the same request id used to return `key_store_unavailable`.

Admission is one SQLite immediate transaction. It reserves `min(max_tokens, reserve_tokens, remaining)`. If `max_tokens` is absent, it reserves `reserve_tokens`, capped by remaining. A limit of `0` reserves `0` and is never exhausted.

Completion is idempotent on `request_id`. Success bills input plus output. Failure bills `0`. A reservation older than `reservation_ttl` is deleted without billing, so a crash can underbill. It must not overbill.

## Storage

SQLite through `modernc.org/sqlite`, WAL, `busy_timeout=5000`. Only the SHA-256 of the secret is stored, plus a 4-character prefix and the last 4 characters.

Tables:

- `keys`: identity, label, customer, note, status, expiry, limit, used tokens, request count, `group_id`
- `model_groups`: id, name, description, `all_models`, `models_json` (upstream names), `aliases_json` (name to alias), `is_system`, timestamps
- `reservations`: open holds keyed by request id
- `requests`: model, requested model, transport, status, latency, token counts, billed tokens, upstream account fields
- `usage_notes`: token snapshot received before completion, plus account and effort fields copied at finalize

Stored status is `active` or `disabled`. Computed status adds `expired` and `exhausted`. Request status is `pending`, `succeeded`, `failed`, `rejected`, or `canceled`.

## Tests

```bash
cd examples/plugin/key-shop/go
go test ./keyshop -count=1
```

From the repository root:

```bash
go test ./internal/api -run TestKeyShop -count=1
go test ./internal/runtime/executor/helps -run TestUsageReporterUsesExecutionRequestID -count=1
go build -o /tmp/cli-proxy-api ./cmd/server
```

## Hướng dẫn ngắn

- Khách dùng `http://<host>:<port>/v1` và key `ck_...`.
- Trang khách: `http://<host>:<port>/client`.
- Tạo key trong panel, menu **API Keys**, bằng mật khẩu đăng nhập management. Không dán hash bcrypt và không dán key khách.
- Không nhập tên vẫn tạo được. Sửa hạn hoặc tắt key không xóa tên đã lưu.
- Hết hạn hoặc hết token thì request bị chặn trước upstream. Request lỗi không trừ token.

Legacy `/key-dashboard` redirects to `/client`; the legacy session URL remains supported.
