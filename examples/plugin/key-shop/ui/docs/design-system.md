# Key shop design system

## Principles

Admin is an operator workbench. It should feel like another CPA Management Center page: warm paper, left rail, dense but aligned data, quiet gray actions.

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
| `AppModal` | Centered dialog for create, edit, details, and delete. No side drawer. Escape and scrim close |
| `AppModal` | Centered confirmation, focus moves inside, Escape cancels |
| `Toast` | Bottom-right, 2.2s, status text, does not cover the primary action on mobile |

### Admin layout

- Desktop: 216px rail, page canvas, floating action cluster at the top right.
- Rail groups: Overview, Access, Activity. Items scroll to sections. The active item follows the section in view.
- Below 768px the rail becomes an off-canvas drawer. A menu button stays in the top-left cluster.
- The rail can collapse to a 60px icon rail on desktop. Labels remain available to assistive technology.
- Content is left-aligned inside a 1440px max width. Do not center the workspace like a marketing page.
- Metrics are two rows: four primary figures, then a quieter outcome strip. Do not render eleven identical cards.
- Tables are desktop-only. Below 768px each record becomes a card with the same actions.

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

A Preview menu exposes scenarios. It is visually separate, labeled "Preview", and must be deleted before any production embed. It is not part of either design system.
