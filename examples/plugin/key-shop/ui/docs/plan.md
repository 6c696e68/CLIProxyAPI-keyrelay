# Key shop Vue mock UI implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build two reviewable Vue 3 mock interfaces for key-shop admin and the customer dashboard without touching the embedded production pages.

**Architecture:** Each interface is an independent Vite app with its own tokens, components, fixtures, and in-memory service. Views never call the network. Production HTML remains the runtime until a later, separately approved wiring phase.

**Tech Stack:** Vue 3.5, TypeScript, Vite 6, Tailwind CSS 4.

**Spec:** `examples/plugin/key-shop/ui/docs/spec.md`

## Global Constraints

- Do not edit `examples/plugin/key-shop/go/web/admin.html`.
- Do not edit `examples/plugin/key-shop/go/web/dashboard.html`.
- Do not edit `web_embed.go` or `main.go` in this phase.
- Do not call plugin, management, or `/v1` endpoints.
- Do not add a shared visual component package.
- Admin tokens follow CPA warm paper values in `docs/design-tokens.md`.
- Client tokens follow the Harbor ledger values in `docs/design-tokens.md`.
- Visible form controls are custom Vue components.
- Prototype page size is 5. Production page size stays 25 until the wiring phase.
- Real data wiring is blocked until the exact confirmation `DUYỆT OK`.
- Preview output stays in each app's `dist/`.

---

### Task 1: Document the design boundary

**Files:**
- Create: `examples/plugin/key-shop/ui/docs/design-tokens.md`
- Create: `examples/plugin/key-shop/ui/docs/design-system.md`
- Create: `examples/plugin/key-shop/ui/docs/spec.md`
- Create: `examples/plugin/key-shop/ui/docs/plan.md`
- Create: `examples/plugin/key-shop/ui/docs/tasks.md`
- Create: `examples/plugin/key-shop/ui/README.md`

- [ ] Write the five documents and the preview README.
- [ ] Confirm the documents name the production freeze and the `DUYỆT OK` gate.

### Task 2: Scaffold both apps

**Files:**
- Create: `examples/plugin/key-shop/ui/admin/package.json`
- Create: `examples/plugin/key-shop/ui/client/package.json`
- Create: Vite, TypeScript, and Tailwind entry files in both apps

- [ ] Pin Vue 3, Vite 6, and Tailwind 4.
- [ ] Set admin port 5174 and client port 5175.
- [ ] Run `npm install` in each app.
- [ ] Do not install Flatpickr, React, or a component library.

### Task 3: Admin mock workspace

**Files:**
- Create: `examples/plugin/key-shop/ui/admin/src/**`

- [ ] Add CPA tokens for light and dark.
- [ ] Add custom controls: button, text, number, password, select, combobox, switch, segmented control, checkbox, date-time, sheet, modal, toast, pager, status.
- [ ] Add gate, shell, metrics, model policy, inventory, request explorer, create, edit, delete, and secret reveal.
- [ ] Add preview scenarios and in-memory mutations.

### Task 4: Client mock ledger

**Files:**
- Create: `examples/plugin/key-shop/ui/client/src/**`

- [ ] Add Harbor tokens for light and dark.
- [ ] Add a separate control set, not an import from admin.
- [ ] Add gate, ledger, quotas, models, requests, detail dialog, and pagination.
- [ ] Add preview scenarios.

### Task 5: Verify and review

- [ ] Run `npm run typecheck` and `npm run build` in both apps.
- [ ] Open both previews at 390 and 1280 widths.
- [ ] Confirm `git status` does not show production HTML or Go embed changes.
- [ ] Call `reviewer` on the new UI diff.
- [ ] Fix critical findings before claiming the mock is ready.

## Later phase, not this plan

After `DUYỆT OK`, a separate plan will wire the approved views to the existing endpoints, restore page size 25, build single-file HTML, and replace the embedded pages. That plan must also replace the Go Flatpickr injection because the custom date picker removes those markers.
