# Tunnel manager: spec

Module inside the `key-shop` plugin. Lets an admin add, edit, delete, start and stop Cloudflare tunnels that the plugin itself created. It never touches tunnels that belong to the device.

## Decisions (closed, do not reopen)

| Topic | Decision |
|---|---|
| Home | `key-shop` plugin, new Admin tab "Tunnels". No new plugin. |
| Tunnel kinds | `token` (named tunnel, run with a Cloudflare tunnel token) and `quick` (trycloudflare, `--url`). |
| Engine | Plugin spawns `cloudflared` as a child process. No Go tunnel library, no new Go dependency. |
| Scope | Only tunnels with a row in the plugin table `tunnels`. Nothing is discovered by scanning. |
| Persistence | New SQLite table `tunnels` in the existing key-shop DB, added by an additive migration. |
| Secrets | The tunnel token is stored in the DB, never returned by any API (only `has_token` and a 4-char tail hint), never logged, never put on argv. |
| Public API | Management routes only, same auth as `groups`. No public route. |
| Autostart | Per-tunnel flag `autostart`. Default false. |

## Isolation from device tunnels (hard rules)

1. The plugin never runs `pkill`, `killall`, `pgrep`-and-kill, or `launchctl`/`systemctl` for cloudflared. It only signals a process it spawned itself, held as `*exec.Cmd`, or a recorded pid that passes the ownership check in rule 4.
2. Child env is built from scratch, not inherited: `PATH`, `TUNNEL_TOKEN` (token kind only), `KEYSHOP_TUNNEL_ID=<id>`, and `HOME=<data dir>/tunnels/<id>`. No inherited `TUNNEL_*`, `CLOUDFLARED_*` or `HOME`, so `~/.cloudflared/` and the device's `config.yml` are never read or written.
3. Fixed argv, no shell: token kind `tunnel --no-autoupdate --metrics 127.0.0.1:0 run`; quick kind `tunnel --no-autoupdate --metrics 127.0.0.1:0 --url <target>`. The token goes only through env. `cloudflared service install` is never called.
4. Ownership check on adoption after a plugin restart: a recorded pid is signaled only if `/proc/<pid>/environ` contains `KEYSHOP_TUNNEL_ID=<that id>` (Linux). On other OSes the record is cleared and nothing is signaled.
5. On plugin shutdown the plugin stops every child it started (SIGTERM, 5 s, then SIGKILL on that child only).
6. Delete of a tunnel stops its child first, removes its `HOME` dir, then removes the row. It never removes anything outside `<data dir>/tunnels/<id>`.

## Data model

Table `tunnels`:

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | `tn_` + 12 random hex chars |
| `name` | TEXT | 1-64 chars, unique case-insensitive |
| `kind` | TEXT | `token` or `quick` |
| `target_url` | TEXT | `http://` or `https://` host[:port], required for `quick`, optional hint for `token` |
| `token` | TEXT | token kind only; empty for quick |
| `autostart` | INTEGER | 0 or 1 |
| `desired` | TEXT | `running` or `stopped`; what the admin last asked for |
| `pid` | INTEGER | last spawned pid, 0 if none |
| `public_url` | TEXT | quick kind: parsed `https://*.trycloudflare.com`; empty otherwise |
| `last_error` | TEXT | last exit reason, truncated to 300 chars |
| `created_at`, `updated_at` | TEXT | RFC3339 UTC |

Runtime status (not stored): `running`, `starting`, `stopped`, `failed`, plus `restarts` and `started_at`.

## Config (plugin YAML, all optional)

```yaml
cloudflared_path: ""        # default: look up "cloudflared" in PATH
tunnels_enabled: true       # false: routes return 404 and no child is ever spawned
```

Missing binary is not a startup error. Start returns `409 cloudflared not found`.

## API (management routes, JSON)

| Method | Path | Body or query | Result |
|---|---|---|---|
| GET | `/plugins/key-shop/tunnels` | none | `{tunnels:[Tunnel], cloudflared:{found,path,version}}` |
| POST | `/plugins/key-shop/tunnels` | `{name,kind,target_url,token,autostart}` | `201`-style `Tunnel` |
| POST | `/plugins/key-shop/tunnels/update` | `{id,name?,target_url?,token?,autostart?}` | `Tunnel`. Empty or absent `token` keeps the old one. Kind is immutable. If running, it restarts. |
| POST | `/plugins/key-shop/tunnels/delete` | `{id}` | `{ok:true}` |
| POST | `/plugins/key-shop/tunnels/start` | `{id}` | `Tunnel` |
| POST | `/plugins/key-shop/tunnels/stop` | `{id}` | `Tunnel` |
| GET | `/plugins/key-shop/tunnels/logs` | `?id=&lines=200` | `{lines:[string]}` from an in-memory ring buffer (max 500 lines, token redacted) |

`Tunnel` JSON: `id,name,kind,target_url,has_token,token_hint,autostart,desired,status,pid,public_url,last_error,restarts,started_at,created_at,updated_at`.

Errors use `jsonError`: 400 invalid input, 404 unknown id, 409 duplicate name / already running / cloudflared missing, 500 otherwise. The same handlers are reached with POST only for writes.

## Validation

- `name`: trimmed, 1-64 chars, no control chars.
- `kind`: exactly `token` or `quick`.
- `target_url`: parsed with `net/url`, scheme `http` or `https`, non-empty host, no userinfo, no spaces. Required for `quick`.
- `token`: required for `token` kind on create, 20-4096 chars, no whitespace.
- Max 20 tunnels total.

## Process supervision

- One goroutine per running tunnel owns `cmd.Wait`.
- Unexpected exit while `desired=running` restarts with backoff 2 s, 4 s, 8 s ... capped at 60 s. Backoff resets after 60 s of uptime. Stop sets `desired=stopped` first so no restart happens.
- Quick kind: parse `https://[a-z0-9-]+\.trycloudflare\.com` from stderr into `public_url`.
- stdout and stderr merge into the ring buffer; any line containing the token is replaced by `[redacted]` before it is stored.
- Plugin start: for each row with `desired=running` or `autostart=1`, start it (after the ownership check on a stale pid).

## Out of scope

Cloudflare API calls (creating tunnels or DNS), ingress rule editing, metrics, SSH or other tunnel kinds, anything in `/v0/management`, host (`cmd/`, `internal/`) code.

## Acceptance

- `go test ./...` in `examples/plugin/key-shop/go` passes, with the new tests using a fake cloudflared script, never the real binary.
- A test proves a foreign process with a similar name is never signaled.
- No change outside `examples/plugin/key-shop/`.
