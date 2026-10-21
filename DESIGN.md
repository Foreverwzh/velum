# Design

Velum desktop (Tauri + React, `desktop/src`). Operate surface: a working tool people open briefly and often. Craft bar: Linear / Raycast (user's reference, 2026-10-21) — restrained, precise, equally good in light and dark. Identity lives in details, not a theme.

## Principles

- One window, one structure. No "simple / pro" split: sidebar navigation, the first page answers "is protection on, and which line is traffic taking".
- Explanation is the signature. Every route decision renders as the **route chain** (`Chain` in `components/ui.tsx`): rule `#n` → policy groups → node, always the same shape, everywhere (overview, groups, connections, diagnosis, palette).
- Find, don't scroll. ⌘K command palette switches any node in any group, toggles switches, diagnoses a typed address, jumps pages. Lists are searchable (⌘F in groups).
- Paused is not an error. Wanted-but-paused switches are amber with their reason inline; red is only for failures and timeouts.

## Tokens

All colours come from CSS variables in `desktop/src/styles.css`, exposed to Tailwind via `@theme` (`bg-panel`, `text-fg-2`, `border-line`, `bg-accent-soft` …). Light and dark are both authored; the app follows the system.

| Role | Light | Dark | Use |
| --- | --- | --- | --- |
| bg | #f7f7f8 | #0d0e10 | content ground |
| sidebar | #f0f0f2 | #111214 | navigation column |
| panel / raised | #ffffff | #141518 / #1a1b1f | grouped rows, inspector, popovers |
| line / line-strong | 8% / 14% ink | 7% / 12% white | hairline separators, control borders |
| fg / fg-2 / fg-3 | #16171a / #55585f / #6f727a | #ecedee / #a3a6ad / #80838b | text hierarchy |
| accent (Velum teal) | #0b8f7e | #3fd1bd | protected state, primary action, selection, latency < 300 ms |
| warn | #a3650a | #f0b552 | paused, degraded, 300–800 ms |
| err | #c9343b | #f2676b | errors, timeouts, rejected |
| info | #3b6fd1 | #86a9f7 | rule references, notices |

Soft variants (`*-soft`) are 10–13% tints for chips and selected rows. Shadows: `shadow-raise` for buttons, `shadow-pop` for the palette only.

## Type

System UI stack (SF Pro / PingFang SC; Segoe UI / Microsoft YaHei on Windows), 13 px base. Scale: 11 / 11.5 / 12 / 12.5 / 13 / 15 / 18 / 22 px. Monospace (SF Mono / Consolas) only for data: latency, bytes, times, addresses, rule text and source locations, always with tabular numerals.

## Components (`desktop/src/components/ui.tsx`)

- **Button** primary / secondary / ghost / danger, sm 24 px / md 28 px, optional lucide icon and loading spinner.
- **Switch** 30×18, accent when on, warn when on-but-paused.
- **Tag** 18 px chip in neutral / accent / warn / err / info.
- **Latency** coloured dot + number (`72 ms`, `超时`); never colour alone.
- **Chain** the route chain; step kinds rule (info, mono), group (neutral), node (accent tint), direct, reject (err).
- **Panel + Row** hairline-bordered row groups; never cards inside cards.
- **PageHeader**, **Section**, **KV**, **Empty** (icon + title + hint + action), **Notice**, **SearchInput** (with shortcut hint), **Kbd**.
- Icons: lucide-react, 1.8 stroke. No emoji or Unicode glyphs as icons (emoji only appear inside subscription-provided names).

## Layout

Sidebar 208 px (status mark, ⌘K entry, seven pages with ⌘1–⌘7). Content pages either a centred column (max 720–780 px: overview, diagnosis, warnings, settings) or full-bleed master–detail (groups: 264 px list + detail; connections: table + 340 px inspector). Window min 760×520.

## Motion

150–200 ms colour transitions; switch knob uses an expo ease-out; the protected status mark pulses slowly. Nothing animates on page load. `prefers-reduced-motion` disables all of it.
