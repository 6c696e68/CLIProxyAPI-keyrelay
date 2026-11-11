# Key shop design system

## Principles

Admin is an operator workbench. It should feel like another CPA Management Center page: warm paper, one top tab bar, dense but aligned data, quiet gray actions. It runs inside the panel, so it follows the panel theme and never draws a second sidebar.

Client is a private ledger. It should feel calmer and more personal: cool mist canvas, serif identity, quota health as the visual center, no operator chrome.

Both systems use the same interaction grammar:

- Every important control is a custom Vue component. Native `select`, `checkbox`, `datetime-local`, and `range` are not used in the visible UI.
- Labels sit above controls. Hints sit below. Errors replace the hint and use the danger color plus `role="alert"`.
- Disabled controls stay visible at 45% opacity and do not accept pointer or keyboard activation.
- Icon-only buttons have an accessible name and a 36px target, 44px on touch layouts.
- Destructive actions are never the first or only filled button in a row.
- Empty, loading, and error states are designed views, not blank regions or browser alerts.

## Admin components

| Component | Behavior |
| --- | --- |
| `AppButton` | `primary`, `secondary`, `ghost`, `danger`; sizes `md` and `sm`; loading disables and shows a text label, not a spinner-only button |
| `AppTextInput` / `AppNumberInput` / `AppPasswordInput` | 36px, 8px radius, warm surface, focus ring |
| `AppSelect` | Button trigger, listbox, arrow keys, Home/End, Enter, Escape, typeahead |
| `AppCombobox` | Search field plus result list; selected items cannot be added twice |
| `AppSwitch` | `role="switch"`, 44px track target, label remains visible |
| `AppSegmented` | Two or more options, `aria-pressed` |
| `AppCheckbox` | 18px custom box, label click toggles |
| `AppDateTime` | Month grid, 24-hour time, presets, no Flatpickr and no native date input |
| `StatusChip` | Dot plus sentence-case label; good, warn, bad, muted |
| `Pager` | Previous/Next, disabled at ends, range text |
| `AppModal` | Centered dialog for create, edit, details, and delete. Escape and scrim close |
| `AppSheet` | Side sheet for long editors. Close stays in the sheet header, never a floating button |
| `Toast` | Bottom-right, 2.2s, status text, does not cover the primary action on mobile |

### Admin layout

- The admin is mounted in the panel as a plugin resource iframe. The panel header is fixed and floats over the iframe's top edge, so the app reserves that band in `--chrome-top` before painting content.
- Theme comes from the panel (`data-theme="dark"` means dark; `white` or absent means light). The app observes the parent and repaints on a live theme switch. It does not ship a second theme switch while embedded.
- Navigation is one horizontal tab bar: Dashboard, Model groups, Keys, Plans, Partners, Request log, Tunnels. No internal sidebar, no second nav next to the panel rail.
- The tab bar is sticky and owns the reserved band, so scrolled content never shows behind the panel actions. On narrow screens the tabs scroll horizontally and the active tab is scrolled into view.
- Global controls live in the tab bar: refresh always, theme and sign-out only when the page is opened outside the panel. Nothing floats over page content.
- Content is left-aligned inside a 1440px max width. Do not center the workspace like a marketing page.
- Metrics are two rows: four primary figures, then a quieter outcome strip. Do not render eleven identical cards. Below 768px the primary row stays two columns.
- Tables are desktop-only. Below 768px each record becomes a card with the same fields and actions, including usage and expiry.
- Charts share one shape in both plugin pages: y-axis labels, horizontal gridlines, three x labels, and an empty state inside the plot.

## Client components

The client has its own components with the same namespaced behavior, but Harbor colors, 10px controls, and 18px panels. Do not copy admin class names across projects.

Client-specific patterns:

- Top bar: wordmark, masked key, theme, switch key.
- Identity panel: serif label, status, created/expires/last used as a quiet row.
- Quota stack: token meter and request meter, each with used, limit, remaining, and a one-line accounting note.
- Endpoint row: mono URL, copy button, polite status.
- Model list: mono names in quiet chips, not accent-filled pills.
- Request detail: bottom sheet on mobile, centered dialog from 720px up.

## States

Every data region has:

- Loading: skeletons matching the final geometry.
- Empty: one sentence and the next action, inside the same panel.
- Error: inline alert with retry. No `window.alert`.
- Limit reached: danger alert above the affected meter, not a modal.

## Prototype-only chrome

A Preview menu exposes scenarios in dev builds. It sits inside the tab bar so it never covers page content, and it is not part of either design system. The production embed does not include it.
