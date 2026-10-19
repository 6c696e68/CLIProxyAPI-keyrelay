# Key shop UI

Vue 3 admin and customer pages. Production copies are the single-file HTML embeds:

- `examples/plugin/key-shop/go/web/admin.html`
- `examples/plugin/key-shop/go/web/dashboard.html`

`npm run dev` keeps the mock services and Preview bar. Production builds call the live plugin endpoints, restore page size 25, and omit the Preview bar.

## Screens

The admin app is a hash router with four screens. `AdminService` is declared once; `api/live.ts` and `mock/service.ts` both implement it.

| Screen | Hash | What it shows |
| --- | --- | --- |
| Dashboard | `#/` | Lifetime key health, usage over the selected range, status mix, top models, keys, customers, and accounts, latency, cache-hit ratio, and the latest requests |
| Model groups | `#/groups` | Group table (name, model count, key count) and a sheet editor. Models are upstream names. The built-in `all-models` group cannot be edited or deleted. |
| Keys | `#/keys` | Key table with a Group column, required group on create, and multi-select bulk edit and delete |
| Request log | `#/requests` | Filter bar, summary strip, cursor pager, and a detail drawer. Account is the upstream auth id (`auth_id`), shown truncated with the full value in the title and the drawer. |

The customer page stays a separate app. Its session shows the key's group name when one is assigned.

## Preview

```bash
cd examples/plugin/key-shop/ui/admin && npm install && npm run dev
cd examples/plugin/key-shop/ui/client && npm install && npm run dev
```

Admin preview: `http://127.0.0.1:5174`  
Client preview: `http://127.0.0.1:5175`

Any non-empty password opens the mock workspace. Type `bad` to see an authentication error.

## Build and embed

From the repository root. Rebuild an embed only when its sources are newer than the HTML already in `go/web/`.

```bash
cd examples/plugin/key-shop/ui/admin && npm run build && cp dist/index.html ../../go/web/admin.html
cd examples/plugin/key-shop/ui/client && npm run build && cp dist/index.html ../../go/web/dashboard.html
```

`npm run build` typechecks, then Vite emits one HTML file (`vite-plugin-singlefile`). Rebuild the `key-shop` shared library after replacing those files. The HTML is compiled into the library. A config reload does not pick up a newly copied `.so`.

## Documents

- `docs/design-tokens.md`
- `docs/design-system.md`
- `docs/spec.md`
- `docs/plan.md`
- `docs/tasks.md`
