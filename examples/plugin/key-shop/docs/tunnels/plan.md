# Tunnel manager: plan

Two tickets, run one after the other. Ticket 2 starts only after ticket 1 is closed.

## Ticket 1: backend (Go, `examples/plugin/key-shop/go`)

1. Schema: additive migration adding table `tunnels` in `keyshop/store.go` migration list. Do not alter existing tables.
2. Store: `keyshop/tunnels_store.go` with `TunnelRow` and CRUD (`ListTunnels`, `GetTunnel`, `InsertTunnel`, `UpdateTunnel`, `DeleteTunnel`, `SetTunnelRuntime`). Soft-deleted key rows and other tables are never touched.
3. Manager: `keyshop/tunnels.go` with `TunnelManager` (spawn, stop, restart, ring buffer, redaction, ownership check, backoff, shutdown). Process creation goes through a small `commandFactory` so tests inject a fake script.
4. Service: `keyshop/service_tunnels.go` handlers and validation; wire in `service.go` (`routes()` list and `manage()` switch, added next to `groups`).
5. Wiring: `go/main.go` parses `cloudflared_path` and `tunnels_enabled`, creates the manager in `configure`, stops it in `stopSweeperLocked`/shutdown. `keyshop.Config` gets the two fields.
6. Tests: `keyshop/tunnels_test.go` covering validation, CRUD, secret never returned, fake-cloudflared lifecycle (start, URL parse, crash restart, stop, delete), redaction, env isolation (child env has no inherited vars), ownership check, `tunnels_enabled=false`.
7. Gate: `gofmt`, `go vet ./...`, `go test ./...`, `go build` of the plugin as in the `Makefile` of `examples/plugin`.

Files (10): `go/keyshop/store.go`, `go/keyshop/tunnels_store.go`, `go/keyshop/tunnels.go`, `go/keyshop/service_tunnels.go`, `go/keyshop/service.go`, `go/main.go`, `go/keyshop/tunnels_test.go`, `go/keyshop/service_tunnels_test.go`, `README.md`, `docs/tunnels/tasks.md`.

## Ticket 2: admin UI and docs

1. Types and API: `ui/admin/src/types.ts`, `api/live.ts`, `api/map.ts`, `mock/service.ts`, `mock/fixtures.ts`.
2. View: `ui/admin/src/views/TunnelsView.vue` (list with status chips, create/edit sheet, delete confirm, start/stop, log drawer, one-time token field that is never prefilled).
3. Shell: route in `composables/useRoute.ts`, nav item in `components/AdminShell.vue`, view mount in `App.vue`; i18n-free, English labels like the rest of the admin UI.
4. Build: `npm run build` in `ui/admin`, copy the single-file output over `go/web/admin.html`, keep `adminPage()` guards passing.
5. Docs: `docs/key-shop.md` and key-shop `README.md` section.
6. Gate: `npm run typecheck`, `npm run build`, `go test ./...`.

The working tree already has uncommitted edits in `go/web/admin.html`, `ui/admin/src/components/ModelPicker.vue`, `ui/admin/src/styles.css`. They belong to the user. Never revert, stash or reformat them; ticket 2 builds on top of them.

## Order and risk

- Ticket 1 is the risk: process handling. The spec's isolation rules are the review checklist.
- Ticket 2 is low risk and only reads the API contract from the spec.
- No deploy, no commit, no push unless the user asks later.
