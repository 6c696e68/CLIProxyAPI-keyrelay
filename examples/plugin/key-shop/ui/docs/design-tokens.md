# Key shop design tokens

Two independent token sets. Do not import one app's tokens into the other.

Shared rules, not shared values:

- 4px spacing base.
- One gray family per surface.
- One accent per surface.
- Tabular numbers for every metric.
- Focus ring is 2px, offset 2px, never removed.
- Motion stays at or under 200ms, and respects `prefers-reduced-motion`.
- Minimum control height is 36px on desktop and 44px for primary mobile actions.

## Admin — CPA workbench

Source of truth: `Cli-Proxy-API-Management-Center/src/styles/themes.scss` and `variables.scss`.

### Color

| Token | Light | Dark |
| --- | --- | --- |
| `--bg-page` | `#faf9f5` | `#151412` |
| `--bg-surface` | `#f0eee8` | `#1d1b18` |
| `--bg-muted` | `#e9e6df` | `#262320` |
| `--bg-hover` | `#e3e0d8` | `#2e2a26` |
| `--bg-float` | `#fffdf9` | `#2a2723` |
| `--ink` | `#2d2a26` | `#f6f4f1` |
| `--ink-soft` | `#6d6760` | `#c9c3bb` |
| `--ink-faint` | `#a29c95` | `#9c958d` |
| `--line` | `#e3e1db` | `#3a3530` |
| `--line-strong` | `#d5d2cb` | `#4a453f` |
| `--accent` | `#8b8680` | `#8b8680` |
| `--accent-hover` | `#7f7a74` | `#9a948e` |
| `--accent-contrast` | `#ffffff` | `#ffffff` |
| `--good` | `#0f8f68` | `#6ee7b7` |
| `--good-bg` | `#e7f6f0` | `#14352c` |
| `--warn` | `#9a5b16` | `#fcd34d` |
| `--warn-bg` | `#f8f0e4` | `#3a2d14` |
| `--bad` | `#a34738` | `#f1b0a6` |
| `--bad-bg` | `#f8ebe8` | `#3a221e` |

`--good` is darkened from CPA's `#10b981` on light surfaces so small status text clears 4.5:1. The meter fill may use `#10b981`.

### Type

- Sans: `-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", sans-serif`
- Mono: `ui-monospace, "SF Mono", "Cascadia Mono", Menlo, Consolas, monospace`
- Page title: 28px / 700 / -0.03em / 1.15
- Section title: 18px / 700 / -0.02em
- Body: 14px / 400 / 1.5
- Label: 12px / 600 / 0.01em
- Eyebrow: 11px / 700 / 0.08em, sentence case, not all-caps walls of text
- Table header: 11px / 700 / 0.06em
- Metric: 22px / 700 / tabular-nums / -0.03em

### Space, radius, elevation

- Space: 4, 8, 12, 16, 20, 24, 32, 40
- Control radius: 8px
- Panel radius: 12px
- Sheet radius: 16px on the leading edge only
- Sidebar: 216px expanded, 60px collapsed
- Content max: 1440px
- Shell gutter: 24px, 16px below 768px
- Shadow: `0 1px 2px rgb(45 42 38 / 0.06)` on panels; `0 18px 40px rgb(45 42 38 / 0.16)` on overlays
- Z: sticky 20, dropdown 40, sheet 60, modal 70, toast 80

## Client — Harbor ledger

A cool, quiet customer ledger. It does not use CPA taupe, and it does not use the old cyan-on-black page.

### Color

| Token | Light | Dark |
| --- | --- | --- |
| `--bg-page` | `#f3f6f8` | `#10161c` |
| `--bg-surface` | `#ffffff` | `#182028` |
| `--bg-muted` | `#e7eef2` | `#222b34` |
| `--bg-hover` | `#dce6ec` | `#2a3540` |
| `--ink` | `#17212b` | `#eef3f6` |
| `--ink-soft` | `#5d6b78` | `#a9b6c0` |
| `--ink-faint` | `#8b9aa6` | `#7f8d99` |
| `--line` | `#d5e0e7` | `#31404c` |
| `--line-strong` | `#c3d0d9` | `#415263` |
| `--accent` | `#0f6e6b` | `#8fd0cb` |
| `--accent-hover` | `#0c5c59` | `#b7e4e0` |
| `--accent-contrast` | `#f4fffe` | `#10211f` |
| `--accent-soft` | `#e5f3f2` | `#173430` |
| `--good` | `#1f7a56` | `#8ed7b4` |
| `--warn` | `#8a5a16` | `#e7c48a` |
| `--bad` | `#9d3b3b` | `#f0b4b4` |

Accent saturation stays under the old `#55d8ed`. Teal is used for the primary action, the healthy meter, and links. It is not used as a page background.

### Type

- Display: `Iowan Old Style, Palatino, "Palatino Linotype", Georgia, serif`
- Sans: `Avenir Next, "Segoe UI", sans-serif`
- Mono: same system mono as admin, but it is a client token, not an import
- Display title: 40px / 620 / -0.035em / 1.05, down to 32px below 640px
- Section title: 20px / 650 / -0.02em
- Body: 15px / 400 / 1.55, measure under 68 characters for prose
- Metric: 28px / 650 / tabular-nums
- Label: 12px / 600

### Space and shape

- Content max: 1080px
- Page padding: 28px 24px 64px, 20px 16px 40px below 640px
- Panel radius: 18px
- Inner radius: 12px
- Control radius: 10px
- No left sidebar. Navigation is a 64px top bar.
- Overlay shadow uses the cool ink, not pure black: `0 24px 48px rgb(16 22 28 / 0.18)`
