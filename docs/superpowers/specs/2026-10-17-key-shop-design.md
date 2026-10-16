# Key Shop Design

Date: 2026-10-17
Status: implemented on feat/key-shop
Plugin ID: `key-shop`
Public URL: `http://<host>/key-dashboard`

## Goal

Sell many client API keys on one CLIProxyAPI instance. Each key has an expiry and a token cap. The operator manages keys in the existing plugin menu. A customer opens a short public page, pastes only their own key, and sees usage. The plugin must reject an expired, disabled, or exhausted key before any upstream model call.

## Non-goals

- Do not copy code or text from `LingyeNBird/cpa2pool`. That repository is AGPL-3.0. Use only the idea of a quota gate.
- Do not add prices, RPM, bulk import, payment, installer ZIPs, child keys, or a `/v1/models` injection.
- Do not change model mapping, subagent blocking, `ch-relay`, or `ch-relay-vippro`.
- Do not write customer keys into `config.yaml` or `auth/`.
- Do not deploy to `192.168.8.1`. Deploy is a later explicit request and targets port `8330` only.
- Do not modify `/v0/management` handlers except to compile.

## Confirmed product rules

- Existing `access.api-keys` stay valid and are not quota-limited.
- Plugin keys use the prefix `ck_`. They are not exclusive frontend auth.
- Billed tokens are `input_tokens + output_tokens`. Reasoning and cache are displayed and are not added again.
- A failed, rejected, or canceled request is logged and bills `0`.
- Default expiry is 7 days. Default token cap is `100000000`. An explicit `0` means unlimited.
- The customer-facing page is `GET /key-dashboard`. The data call is `GET /key-dashboard/api/session` with `Authorization: Bearer <key>`. The key is never placed in a URL.

## Architecture

`key-shop` is a C-shared plugin. Testable policy and storage live in package `keyshop`, which has no CGO. `package main` is only the ABI adapter.

The host cannot let a plugin register a route outside `/v0/resource/plugins/<id>/`. A small host alias rewrites the two public URLs onto the plugin resource routes. Plugin resources are GET-only and are not management-authenticated. Admin mutations stay on `/v0/management/plugins/key-shop/...`, which already requires the management secret.

Management and resource route normalization rejects `:`, `*`, and spaces. No path parameter is allowed. Identity travels in JSON or in the `id` query value.

```text
client key ck_...
  -> FrontendAuth accepts active unexpired key, principal = key id
  -> RequestInterceptor reads the same secret from inbound headers
  -> transaction reserves min(max_tokens, 32768, remaining)
  -> upstream
  -> usage.handle stores the token snapshot by request id
  -> request.complete finalizes: bill on success, bill 0 otherwise, release reservation
```

Auth runs before the interceptor. A `ck_` key is accepted only when it is active, unexpired, and has remaining tokens after open reservations. Expired, disabled, and exhausted keys are not accepted. That closes `/v1/live` and `/v1/realtime*`, which select Codex OAuth directly and do not call `Execute`, `ExecuteStream`, `ExecuteCount`, or either request interceptor.

Those paths are unsupported for `ck_` keys in v1. `frontend_auth.authenticate` returns not authenticated for a `ck_` key when the path is `/v1/live`, starts with `/v1/live/`, is `/v1/realtime`, starts with `/v1/realtime/`, is `/v1/alpha/search`, or is `/backend-api/codex/alpha/search`. The plugin auth adapter cannot set an HTTP status, so the client receives 401. It does not reach upstream.

The host ignores an interceptor RPC error and continues upstream. A `ck_` admission or store failure must therefore be a successful plugin result with `Terminate: true` and status `503`. Returning an error from `handleMethod` is not a deny.

`request.intercept_before` is the second gate for paths that do execute. It denies an expired key with 403 `key_expired`, a disabled key with 403 `key_disabled`, and an exhausted key with 402 `token_limit_exceeded`. Count-token paths and `generate=false` use the same denial. They do not get a free upstream call. A key that is allowed on those paths reserves `0`.

A key that is already expired, disabled, or exhausted at auth never reaches the interceptor, so those clients see 401, not 402 or 403. The interceptor statuses apply when auth saw remaining tokens and the reserve transaction then denies, including two concurrent requests racing for the last tokens.

Responses websocket turns that reach `ExecuteStreamWithAuthManager` are covered by `request.intercept_before`.

## Identity and secrets

- Secret format: `ck_` plus 32 bytes encoded with `base64.RawURLEncoding`.
- Persist only `sha256` of the full secret, a 4-character display prefix after `ck_`, and the last 4 characters.
- Show the secret once, on create and on rotate.
- Frontend auth principal is the key id, never the secret. Usage logs therefore do not store the secret.
- Compare hashes with `subtle.ConstantTimeCompare`.
- Do not log the secret, management key, prompt, response, or upstream credential.

Header lookup order matches the native access provider: `Authorization` bearer, `X-Api-Key`, `X-Goog-Api-Key`. Query `key` and `auth_token` are ignored so a key cannot leak into an access log through the URL.

## Quota

Admission is one SQLite immediate transaction:

1. Load the key by secret hash.
2. Reject unless status is `active`, expiry is absent or in the future, and the key is not soft-deleted.
3. `remaining = token_limit - tokens_used - sum(open reservations)` when `token_limit > 0`.
4. Reserve `min(requested max_tokens, reserve_tokens, remaining)`. If `max_tokens` is absent, reserve `reserve_tokens` (default `32768`). If remaining is below 1, deny.
5. A `token_limit` of `0` reserves `0` and does not deny for quota.
6. Count-token paths and `generate=false` still deny expired, disabled, and exhausted keys. Only an allowed key reserves `0` on those paths.

Denial body is OpenAI-shaped JSON with status `402` for quota and `403` for a key that became disabled or expired between auth and intercept:

```json
{"error":{"message":"token limit exceeded","type":"insufficient_quota","code":"token_limit_exceeded"}}
```

```json
{"error":{"message":"api key expired","type":"invalid_request_error","code":"key_expired"}}
```

The host lifecycle id and the usage record id are different today. `newRequestLifecycleTracker` mints one UUID. `NewUsageReporter` mints another. The implementation must put the lifecycle id on the execution context with `usage.WithExecutionRequestID` before execution, and `NewUsageReporter` must use `usage.ExecutionRequestIDFromContext` when it is present. After that change, `usage.handle` and `request.complete` share `RequestID`. A missing context id still mints a reporter id so existing callers do not break.

Finalization is idempotent on that shared `request_id`:

- Success bills `input + output`, increments `tokens_used` and `request_count`, and writes one request row.
- Failure, rejection, and cancellation bill `0` and still write the request row.
- A second completion does not bill again.
- If completion arrives before usage, the request row is written with the tokens then known. A later usage event for an already finalized request does not change the bill.
- A sweeper deletes reservations older than 15 minutes without billing. A crash can underbill. It must not overbill.

Computed `exhausted` is true only when `token_limit > 0` and `tokens_used + open reservations >= token_limit`. A stored limit of `0` is unlimited and is never exhausted. The stored status remains `active` unless an operator disables the key. Computed status is what APIs return.

## Storage

SQLite through `modernc.org/sqlite`, WAL, `busy_timeout=5000`. The plugin module owns that dependency. The host module does not.

Default file is `~/.cliproxyapi/key-shop/key-shop.db`. A set `data_file` is used exactly. Tests pass a temporary file. The plugin creates the parent directory with mode `0700` and the database with owner-only access.

Tables:

- `keys`: `id`, `secret_hash`, `display_prefix`, `display_last4`, `label`, `customer`, `note`, `status`, `created_at`, `expires_at`, `token_limit`, `tokens_used`, `request_count`, `last_used_at`, `deleted_at`
- `reservations`: `request_id` primary key, `key_id`, `reserved_tokens`, `created_at`
- `requests`: `request_id` primary key, `key_id`, `model`, `transport`, `protocol`, `status`, `http_status`, `latency_ms`, `ttft_ms`, `input_tokens`, `output_tokens`, `reasoning_tokens`, `cached_tokens`, `billed_tokens`, `requested_at`, `completed_at`

`status` stored values are `active` and `disabled`. Computed values add `expired` and `exhausted`. Request status values are `succeeded`, `failed`, `rejected`, `canceled`.

Request retention deletes request rows older than 30 days. It does not reduce `tokens_used`.

## Plugin configuration

```yaml
plugins:
  enabled: true
  configs:
    key-shop:
      enabled: true
      priority: 100
      data_file: ""
      default_ttl: 168h
      default_token_limit: 100000000
      reserve_tokens: 32768
      reservation_ttl: 15m
      retention_days: 30
```

The plugin receives only its own `config_yaml`, not the CPA config directory. Empty `data_file` resolves to `~/.cliproxyapi/key-shop/key-shop.db`. An operator who wants another location sets `data_file`.

## HTTP

Admin routes require the CPA management secret because they are registered under `/v0/management`:

| Method | Path | Body / query |
| --- | --- | --- |
| POST | `/v0/management/plugins/key-shop/keys` | `label`, `customer`, `note`, `ttl_seconds`, `never_expires`, `token_limit`, `unlimited_tokens` |
| GET | `/v0/management/plugins/key-shop/keys` | optional `q` |
| POST | `/v0/management/plugins/key-shop/keys/update` | `id` plus mutable fields |
| POST | `/v0/management/plugins/key-shop/keys/rotate` | `id` |
| POST | `/v0/management/plugins/key-shop/keys/reset-usage` | `id` |
| POST | `/v0/management/plugins/key-shop/keys/delete` | `id` |
| GET | `/v0/management/plugins/key-shop/requests` | `id`, `status`, `model`, `from`, `to`, `limit` |
| GET | `/v0/management/plugins/key-shop/stats` | none |

Create and rotate return `secret` once. List and detail return `display` as `ck_abcd…wxyz`. They never return `secret` or `secret_hash`.

HTTP mapping is exact. Omitted `ttl_seconds` means `604800`. `never_expires: true` stores a null expiry and ignores `ttl_seconds`. Omitted `token_limit` means `100000000`. `unlimited_tokens: true` stores `0` and ignores `token_limit`. Update `status` accepts only `active` or `disabled`. Any other status is HTTP 400.

Public routes:

| Method | Browser path | Plugin resource path |
| --- | --- | --- |
| GET | `/key-dashboard` | `/v0/resource/plugins/key-shop/dashboard` |
| GET | `/key-dashboard/api/session` | `/v0/resource/plugins/key-shop/api/session` |

The host alias sets `X-Key-Shop-Alias: 1`, rewrites the path, calls `ServeResourceHTTP`, and then aborts the Gin context. The redirect is decided in `pluginResourceNoRoute` before `ServeResourceHTTP`. A direct GET of the long dashboard path, without that header, answers `302` to `/key-dashboard` and does not call the plugin. The session API does not redirect. Unknown alias paths answer `404` and abort. These routes are not API-key middleware routes and are not safe-mode proxy paths.

Session response contains lifecycle, limits, recent 30 requests, and `base_url` equal to the request host plus `/v1`. It does not contain another customer's data or the secret.

## UI

Both pages are embedded single-file HTML with no CDN and no external asset route.

- Admin resource menu label: `API Keys`. It calls the management API. On first use it asks for the management key and keeps it in `sessionStorage` only.
- Public page has an empty menu. It has a key field, a remember-in-`sessionStorage` checkbox, lifecycle, limits, and the latest 30 calls. Copy explains the base URL and that the key must not be put in a URL. Vietnamese is the default visible language. A button switches the static labels to English.

## Host change

`internal/api/key_shop_public.go` registers the alias. `AGENTS.md` records it as a custom flow that must survive upstream sync. The alias is inert when the plugin resource is not registered.

## Tests

Package `keyshop` tests cover hash, secret format, billing, expiry, reservation races, idempotent finalize, failure billing, and retention. Host tests cover the alias, redirect, and 404 without a plugin. No test uses `time.Sleep`. Time is injected.

## Deploy note

Installation needs a rebuilt CPA binary plus `plugins/linux/arm64/key-shop.so`. That step is outside this implementation. Do not copy either artifact to the router in this change.
