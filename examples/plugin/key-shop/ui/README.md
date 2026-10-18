# Key shop UI

Vue 3 admin and customer pages. Production copies are the single-file HTML embeds:

- `examples/plugin/key-shop/go/web/admin.html`
- `examples/plugin/key-shop/go/web/dashboard.html`

`npm run dev` keeps the mock services and Preview bar. Production builds call the live plugin endpoints, restore page size 25, and omit the Preview bar.

## Preview

```bash
cd examples/plugin/key-shop/ui/admin && npm install && npm run dev
cd examples/plugin/key-shop/ui/client && npm install && npm run dev
```

Admin preview: `http://127.0.0.1:5174`  
Client preview: `http://127.0.0.1:5175`

Any non-empty password opens the mock workspace. Type `bad` to see an authentication error.

## Embed

```bash
cd examples/plugin/key-shop/ui/admin && npm run build && cp dist/index.html ../../go/web/admin.html
cd examples/plugin/key-shop/ui/client && npm run build && cp dist/index.html ../../go/web/dashboard.html
```

Rebuild the `key-shop` plugin after replacing those files. The HTML is compiled into the shared library.

## Documents

- `docs/design-tokens.md`
- `docs/design-system.md`
- `docs/spec.md`
- `docs/plan.md`
- `docs/tasks.md`
