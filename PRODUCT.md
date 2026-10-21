# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

The desktop app is a Tauri 2 shell (macOS Apple silicon, Windows x64) rendering a React 19 + Tailwind 4 web UI in `desktop/src`. It ships alongside a tray / menu-bar menu (`desktop/src-tauri/src/tray.rs`). Android is a separate Flutter app (`mobile/`) and is out of scope for desktop design work.

## Users

People who already have a Clash / Stash / mihomo subscription and want a proxy client on their own computer. Many also work on a company network or corporate VPN at the same time. They open the window briefly and often: to check that protection is on, flip the system proxy or TUN, switch a node or run a latency test, and when something misbehaves, to find out why a particular connection went the way it did. Velum is a public, open-source app (GPL-3.0-or-later), not an internal tool.

## Product Purpose

Run a sing-box–based proxy core (`velumd`) from an existing Clash-style subscription, and keep it working when the network changes, without the user having to understand routing. Success: protection stays on through Wi-Fi switches, sleep/wake and corporate VPNs; when a site misbehaves, the user can see in seconds which rule and outbound it took and why.

## Positioning

Velum is the explainable client. Every connection carries the rule that matched it, the source line in the subscription or local overlay that rule came from, and the full outbound chain; `vlu why` / the diagnosis view replays DNS, system-proxy bypass and rule matching step by step. It coexists with split corporate VPNs (their subnets and domains go direct, their DNS is respected) and yields to full-tunnel VPNs and captive portals instead of fighting them. Conversion is honest: anything that cannot be translated exactly from the subscription surfaces as a warning instead of being silently dropped.

## Operating Context

- Main window (980×700 default, min 480×560), macOS overlay title bar; plus the tray menu for quick switching.
- Data comes from velumd's local HTTP API over a Unix socket / named pipe: status (polled every 2s), policy groups with members, selected member and last latency, a live connection stream (SSE), the why/diagnosis report, warnings, overlay file, subscription, helper/service status.
- Typical subscription scale: ~20 policy groups, ~25 nodes each, nested groups (a group's member can be another group), node names with flag emoji and long Chinese labels.
- Windows needs the Velum service installed for TUN; macOS needs `velum-helper` installed (admin password once).

## Capabilities and Constraints

- Switches: system proxy and TUN are independent and can both be on; each may be "wanted but paused" (captive portal, full-tunnel VPN, outdated helper) with a reason string.
- Policy groups: `select` (user picks a member) and `urltest` (automatic, read-only); latency test per group; `-1` means timeout.
- Connections: domain/destination, process, matched rule index + provenance (file:line + text), outbound chain, bytes, first-byte time, errors; REJECT is expected, not an error.
- Diagnosis: system proxy step, DNS step, rule trace (matched / not matched with reason / unknown), final outbound, notes, compared against the last real connections to the same host.
- Local overlay: YAML editor (dev direct, hosts override, system-proxy bypass); saved with validation and automatic rollback.
- Warnings: grouped by category, levels "degraded" and "unsupported", each with source location.
- System components: TUN component install/update/uninstall, `vlu` CLI install, login item.
- Copy is Chinese (zh-CN). No outbound "mode" (rule/global/direct) switch exists in the core.

## Brand Commitments

- Name: Velum. App icon at `desktop/app-icon.png`; monochrome template tray icon at `desktop/src-tauri/tray-template.png`.
- Must not imitate or claim association with sing-box, Clash, Stash or Surge (README states Velum is independent of sing-box and its authors).
- The user asked for a recognisable visual identity of Velum's own, not a stock macOS look (2026-10-21).

## Evidence on Hand

Real data is available from the user's running velumd in development (`bun run dev` proxies `/api` to the socket). No testimonials, user counts, benchmarks or press exist; none may be invented.

## Product Principles

1. One answer first: the window opens on whether protection is working and which line traffic is taking; detail is one step away, never in the way.
2. Explain, don't just show: every route decision links to the rule and the source line that caused it.
3. Find in seconds: with hundreds of nodes, choosing or testing a node is a search-and-pick task, not a scroll.
4. Coexist politely: paused states and their reasons are first-class, never rendered as errors.
5. Honest conversion: degraded or unsupported subscription features stay visible.

## Accessibility & Inclusion

No product-specific standard established. Follow the platform floor: keyboard reachable controls, visible focus, sufficient contrast in light and dark, status never conveyed by colour alone.
