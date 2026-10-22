# Tunnel manager: tasks

Status: `[ ]` pending, `[x]` done.

## Ticket 1: backend

- [x] T1.1 Migration for table `tunnels` (additive, idempotent).
- [x] T1.2 `tunnels_store.go` CRUD with name uniqueness (case-insensitive) and 20-row cap.
- [x] T1.3 Validation helpers: name, kind, target_url, token.
- [x] T1.4 `TunnelManager`: injected command factory, scrubbed child env, fixed argv, token only via env.
- [x] T1.5 Ring buffer (500 lines) with token redaction.
- [x] T1.6 Quick tunnel URL parse from stderr into `public_url`.
- [x] T1.7 Supervision: backoff restart, `desired` flag, stop never restarts.
- [x] T1.8 Ownership check via `/proc/<pid>/environ` marker; non-Linux clears the record only.
- [x] T1.9 Shutdown: SIGTERM, 5 s, SIGKILL on own children only.
- [x] T1.10 Handlers: list, create, update (restart if running, empty token keeps old), delete, start, stop, logs.
- [x] T1.11 Wire `routes()` and `manage()` in `service.go` next to `groups`.
- [x] T1.12 Config keys `cloudflared_path`, `tunnels_enabled` in `main.go` and `keyshop.Config`.
- [x] T1.13 Tests with a fake cloudflared shell script (see spec Acceptance).
- [x] T1.14 Gate: gofmt, vet, test, plugin build; paste outputs in the packet `check`.

## Ticket 2: UI and docs

- [x] T2.1 Types, API client, mock service and fixtures.
- [x] T2.2 `TunnelsView.vue` with list, sheet, delete confirm, logs drawer.
- [x] T2.3 Route, nav item, App mount.
- [x] T2.4 Typecheck and build; refresh `go/web/admin.html`; `adminPage()` guards pass.
- [x] T2.5 Docs in `docs/key-shop.md` and `README.md`.
- [x] T2.6 Gate: typecheck, build, `go test ./...`.
