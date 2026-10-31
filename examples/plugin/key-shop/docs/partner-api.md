# key-shop partner API

Integrator contract for sellers that mint `ck_` keys through key-shop. key-shop does not take money; the seller charges its own wallet and must debit only after a mint succeeds. The design and policy rationale is in [the partner sales spec](../../../../docs/superpowers/specs/2026-10-30-key-shop-partner-sales-design.md).

## Base URL and auth

The host exposes the API at:

```text
https://cpa.example.com/keyshop/partner/v1
```

Every request needs a partner secret:

```http
Authorization: Bearer ksp_xxx
```

- The secret is `ksp_` followed by 43 base64url characters (32 random bytes).
- Only the SHA-256 is stored. key-shop returns the secret once, on create or rotate.
- Do not send `X-Key-Shop-Partner` or `X-Key-Shop-Client-IP`. The host strips any inbound `X-Key-Shop-Partner*` header and sets both itself.
- A disabled partner gets `403 partner_disabled`. A missing or unknown secret gets the same `401 unauthorized`.
- `health` also requires auth.

The API must be enabled with `partner_api_enabled: true`. While it is off, every partner request returns `503 partner_api_disabled` before auth.

Every request and response is JSON. Timestamps are RFC3339 in UTC.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | Credential and server check |
| GET | `/plans` | Plans this partner may sell |
| POST | `/mint` | Create an order and its keys |
| POST | `/confirm` | Mark an order paid, after commit |
| POST | `/void` | Cancel an order; disables its keys |
| POST | `/status` | Read one order and its keys |
| POST | `/refs` | List this partner's orders, newest last |

### health

```http
GET /keyshop/partner/v1/health
```

```json
{
  "ok": true,
  "partner": { "id": "ptn_demoshop", "name": "Demo shop" },
  "server_time": "2026-10-30T12:00:00Z"
}
```

### plans

```http
GET /keyshop/partner/v1/plans
```

Returns only active plans this partner may use. An empty `allowed_plan_ids` on the partner means every plan.

```json
{
  "plans": [
    {
      "id": "plan_pro",
      "name": "Pro 30d",
      "description": "Monthly plan",
      "revision": 2,
      "active": true,
      "group": {
        "id": "grp_vippro",
        "name": "vippro",
        "all_models": false,
        "models": ["gpt-6-sol", "gpt-6-astra"]
      },
      "ttl_seconds": 2592000,
      "never_expires": false,
      "token_limit": 0,
      "unlimited_tokens": true,
      "request_limit": 5000,
      "unlimited_requests": false
    }
  ],
  "base_url": "https://cpa.example.com/v1",
  "dashboard_url": "https://cpa.example.com/client"
}
```

`base_url` and `dashboard_url` follow the same rule on `mint`:

- With `public_base_url` set: `base_url = public_base_url + "/v1"` and `dashboard_url = public_base_url + public_dashboard_path`.
- Otherwise: the request `Host` with `https`, except `localhost` and `127.0.0.1` use `http`.

`models` lists the names a customer may call (the client alias when set, otherwise the upstream name). Send these values to the customer, not the upstream names.

### mint

```http
POST /keyshop/partner/v1/mint
Content-Type: application/json
```

```json
{
  "ref": "shop-20261030-0001",
  "plan_id": "plan_pro",
  "plan_revision": 2,
  "quantity": 2,
  "secrets": ["ck_xxx", "ck_yyy"],
  "customer": "buyer@example.com",
  "note": "order 1001"
}
```

The seller generates the secrets, `ck_` followed by 43 base64url characters (32 random bytes), exactly like a customer key. key-shop stores only their SHA-256.

Validation is `400 bad_request`:

- `ref` matches `^[A-Za-z0-9._:-]{8,80}$`.
- `quantity` is between 1 and the partner maximum (`partner.max_quantity` when set, otherwise `partner_max_quantity`).
- `len(secrets) == quantity`.
- Every secret matches `^ck_[A-Za-z0-9_-]{43}$` and repeats within the request are rejected.
- `customer` and `note` are at most 200 characters each.
- The request body must be at most `partner_max_body_kb` (the host may reject a larger body with `413` first).

Success (`200`) returns keys in the **same order** as the request secrets:

```json
{
  "ref": "shop-20261030-0001",
  "state": "minted",
  "replayed": false,
  "keys": [
    { "id": "3f6c...", "display": "ck_7Hq2…k9Lw", "expires_at": "2026-11-29T12:00:00Z" },
    { "id": "9a1b...", "display": "ck_2Mfp…x4Td", "expires_at": "2026-11-29T12:00:00Z" }
  ],
  "plan": { "id": "plan_pro", "revision": 2, "...": "the full plan object" },
  "base_url": "https://cpa.example.com/v1",
  "dashboard_url": "https://cpa.example.com/client"
}
```

- A minted key is active immediately. `confirm` does not activate it.
- `expires_at` is the mint time plus the plan TTL, or `null` when the plan never expires.
- `display` is `ck_` plus the first 4 and last 4 characters of the secret, for example `ck_7Hq2…k9Lw`.
- `state` is the order's current state: `minted` or, on a replay after confirm, `confirmed`.
- The plan object has the fields shown under `plans`.

### confirm

```http
POST /keyshop/partner/v1/confirm
```

```json
{ "ref": "shop-20261030-0001", "order_id": "shop-1001", "buyer": "buyer@example.com" }
```

Success (`200`):

```json
{ "ref": "shop-20261030-0001", "state": "confirmed" }
```

- `order_id` is required. Empty or whitespace returns `400 bad_request`.
- Confirming the same `ref` with the same `order_id` is safe and returns 200.
- A different `order_id` returns `409 order_conflict`.
- Confirming a voided ref returns `410 ref_voided`.
- A non-empty `buyer` replaces the `customer` on the order's keys.

### void

```http
POST /keyshop/partner/v1/void
```

```json
{ "ref": "shop-20261030-0001", "reason": "shop debit failed" }
```

Success (`200`):

```json
{ "ref": "shop-20261030-0001", "state": "voided", "keys_disabled": 2 }
```

- Voiding a minted order sets it to `voided`, disables its keys, and reports how many were disabled.
- Voiding an already voided ref returns 200 with `keys_disabled: 0`.
- Voiding a confirmed ref returns `409 ref_confirmed`.
- Voiding an unknown ref writes a tombstone and returns 200 with `keys_disabled: 0`. A later mint for that ref returns `410 ref_voided`, so a failed sale can never be revived.

### status

```http
POST /keyshop/partner/v1/status
```

```json
{ "ref": "shop-20261030-0001" }
```

```json
{
  "ref": "shop-20261030-0001",
  "state": "minted",
  "order_id": "shop-1001",
  "keys": [
    { "id": "3f6c...", "display": "ck_7Hq2…k9Lw", "status": "active", "expires_at": "2026-11-29T12:00:00Z" },
    { "id": "9a1b...", "display": "ck_2Mfp…x4Td", "status": "active", "expires_at": "2026-11-29T12:00:00Z" }
  ]
}
```

`status` is the computed key status, as on the keys list: `active`, `disabled`, `expired`, or `exhausted`. A missing ref returns `404 ref_not_found`.

### refs

```http
POST /keyshop/partner/v1/refs
```

```json
{ "since": "2026-10-30T00:00:00Z", "until": "2026-10-31T00:00:00Z", "cursor": "", "limit": 100 }
```

All fields are optional; an empty body is the same as `{}`.

```json
{
  "refs": [
    {
      "ref": "shop-20261030-0001",
      "state": "minted",
      "quantity": 2,
      "plan_id": "plan_pro",
      "order_id": "",
      "created_at": "2026-10-30T12:00:00Z"
    }
  ],
  "next_cursor": "opaque-string"
}
```

- Only this partner's orders are returned.
- Ordered by `created_at`, oldest first.
- `limit` defaults to 100 and is capped at 500.
- `cursor` is opaque. Pass `next_cursor` back to continue; an empty `next_cursor` means the page is the end.
- `since` and `until` are inclusive RFC3339 bounds. A malformed time or cursor returns `400 bad_request`.

## Idempotency, replay, and tombstones

`ref` is the idempotency key and must stay unique per partner and order. The server fingerprints `plan_id`, `plan_revision`, `quantity`, and a SHA-256 of every secret. `customer` and `note` are not part of the fingerprint.

- Repeating a mint with the same fingerprint returns `200` with `replayed: true`, the order's current state, and the same keys in their original order. No new keys or orders are written.
- Repeating a ref with a different fingerprint returns `409 ref_conflict`.
- A voided ref always returns `410 ref_voided` for `mint`. That includes tombstones for refs that were never minted.
- A plan content change bumps its revision, and the fingerprint includes the revision. A **new** ref that names an old revision returns `409 plan_changed`, with the current plan in a top-level `plan` field next to `error`. An existing ref retried with its original body still replays, because its fingerprint matches; retrying that same ref with the new revision returns `409 ref_conflict`. Reload plans and start a new ref for the new revision.
- `secret_conflict` (409) means a secret already exists in keys, from a previous order or another partner. Generate fresh secrets and use a new ref.

## Error responses

Errors use:

```json
{ "error": { "code": "daily_cap_reached", "message": "daily key cap reached" } }
```

`plan_changed` adds a top-level `plan` object:

```json
{ "error": { "code": "plan_changed", "message": "plan revision changed" }, "plan": { "id": "plan_pro", "revision": 3 } }
```

| HTTP | `code` | Meaning |
| --- | --- | --- |
| 400 | `bad_request` | Invalid JSON, a missing `ref`/`order_id`, a failed mint validation, a bad `since`/`until`, an invalid cursor, or a body over `partner_max_body_kb` |
| 401 | `unauthorized` | Missing or unknown partner secret |
| 403 | `partner_disabled` | The secret is valid but the partner is disabled |
| 403 | `plan_not_allowed` | The plan is not in the partner's allowed list |
| 404 | `plan_not_found` | The plan id does not exist |
| 404 | `ref_not_found` | No order for this partner and ref (`confirm`/`status`) |
| 409 | `plan_inactive` | The plan exists but is deactivated |
| 409 | `plan_changed` | The mint named an old plan revision; the current plan is in `plan` |
| 409 | `ref_conflict` | The ref exists with a different fingerprint |
| 409 | `secret_conflict` | A supplied key secret already exists |
| 409 | `order_conflict` | The ref is confirmed with a different `order_id` |
| 409 | `ref_confirmed` | `void` cannot cancel a confirmed order |
| 410 | `ref_voided` | The ref is voided or tombstoned |
| 429 | `daily_cap_reached` | The partner's daily key cap would be exceeded |
| 503 | `partner_api_disabled` | `partner_api_enabled` is false |
| 500 | `internal` | Unexpected server error; retry with the same body and ref |

Transport-level responses from the host:

- `404` for any path outside the seven documented partner routes, or when the partner route is not registered.
- `413` when the host's generic management body cap rejects the request before the plugin's `partner_max_body_kb` check.

## Caps and quotas

- **Quantity:** 1 to `partner.max_quantity` when set, otherwise `partner_max_quantity` (default 50, config range 1–500).
- **Daily keys:** `partner.daily_key_cap` when set, otherwise `partner_daily_key_cap` (default 300). `0` means unlimited. The sum covers this partner's orders created in the current UTC day with state other than `voided`. Voided orders stop consuming the cap.
- **Plan limits:** TTL and token/request limits come from the plan. `ttl_seconds` applies unless `never_expires`; `unlimited_tokens`/`unlimited_requests` store no limit.
- There is no separate rate limiter. The daily key cap is the mint quota; the API still accepts health, plans, status, refs, confirm, and void calls.

## Shop integration checklist

1. **CPA first, money second.** Fetch `/plans`, pick a plan, then mint. Debit the customer only after `mint` returns 200.
2. **Stable ref per order.** Generate one `ref` (8–80 characters of `A-Za-z0-9._:-`) per order and never reuse it for a different body. Generate N fresh `ck_` secrets for that ref.
3. **Send the exact same body on a retry.** A network error, timeout, or 5xx is safe to retry with the same `ref`, plan revision, quantity, and secrets. The replay returns the same keys.
4. **Void on failure before commit.** If the order cannot be committed in the shop after a successful mint, call `/void` for that ref. The keys are disabled and the ref stays dead.
5. **Confirm after commit.** Once the order is stored and the customer is debited, call `/confirm` with the shop `order_id`. Retry safely; the same `order_id` is idempotent.
6. **Reconcile.** Poll `/refs` with `since` and `cursor`, then call `/status` for anything that needs a fresh key state. Reconcile `minted` vs `confirmed` vs `voided` against shop orders.
7. **Handle `plan_changed`.** Reload plans, check whether the price or limits changed, and start a new order with a new ref. Never reuse the old ref for the new revision.
8. **Never debit on a failed mint.** A `400`, `403`, `404`, `409`, `410`, `429`, or `503` is not a sale. For `429`, wait for the next UTC day or raise the partner cap.
9. **Keep secrets out of logs and the browser.** The `ksp_` partner secret belongs only in the shop's server-side secret store. `ck_` secrets are sent only in the `mint` body and delivered once to the buyer. Do not log either one.
10. **Health check first.** `GET /health` proves the base URL, the alias, the enabled flag, and the secret before a sale starts.

## Operator configuration

These keys live in `plugins.configs.key-shop`. An invalid value on reconfigure keeps the previous value and logs a warning. An absent key uses the default below; the admin **Partner API settings** card shows an empty field for an absent key and leaves the server default in place when saved.

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `partner_api_enabled` | bool | `false` | Enables the public partner API |
| `public_base_url` | string | `""` | Absolute http(s) URL without a trailing `/`; empty uses the request Host |
| `public_dashboard_path` | string | `/client` | Must start with `/` |
| `partner_max_quantity` | integer | `50` | 1–500; the most keys one mint may create |
| `partner_daily_key_cap` | integer | `300` | ≥ 0; `0` means unlimited |
| `partner_max_body_kb` | integer | `64` | 1–1024; the plugin business body limit |
| `partner_unconfirmed_alert_min` | integer | `10` | ≥ 1; minutes before a minted order counts as overdue |
| `partner_log_retention_days` | integer | `30` | ≥ 1; how long `partner_calls` rows are kept |
| `partner_key_label_template` | string | `{plan}-{ref}-{i}` | Non-empty, ≤ 120; placeholders `{plan}`, `{partner}`, `{ref}` (last 8), `{i}` (1-based) |
| `partner_key_note_template` | string | `partner:{partner} ref:{ref}` | ≤ 200; same placeholders |

The admin screens are **Plans** (`#/plans`) and **Partners** (`#/partners`). Plans fix the group, lifetime, and limits; deleting a plan still referenced by an order returns `409` with the order count. Partners can be created with no secrets in the response body except the one-time `secret`; rotate replaces it immediately. The Partners detail drawer shows the 24h call log and orders with filters, and the settings card edits the table above through the existing plugin config save path without touching other keys.

## Management endpoints used by the admin UI

These are not part of the partner contract. They sit under `/v0/management/plugins/key-shop` and require the management secret:

- `GET/POST /plans`, `POST /plans/update`, `POST /plans/delete`
- `GET/POST /partners`, `POST /partners/update`, `POST /partners/rotate`, `POST /partners/delete`
- `GET /partners/calls` (`partner`, `op`, `status` = `ok`/`error`, `from`, `to`, `limit`, `cursor`)
- `GET /partners/orders` (`partner`, `state`, `ref`, `from`, `to`, `limit`, `cursor`)
- `GET /keys` with `partner` and `ref` filters; rows carry `partner_id` and `partner_ref`

`partner_calls` records one row per partner call with partner id (empty when auth failed), op, HTTP status, error code, ref (bounded to 80 characters before auth), latency, and client IP. It never stores tokens or `ck_` secrets.
