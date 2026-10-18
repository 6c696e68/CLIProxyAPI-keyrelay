# Key shop Vue mock UI specification

## Goal

Replace the visual and component foundation of the key-shop admin and customer dashboard with two independent Vue 3 applications, reviewed first with mock data.

This phase does not change plugin behavior, routes, or embedded HTML.

## Non-goals

- No edits to `go/web/admin.html`, `go/web/dashboard.html`, `web_embed.go`, or `main.go`.
- No `fetch` to plugin, management, or `/v1` endpoints.
- No production single-file replacement.
- No shared visual package between admin and client.
- No new product features beyond the screens that already exist.

Real API wiring starts only after the exact confirmation `DUYỆT OK`.

## Stack

- Vue 3.5 + TypeScript
- Vite 6
- Tailwind CSS 4, build-time, no CDN
- No UI library
- No Flatpickr
- Preview servers: admin `127.0.0.1:5174`, client `127.0.0.1:5175`

## Mock boundary

Views call a local service. The service returns deterministic fixtures and can mutate an in-memory copy so create, edit, rotate, reset, and delete can be reviewed. Those mutations are simulated. They do not call the network.

Authentication accepts any non-empty secret. The secret `bad` returns an authentication error. Remembered secrets use `sessionStorage` keys `keyshop.prototype.admin` and `keyshop.prototype.client`.

Preview scenarios: `ready`, `loading`, `empty`, `error`, `limited`.

Prototype page size is 5. Production page size remains 25 and must be restored during the later wiring phase.

## Admin screens

### Gate

- Management key field, show/hide, submit.
- Empty submit shows "Enter the management key."
- `bad` shows "The management key could not be authenticated."
- Loading disables the button and changes its label to "Checking key".

### Workspace

Sections, in order:

1. Overview metrics.
2. Customer model access.
3. Key inventory.
4. Request explorer.

Metrics:

- Primary: total keys, active, tokens used, admitted calls.
- Secondary: succeeded, failed, canceled, rejected, request-limited keys, expiring within 7 days.

Model access:

- Mode `All models` or `Selected models only`.
- Search discovered models, add, optional alias, remove.
- Save stays disabled while loading or when the policy has not loaded.
- Save shows a success toast. Error scenario shows an inline error and does not claim success.

Inventory:

- Search by label, customer, or id.
- Status filter: all, active, disabled, expired, exhausted.
- Desktop table columns: Key, Customer, Status, Requests, Usage, Created, Expires, Actions.
- Mobile cards show the same facts and actions.
- Actions: enable/disable switch, edit, rotate, delete.
- Rotate reveals the new secret once in the create/edit sheet area.
- Delete asks for confirmation and names the label.

Create sheet:

- Label with generate.
- Customer, quantity 1–500, expiry with presets, never expires, token limit, unlimited tokens, request limit, unlimited requests, note.
- Quantity above 1 explains the unique suffix rule.
- Submit reveals secrets once, with copy.

Edit sheet:

- Read-only key id with copy.
- Label, customer, token limit, request limit, unlimited requests, expiry, never expires, status.
- Save and reset usage.
- Reset clears token usage only. The note says so.

Request explorer:

- Follows the selected key, or all keys when none is selected.
- Columns: Time, Model, Transport, Status, Reason, TTFT, Latency, Billed.
- Mobile cards for the same fields.
- Cursor-style previous/next over the mock pages.

## Client screens

### Gate

- API key field, remember checkbox, submit.
- Empty and `bad` errors.
- Copy explains that the key is not stored by the page except for the current session when remembered.

### Ledger

- Top bar with wordmark, masked key, theme, switch key.
- Identity panel with label, display, status, created, expires, last used.
- Endpoint copy.
- Token meter and request meter.
- Limit-reached alert when the scenario or key requires it.
- Stats: recorded, admitted, succeeded, failed, canceled, rejected, total tokens, cached tokens on this page, key status.
- Available models, or an unavailable note.
- Recent requests table and mobile cards.
- Detail dialog with request id, status, reason, HTTP status, transport, protocol, latency, TTFT, and token split.
- Pagination.

## Responsive

Review widths: 390, 768, 1280, 1440.

- No horizontal page scroll at those widths.
- Tables scroll inside their shell only when their content requires it.
- Sheets and dialogs stay within the viewport and scroll internally.
- Text in metrics can wrap without overlapping.

## Accessibility

- One `h1` per view.
- Dialogs use `role="dialog"`, `aria-modal="true"`, and a labelled title.
- Status uses `role="status"` where it updates live.
- Keyboard can open, choose, and close select, combobox, date popover, sheet, and modal.
- Contrast target is 4.5:1 for text and 3:1 for UI boundaries.

## Acceptance for this phase

- Both apps install, type-check, and build.
- Both preview servers render every required state.
- Production Go files are unchanged.
- A reviewer can reject the visual design without any production rollback.
