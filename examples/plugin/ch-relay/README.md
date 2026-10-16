# ch-relay

`ch-relay` is a host-mediated CLIProxyAPI plugin. It exposes configured models under the `ch-relay/<model>` namespace and forwards supported OpenAI Chat Completions and Responses requests through the host's Codex OAuth accounts.

The plugin does **not** receive access tokens, auth files, account IDs, or the configured destination. The host validates the persisted destination, selects and refreshes Codex credentials, applies rotation/Plus/sticky/retry policy, and sends the outbound HTTPS request.

## Build

From the repository root:

```bash
# macOS development plugin
go build -C examples/plugin/ch-relay/go -buildmode=c-shared \
  -o examples/plugin/ch-relay/go/ch-relay.dylib .

# GL-MT6000: match the host's Entware glibc 2.27 arm64 runtime
CC='zig cc -target aarch64-linux-gnu.2.27' CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
  go build -C examples/plugin/ch-relay/go -buildmode=c-shared -ldflags='-s -w' \
  -o /tmp/ch-relay-router-arm64.so .
```

Copy the library into the configured `plugins.dir` and name it `ch-relay.so` on Linux or `ch-relay.dylib` on macOS. The generated header is a build artifact and is not required at runtime.

The GL-MT6000 host uses a dynamic CGO build with Entware glibc 2.27 in `/opt/lib`, an rpath of `/opt/lib`, and ELF interpreter `/opt/ld-linux-aarch64.so.1`. Match that libc/architecture when building the plugin; do not load a musl `.so` into this glibc host. OpenWrt's base-system libc does not determine the running host's ABI.

## Enable

Both the global plugin switch and this plugin's config must be enabled:

```yaml
plugins:
  enabled: true
  dir: "/root/.cliproxyapi/plugins"
  configs:
    ch-relay:
      enabled: true
      priority: 1
      base_url: "https://relay.example/v1"
      models:
        - relay-gpt
      claim_mode: namespace
      rotation_mode: round-robin
      plus_preference: prefer
      sticky_seconds: 300
      retry_on_auth_error: true
      retry_on_rate_limit: true
      retry_on_5xx: true
      max_retries: 2
```

Restart CLIProxyAPI after replacing a dynamic library. Use the management panel's Plugins page to inspect metadata, edit fields, and open the redacted **Codex Relay** status resource.

## Configuration

| Field | Values / meaning |
| --- | --- |
| `base_url` | HTTPS URL with optional base path. Userinfo, query, and fragment are rejected. The host sends borrowed Codex access tokens only to this destination. Configure only a relay you trust. |
| `models` | Upstream model names this plugin may claim. Empty claims nothing. Do not include the `ch-relay/` namespace in entries. |
| `claim_mode` | `namespace` exposes `ch-relay/<model>`; `configured-only` exposes the configured name directly. Namespace mode is the safe default. |
| `rotation_mode` | `round-robin`, `fill-first`, `least-recent`, or `sticky`. Selection is host-owned. |
| `plus_preference` | `ignore`, `prefer`, or `only`. Selection is host-owned. |
| `sticky_seconds` | Sticky affinity lifetime from 0 to 86400 seconds; default 300. |
| `retry_on_auth_error` | Allow host-side retries on 401 for replay-safe `GET /models` only; not 403. |
| `retry_on_rate_limit` | Allow host-side retries on 429 for replay-safe `GET /models` only. |
| `retry_on_5xx` | Allow host-side retries on 5xx for replay-safe `GET /models` only. |
| `max_retries` | Host-side retry cap for replay-safe `GET /models`, from 0 to 8; default 2. Does not enable POST replay. |

The host callback permits only `GET /models`, `POST /chat/completions`, and `POST /responses`. Caller authorization headers are rejected and never forwarded by the plugin.

Retry switches apply conservatively to replay-safe model catalog reads only. Once a `POST /chat/completions` or `POST /responses` request has been sent, the host never replays it—even on 401, 429, or 5xx, and whether streaming or non-streaming. This avoids duplicate generation, side effects, or charges when an upstream may already have processed the request. Its failure is returned to the caller instead.

## Stream behavior

- Chat callbacks emit bare JSON chunks; CLIProxyAPI's Chat handler owns `data:` framing and `[DONE]`. Frames are delimited only by upstream SSE separators or terminal `Done`, never by transport read boundaries or a `data:` substring inside content.
- Responses callbacks preserve complete raw SSE frames and their original bytes. Frames may be coalesced or split across reads; incomplete tails are buffered until the next read or terminal `Done`.
- Stream IDs and callback IDs are opaque. Cancellation, plugin shutdown, upstream EOF, and downstream errors close both sides.

## Status and safety

The `/status` management resource displays aggregate counts and relay policy only. It does not render tokens, auth paths, raw account IDs, full email addresses, request bodies, or response bodies.

Do not put an API key, bearer token, auth JSON, or token-bearing header in plugin configuration. Codex credentials remain in the host OAuth store.
