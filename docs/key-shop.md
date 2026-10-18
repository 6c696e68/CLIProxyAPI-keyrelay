# key-shop

Operator and design notes for the `key-shop` plugin. The install and API reference is [examples/plugin/key-shop/README.md](../examples/plugin/key-shop/README.md). The original design is [docs/superpowers/specs/2026-10-17-key-shop-design.md](superpowers/specs/2026-10-17-key-shop-design.md).

## Place in the fork

`key-shop` is a custom flow on `feat/model-mapping`. It must survive an upstream sync. The host alias is small and lives in `internal/api/key_shop_public.go`. Policy and SQLite live in the plugin so a later host upgrade does not require rewriting the product rules.

Do not copy code from `LingyeNBird/cpa2pool`. That repository is AGPL-3.0. The usage-statistics repositories only record requests after they finish. They cannot reject an expired or exhausted key before upstream.

## Request path

```text
ck_ secret
  -> frontend_auth.authenticate
  -> request.intercept_before checks model access, then reserves tokens
  -> upstream
  -> usage.handle stores the token snapshot
  -> request.complete bills and releases the reservation
```

Model access is plugin-wide, not per key. `model_access_mode: full` allows every model. `custom` allows the configured alias, or the upstream `name` when alias is empty. Matching is case-insensitive and keeps a client thinking suffix such as `(high)`. For a `ck_` key, the host checks this policy after global model mapping and before provider routing. An allowed alias is rewritten to its upstream name, with the thinking suffix preserved, before routing. A model outside the custom list returns HTTP 403 `model_not_allowed` before routing and before `Store.Admit()`, so it creates no token reservation. It still consumes one request-quota unit. A known customer key gets a rejected request row. Once the request limit is reached, later calls return HTTP 429. Operator keys do not enter this policy. `/v1/models` does not run the interceptor.

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

## Port 8330 note

The GL-MT6000 service on port `8319` is a different process from `8330`. `8330` is `/root/.cliproxyapi-v8030`. Installing key-shop there requires that instance's binary, its `key-shop.so`, and a `plugins.configs.key-shop` block. Do not copy those files onto `8319` unless that deploy was requested. Never replace `config.yaml` or `auth/` as part of a binary copy. Add only the plugin config keys, with a backup first.
