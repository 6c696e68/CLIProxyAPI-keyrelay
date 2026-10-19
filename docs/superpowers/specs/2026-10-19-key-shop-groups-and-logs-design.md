# key-shop: model groups, multi-screen admin, request log

Status: backend implemented in `examples/plugin/key-shop/go/keyshop` (groups.go, logs.go, bulk.go, service_groups.go). Admin UI is the work item.

## Goals

1. Model groups: the operator gathers upstream models into named groups. Every new key must belong to one group.
2. Admin UI split into four screens: Dashboard, Model Groups, Keys, Request Log. Shared components, one visual language.
3. Request Log shows, per row, which key and customer made the call, plus model, effort, upstream account, status, latency, TTFT, tok/s, tokens in/out, cache, reasoning.
4. Keys screen supports multi-select with bulk edit and bulk delete.

## Behavior

- Group = `{id, name, description, all_models, models[]}`. `models` is `[{name, alias}]`. `name` is the upstream model, such as `vippro/gpt-6-sol`. `alias` is optional and is the name the client sends. `models_json` stays a string array of upstream names so an older plugin can still read the group. `aliases_json` is a map of upstream name to alias.
- Built-in group `all-models` (`is_system`, `all_models: true`) always exists and cannot be edited or deleted.
- A key with an empty `group_id` keeps the legacy global-policy behavior. New keys (single or batch) require `group_id`.
- A grouped `ck_` key is enforced by the host before provider routing. The host calls `keyshop.resolve_model` and does not consult the global YAML list. An allowed alias is rewritten to the upstream name, keeping a thinking suffix such as `(high)`. A group denial returns HTTP 403 `model_not_allowed`, creates a rejected request row for the key, and reserves no tokens. An unknown group fails closed. The plugin interceptor still checks the group as defense in depth and accepts either the alias or the upstream name.
- A group that lists specific models needs at least one model. A group in use by keys cannot be deleted (HTTP 409 with `key_count`).
- Editing a group takes effect on the next request.

## API contract

Base: `/v0/management/plugins/key-shop`. All requests use the management bearer key. Errors are JSON `{"error": "message"}` with a 4xx/5xx status (the existing `managementJSON` already parses this).

### Groups

| Call | Body | Response |
| --- | --- | --- |
| `GET /groups` | | `{groups: Group[]}` ordered system first, then by name. `Group = {id, name, description, all_models, models: [{name, alias}], is_system, key_count, created_at, updated_at}` |
| `POST /groups` | `{name, description?, all_models?, models?}` | `{group}`. `models` may be `string[]` or `[{name, alias}]`. The response is always `[{name, alias}]`. |
| `POST /groups/update` | `{id, name, description?, all_models?, models?}` (full replace) | `{group}` |
| `POST /groups/delete` | `{id}` | `{deleted: true}`; 409 `{error, key_count}` when in use; 403 for the system group |

Validation errors are 400 (`name must be 1-80 characters`, `choose at least one model`, `name already exists`, `names and aliases must be unique within the group`). An alias is optional. Names and aliases are unique case-insensitively across the group. An alias must not equal a different entry's name. An alias equal to its own name is stored empty.

### Keys

Key objects keep their existing PascalCase JSON and gain `GroupID` (empty string for legacy keys).

- `POST /keys` and `POST /keys/bulk`: add required `group_id`. Missing or unknown returns 400.
- `POST /keys/update`: optional `group_id` (cannot be cleared).
- `POST /keys/bulk-update`: `{ids: string[1..500], ...fields}`. Only fields present in the body change: `group_id, status ("active"|"disabled"), customer, note, ttl_seconds, never_expires, token_limit, unlimited_tokens, request_limit, unlimited_requests`. Returns `{updated: n}`. One unknown or deleted id returns 404 and changes nothing.
- `POST /keys/bulk-delete`: `{ids: string[1..500]}` returns `{deleted: n}`; same all-or-nothing rule.
- `GET /keys`: query `q, status, page, page_size`, plus `group` (a group id, or `none` for legacy keys without a group).

### Request log

`GET /requests` query: `id` (key id), `customer` (substring), `group` (group id), `status`, `model`, `account` (substring of auth id), `effort`, `code` (rejection code), `q` (search request id, model, key label, customer, key id), `from`, `to` (RFC3339), `limit` (default 25, max 100), `cursor`.

Response:

```json
{
  "requests": [{
    "request_id": "", "key_id": "", "key_label": "", "key_display": "ck_abcd…wxyz",
    "customer": "", "group_id": "", "group_name": "",
    "model": "vippro/gpt-6-astra", "requested_model": "gpt-6-astra",
    "transport": "http", "protocol": "openai", "status": "succeeded", "http_status": 200,
    "rejection_code": "", "latency_ms": 35900, "ttft_ms": 2660, "tok_per_s": 11.6,
    "input": 48, "output": 385, "reasoning": 1200, "cached": 36, "billed": 433,
    "provider": "", "auth_id": "", "auth_index": "", "auth_type": "", "effort": "max", "service_tier": "",
    "requested_at": "RFC3339", "completed_at": "RFC3339"
  }],
  "next_cursor": "", "has_more": false, "total": 0,
  "summary": {"total":0,"succeeded":0,"failed":0,"canceled":0,"rejected":0,"input":0,"output":0,"reasoning":0,"cached":0,"billed":0,"avg_latency_ms":0,"avg_ttft_ms":0,"error_rate":0}
}
```

`total` and `summary` cover the whole filter, not just the page. `tok_per_s` is `null` when unknown. `effort`, `provider`, `auth_id` are empty for rejected rows and for rows recorded before this release. `status` values: `succeeded, failed, canceled, rejected, pending`.

### Usage stats

`GET /stats/usage?from=&to=&bucket=hour|day` (defaults: last 24 h, `day`) returns:

```json
{"from":"","to":"","bucket":"hour","summary":{},"series":[{"start":"RFC3339","requests":0,"succeeded":0,"failed":0,"rejected":0,"canceled":0,"input":0,"output":0,"billed":0}],
 "top_models":[{"id":"","label":"","detail":"","requests":0,"billed":0}],"top_keys":[],"top_customers":[],"top_accounts":[],
 "latency":{"latency_p50":0,"latency_p95":0,"ttft_p50":0,"ttft_p95":0},"cache_hit_ratio":0}
```

`series` only contains buckets that have data; the UI fills gaps. For `top_keys`, `label` is the key label and `detail` the customer. For `top_accounts`, `detail` is the provider. Request rows are kept for `retention_days` (default 30).

### Customer session

`GET /api/session` (public, `ck_` key) adds `group: {id, name, all_models}` or `null`. For a limited group, `models` is the client-visible names of that group (the alias when set, otherwise the upstream name). The all-models group and a key with no group still follow the global policy. `model_access_mode` is `full` when nothing limits the key.

`keyshop.resolve_model` input is `{Secret, Model, BodyModel}`. A grouped key returns `{Mode:"group", Allowed, Model}`. `Model` is the upstream name with the client's thinking suffix kept. The match is the client-visible name. A bare upstream name is accepted only when that entry has no alias. `all_models` returns `Allowed` with the requested model unchanged. An unknown group returns `Allowed: false`. No group, an unknown key, or a non-`ck_` secret returns `{Mode:"legacy"}`.

## UI requirements

Stack stays Vue 3 + Tailwind 4 + `vite-plugin-singlefile`; no new runtime dependency (hash router written in-repo). Reuse `components/ui/*`, `.panel`, `.data-table`, `.card-list`, `.metric-*` styles.

- Shell: sidebar items navigate between `#/` (Dashboard), `#/groups`, `#/keys`, `#/requests` with an active state; mobile drawer and theme toggle stay.
- Dashboard: metric cards, usage chart over time (hand-drawn SVG, light/dark safe), status mix, top models/keys/customers/accounts, latency percentiles, cache-hit ratio, and the 10 latest requests linking to Request Log.
- Model Groups: table (name, models count, keys count, actions) and a sheet editor with a two-pane model picker (searchable catalog on the left from `/v1/models` as the existing `catalogModels()` does; selected on the right) and an "Add all matching filter" action. Each selected row has a client alias input, saved as `[{name, alias}]`. Duplicate aliases show an inline error. Show a notice when the legacy global policy is `custom`.
- Keys: existing table plus Group column, row checkboxes plus select-page, and a bulk bar (change group, enable/disable, expiry, limits, delete with a confirm dialog that states the count). Create sheet has a required group select with no empty option. Edit sheet has a group select.
- Request Log: filter bar, summary strip, table with columns Time | Model (+effort chip) | Key and customer | Account | Status (HTTP chip) | Latency (+TTFT) | Tok/s | Tokens in → out (+cache, reasoning), a detail drawer, cursor pager, optional auto-refresh. Mobile uses cards. Account shows `auth_id` truncated with the full value in a title and the drawer; never show it on the customer page.
- `AdminService` interface declared once; `api/live.ts` and `mock/service.ts` both implement it so `npm run dev` keeps working.
- Accessibility and responsive rules from `ui/docs/spec.md` still apply (390, 768, 1280, 1440).

## Out of scope

CSV export, prices, RPM limits, filtering `/v1/models` by group.
