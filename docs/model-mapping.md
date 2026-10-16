# Global Model Mapping

`model-mapping` rewrites the client-requested model name **before provider routing**. Use it when a client hardcodes a retired or unofficial name (for example Zeroclaw sending `gpt-5.4`) and the proxy should call a live upstream model instead.

This is a first-class feature. Do not treat it as missing, and do not replace it with `oauth-model-alias` or per-credential `models[].alias`.

## When to use which mechanism

| Mechanism | What it does | Wildcard / regex | Scope |
|---|---|---|---|
| **`routing.model-mapping`** | Rewrites the request model for routing and upstream | exact, `*` wildcard, regex | All channels, before provider selection |
| `oauth.model-alias` (`oauth-model-alias`) | Catalog + routing alias for OAuth/file auths | exact only | OAuth only: vertex, aistudio, antigravity, claude, codex, kimi, xai |
| `*-api-key.models[].alias` | Per-credential alias / model pool | exact only | That API-key / openai-compat / vertex-compat credential |
| Plugin `ModelRouter.TargetModel` | Plugin rewrite before auth | plugin-defined | Plugins only |

`oauth-model-alias` does **not** apply to `codex-api-key`, `claude-api-key`, `openai-compatibility`, or other API-key credentials.

## Request flow

```
client model (e.g. gpt-5.4)
  → model-mapping (first enabled match wins)
  → thinking suffix preserved: gpt-5.4(high) → gpt-5.5(high)
  → plugin ModelRouter (if any)
  → registry / auth / oauth-model-alias / per-key alias
  → upstream receives mapped name
  → if force-mapping (default true): response model fields echo the original client name
```

## Config

Canonical v8 YAML is `routing.model-mapping`. The runtime field stays `model-mapping` on `SDKConfig`. A legacy top-level `model-mapping` list still loads. A v8 configuration write moves it to `routing.model-mapping` and rejects the legacy key (`use routing.model-mapping`). Do not overwrite a live `config.yaml` on binary deploy; the existing list keeps working until that write.

```yaml
routing:
  model-mapping:
    - match: "gpt-5.4"
      target: "gpt-5.5"
      # mode: exact | wildcard | regex   # empty: wildcard if match contains '*', else exact
      # force-mapping: true              # default true: client still sees gpt-5.4
      # enabled: true                    # default true
    - match: "gpt-5.*"
      target: "gpt-5.5"
      mode: wildcard
    - match: "^gpt-5\\.4(-.*)?$"
      target: "gpt-5.5$1"
      mode: regex
```

Legacy files may still use a root `model-mapping:` list. Same rule fields.

Rules:

- First enabled matching rule wins.
- Match uses the base model after `thinking.ParseSuffix`. Suffixes such as `(high)` or `(8192)` are copied onto the mapped name unless the target already has a suffix.
- Exact is case-insensitive.
- Wildcard uses `*` as “any substring”.
- Regex is compiled at sanitize time. Invalid regex is dropped and logged; it does not crash the server. `$1` replacements work only in regex mode.
- Prefixed credentials (`teamA/gpt-5.4`) match the full string. Use `*/gpt-5.4` or a regex to catch prefixes.
- v1 does not add mapped names to `/v1/models`. Clients that send a hardcoded name do not need a catalog entry.

## Code map

- Types / sanitize: `internal/config/config_types.go`, `internal/config/model_mapping.go`
- Engine: `internal/modelmapping/`
- Handler hook: `sdk/api/handlers/handlers_model_mapping.go`
  - Called from `handlers_execution.go`, `handlers_stream.go`, `handlers_context.go` (`PrepareStreamModelRoute`)
- Response rewrite: `sdk/cliproxy/auth.RewriteResponseModel` / `RewriteSSEModel`
- Management API: `GET/PUT/PATCH/DELETE /v8/management/routing/model-mapping` in `internal/api/handlers/management/config_lists.go`. Generic config path: `/v8/management/config/routing/model-mapping`. Do not add `/v0/management` routes.
- TUI: tab **Model Mapping** (`internal/tui/mapping_tab.go`)
- Config example: `config.example.yaml`
- Watcher log: `internal/watcher/diff/model_mapping.go`

Do not implement per-executor remaps. Keep the rewrite in the handler hook so OpenAI, Claude, Gemini, Codex, stream, count, and websocket share one path.

## Management

- YAML is the source of truth.
- TUI `--tui` can add / edit / delete / toggle rules.
- Management API persists with comment-preserving YAML write and hot-reload. v8 writes store `routing.model-mapping`. `PUT []` or deleting the last rule removes that YAML key; reload is empty. Other settings and comments stay. HTTP 200 from a binary that predates this save is not proof the key is gone.
- The custom management panel already has a global model-mapping tab. It uses `/v8/management/routing/model-mapping`. Do not add a `/v0/management` route or treat that tab as a follow-up.

## Router

GL-MT6000 `192.168.8.1` is musl OpenWrt plus Entware glibc. The live service `/etc/init.d/cliproxyapi` runs `/root/.cliproxyapi/run-protected.sh`, which execs `/root/.cliproxyapi/cli-proxy-api` directly on port `8319`. The binary's ELF interpreter must be `/opt/ld-linux-aarch64.so.1`, not the musl `/lib/ld-linux-aarch64.so.1`. `LD_PRELOAD` must be unset. Do not ship `CGO_ENABLED=0` / `*_no-plugin`. Plugin `.so` files must be GLIBC ABI <= 2.27 and are loaded from `/root/.cliproxyapi/plugins/linux/arm64/`. Replace only the server binary, `ch-relay-vippro.so`, and `static/management.html`. Never overwrite `config.yaml` or the auth directory. See the Fork rule in `AGENTS.md`.

## Agent checklist

If a user says a client model name is retired, hardcoded, or should be redirected:

1. Point them at `model-mapping`, not `oauth-model-alias`, unless they explicitly want an OAuth catalog alias.
2. Keep `force-mapping` default true so the client still sees the name it sent.
3. Preserve thinking suffixes.
4. Do not silently drop this feature during refactors of routing, handlers, or config.
