# ADR-004: Terminal UI and Vendored xterm.js

- **Status**: Accepted
- **Date**: 2026-11-26

## Context
Operators monitoring the fleet often want to inspect logs or run quick commands on one or more nodes without leaving the dashboard or losing sight of fleet metrics. Additionally, `tailmon` ships as a single self-contained binary with zero runtime external build steps.

## Decision
1. **Multi-Tab Terminal Dock**: Add a collapsible, vertically resizable bottom drawer (`#termDock`) in [index.html](file:///Users/adityahimawan/Development/tailscale-monitor/index.html) that can also expand to full-screen. Device cards expose a `Terminal` action button when `terminal_enabled && online && terminal_available`, and the dock tab bar includes a `+` host picker for opening multiple tabs (including multiple tabs to the same host).
2. **Vendored & Embedded `xterm.js`**: Vendor pinned `xterm.js` (`@xterm/xterm@5.5.0`), `@xterm/addon-fit@0.10.0`, and `@xterm/addon-web-links@0.11.0` into `assets/xterm/` and embed them via `//go:embed assets/xterm/*`.

## Alternatives Considered
- **Full-page modal / dedicated `/terminal` route**: Hides the live telemetry cards or scatters sessions across browser tabs.
- **Loading `xterm.js` from a public CDN at runtime**: Introduces external network dependency and fails in air-gapped or restrictive environments.

## Consequences
- Binary size increases by ~300 KB for vendored `xterm.js` assets, preserving single-binary deployment.
- Global dashboard keyboard shortcuts (`/` search, `Escape`) must explicitly ignore events when focus is inside `#termDock` or `#termPinModal`.
