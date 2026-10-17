# ch-relay-vippro

Plugin executor for CLIProxyAPI. It forwards Chat Completions and OpenAI Responses requests to one HTTPS OpenAI-compatible endpoint. Each upstream call uses a Codex OAuth access token taken from the host auth store. The plugin has no `api-key` and no `keys` field.

The plugin id, shared-library file stem, `Metadata.Name`, and `plugins.configs` key are all `ch-relay-vippro`.

This plugin does not use the host relay seam. It calls `host.auth.list`, `host.auth.get`, and `host.http`. It does not refresh tokens and it does not call `host.auth.save`.

## Install

Build a c-shared library and put it where the host loads plugins. The file stem must stay `ch-relay-vippro`. Do not commit the binary or the generated header.

macOS, from `examples/plugin/ch-relay-vippro/go`:

```bash
go build -buildmode=c-shared -o ch-relay-vippro.dylib .
rm -f ch-relay-vippro.h
```

Linux arm64 for a glibc 2.27 host, from the same directory:

```bash
CC="zig cc -target aarch64-linux-gnu.2.27" CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
  go build -buildmode=c-shared -o ch-relay-vippro.so .
rm -f ch-relay-vippro.h
```

Copy the library to the host plugin directory for that OS and CPU, for example `plugins/linux/arm64/ch-relay-vippro.so`. Restart the host process so it loads the new file. Replacing the CLIProxyAPI server binary is not required.

A bundled `linux-arm64/ch-relay-vippro.so` in a release zip is built with the command above.

## Enable

`plugins.enabled` stays false in `config.example.yaml`. Turn plugins on in the config you run, and set this plugin's `enabled` flag to true. If `enabled` is omitted, the host injects `enabled: false` and the plugin claims nothing.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    ch-relay-vippro:
      enabled: true
      priority: 1
      base-url: "https://7656b154.linhtd.com/v1"
      prefix: "vippro"
      headers:
        X-Title: "ch-relay-vippro"
        X-Session: "$CPA-SESSION-ID"
      request-retry: 3
      fallback-retry: 0
      rotation-mode: round-robin
      plus-preference: prefer
      sticky-seconds: 300
      model-cooldown-minutes: 1440
      stats-file: ""
      models:
        - name: "gpt-5.4"
          alias: "gpt-5.4"
          display-name: "GPT-5.4"
          fallback: []
```

`priority` is only the host's plugin order. Account choice uses each Codex auth record's own priority and keeps the highest tier.

## Config fields

| Field | Meaning |
| --- | --- |
| `enabled` | When false, the plugin claims no models and the executor rejects requests. |
| `base-url` | HTTPS origin plus optional path. Empty uses `https://7656b154.linhtd.com/v1`. `http` is accepted only for localhost, private IPs such as `192.168.8.1`, and shared addresses in `100.64.0.0/10` (Tailscale). Userinfo, query, and fragment are rejected. |
| `prefix` | Client model prefix. Trimmed of slashes. A value that still contains `/` claims nothing. |
| `headers` | Extra upstream headers. `$NAME` copies that client header. `$CPA-SESSION-ID` uses the executor session. A missing source drops the header. |
| `request-retry` | Extra attempts on the next account with the same upstream model. `0` sends once. Default `3`. Maximum `32`. A value outside that range is clamped. The total number of account attempts is `1 + request-retry`. |
| `fallback-retry` | Extra attempts with the row `fallback` names after every primary attempt failed because the upstream refused the model. `0` disables fallback even when a list is filled in. Default `0`. Maximum `3`. These calls do not spend `request-retry`. |
| `rotation-mode` | `round-robin`, `fill-first`, `least-recent`, or `sticky`. |
| `plus-preference` | `off`, `prefer`, or `only`. `ignore` is stored as `off`. Default `prefer`. |
| `sticky-seconds` | Sticky session lifetime. Default `300`. Maximum `86400`. |
| `model-cooldown-minutes` | After the upstream says the model is not supported for a ChatGPT account, skip that account and that access token for this many minutes. `0` disables the skip. Default `1440` (1 day). Maximum `10080`. When `stats-file` is set, the skip is saved in `stats-file + ".cooldowns.json"` and survives restart. The file stores a token hash, not the access token. |
| `stats-file` | JSON file of hourly buckets kept for 90 days. Empty does not write a file. |
| `models` | Rows of upstream model name, optional alias, optional fallback names, and optional OpenAI model fields. |

## Models

With `prefix: vippro` and alias `gpt-5.4`, clients call `vippro/gpt-5.4`. The upstream model name is the row `name`, not the alias. An empty prefix claims the alias itself.

A thinking suffix such as `(high)` is accepted on the client model and copied onto the upstream name.

Several rows with the same alias are one catalog model. The plugin rotates those upstream names. An exact repeat of the same `name` and `alias` keeps the first row and does not fail registration. A row with an alias and an empty name is rejected. A blank name row is skipped.

Optional row fields follow the OpenAI-compatible model object: `display-name`, `max-context-length`, `force-mapping`, `image`, `input-modalities`, `output-modalities`, `is-compat`, `use-max-completion-tokens`, and `thinking`. If `thinking` is omitted, the row inherits the canonical model's levels, including `xhigh` and `max` when that model supports them. An explicit `thinking.levels` list still wins.

`fallback` is an ordered list of upstream names. The row's own name and repeats are dropped. With `fallback-retry` above `0`, the plugin first tries the row `name` on the selected accounts. When every one of those attempts fails with a model refusal (the ChatGPT model-not-supported error, `model_not_found`, or "model ... does not exist"), or every account is in model cooldown, it sends the fallback names instead. A thinking suffix such as `(high)` stays attached. Fallback calls may use accounts in model cooldown, because that cooldown only means the primary model was refused.

Fallback only changes the `model` value in the request. Response bytes and stream bytes are passed through unchanged, so the response `model` field shows the fallback name. Fallback never starts after a 2xx response, including a 2xx stream that carries an error, because the client may already have bytes. Other errors such as 429 or 5xx keep the primary model. A Responses request that carries `previous_response_id` or encrypted reasoning from another model may still be refused by the fallback model.

A non-2xx response tries the next selected account and keeps that same model, up to `request-retry` extra calls. Stream error bytes are copied as-is. The ChatGPT model-not-supported error cools that account and access token for `model-cooldown-minutes`, including when the error arrives inside a 2xx stream.

## Accounts

A record is eligible when all of these are true:

- `provider` is `codex`
- not disabled
- not unavailable
- `next_retry_after` is empty or not in the future
- `access_token` is not empty

Plus means the access token that is sent as `Authorization: Bearer` has `chatgpt_plan_type` exactly `plus` in the JWT claim `https://api.openai.com/auth`. `id_token` and a stored `plan_type` of plus do not count when that bearer is a JWT that says something else. `pro`, `team`, `go`, and `free` are not Plus. `only` never sends a bearer whose JWT plan is not plus.

`plus-preference`:

- `off`: Plus and other Codex accounts share one pool. `ignore` is stored as `off`.
- `prefer`: while any enabled Plus account exists, including Plus accounts that are cooling or unavailable, free and other plans are not used. If every enabled Plus account is cooling or unavailable, the request fails with `no_plus_account` and does not call HTTP. Other plans are used only when no enabled Plus account exists.
- `only`: Plus accounts only. If none are eligible, the request fails with `no_plus_account` and does not call HTTP.

Among the accounts that remain, only the highest auth `priority` is used.

## Rotation

- `round-robin` keeps the access token that succeeded. One upstream error moves to the next account and stays there if that account succeeds. A client cancel does not move the token.
- `fill-first` keeps using the first account in the sorted pool.
- `least-recent` orders by `UpdatedAt`, earliest first.
- `sticky` keeps one account for a session until `sticky-seconds` elapses.

The session id is the first non-empty value among executor metadata `canonical_session_id`, `execution_session_id`, and `session_id`, then the headers `Session-Id`, `X-Session-Id`, and `X-Claude-Code-Session-Id`. `$CPA-SESSION-ID` uses that same value. A client header whose name is literally `CPA-SESSION-ID` is not copied.

Sticky is recorded for the account that actually succeeded. Repeating a successful account does not extend the original deadline.

## Upstream response

A 2xx body or stream is returned as the base URL sent it. The plugin does not read a 2xx body to switch accounts or models.

Chat streams pass each upstream line through `ConvertOpenAIResponseToOpenAI`. Responses streams are the original bytes. The request is `POST {base-url}/chat/completions` or `POST {base-url}/responses` with `Authorization: Bearer` set to the selected Codex access token. Stream requests also send `Accept: text/event-stream` and `Cache-Control: no-cache`.

A non-2xx response tries the next account with the same model, up to `1 + request-retry` calls. The error `model is not supported` with `ChatGPT account` cools that account index and that access token until `model-cooldown-minutes` elapse. Later requests do not select either one. The same match inside a 2xx stream body cools the account after that stream is opened. `0` still switches accounts inside the current request and does not remember the skip. When `stats-file` is set, active cooldowns are reloaded after restart. The last upstream error body is returned unchanged.

## Protocols

The executor accepts Chat Completions and OpenAI Responses, including streaming. Chat Completions go to `{base-url}/chat/completions`. Responses go to `{base-url}/responses`.

`executor.count_tokens` returns `{"input_tokens":0}` and does not call the host.

The client `Authorization` header is not forwarded. The plugin sets `Authorization: Bearer` to the selected Codex access token.

## Stats

`stats-file` stores hourly request and token buckets for 90 days. Restart reloads that file. Totals are the sum of buckets still inside the retention window.

`GET /v0/management/ch-relay-vippro/stats` returns immediately and does not call `host.auth.list`. `usage_by_email` lists each account email with success count, error count, and total tokens, sorted by request count descending.

`GET /v0/resource/plugins/ch-relay-vippro/dashboard` is the React dashboard that ships inside the plugin. The host lists it as the Dashboard menu. The page loads `?format=json` for totals, the email table, the chart, and the latest account check, and refreshes that JSON without reloading. The table shows `asd***@****.com`. Each Copy button copies the full email. This page is a plugin resource, so the host does not require the management key. Rebuild the page from `go/ui` with `bun run build` before compiling the plugin. Account checks still start from `POST /v0/management/ch-relay-vippro/accounts/check`, and the plugin also scans on its own.

`POST /v0/management/ch-relay-vippro/accounts/check` starts a background scan of enabled Codex accounts when one is not already running, then returns the same stats JSON. The plugin also scans shortly after load and every 30 minutes. Disabled and non-Codex records are skipped. The response does not include access tokens, emails, or auth file paths.

`accounts` is null until the first check. After that the object is:

| Field | Meaning |
| --- | --- |
| `running` | A check is in progress. |
| `checked` | Records finished in this check. |
| `total` | Enabled Codex records considered. |
| `eligible` | Enabled, available, not cooling, with a token. This includes free accounts. |
| `plus` | Eligible records whose live token plan is `plus`. |
| `cooling` | Enabled records still before `next_retry_after`. |
| `unavailable` | Enabled records that are unavailable or have no token. |
| `disabled_skipped` | Disabled Codex records. They are not part of `total`. |
| `checked_at` | RFC3339 time, or empty. |
| `duration_ms` | Check duration. |

`eligible` is not the set used by `prefer` or `only`. Those modes send only the Plus subset described above.

Token totals come from the upstream usage object, including `response.usage` inside response bodies and SSE `data:` lines.

The management page is the plugin resource above. Plugin config stays in the host plugin settings screen.

## Errors

| Code | When |
| --- | --- |
| `disabled` | The plugin `enabled` flag is false. |
| `unknown_model` | The client model is not claimed. |
| `invalid_payload` | The body has no usable `model` field. |
| `unsupported_format` | The format is not Chat Completions or Responses. |
| `no_plus_account` | `prefer` or `only` has no usable Plus account. |
| `no_account` | No eligible Codex account is left. |
| `auth_unavailable` | `host.auth.list` failed. |
| `upstream_error` | The last upstream attempt failed. HTTP status is the upstream status. The body is the upstream body. |
