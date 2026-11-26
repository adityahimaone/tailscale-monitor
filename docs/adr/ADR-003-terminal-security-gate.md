# ADR-003: Terminal Security Gate

- **Status**: Accepted
- **Date**: 2026-11-26

## Context
Read-only fleet metrics are relatively low risk, whereas an interactive shell grants full command execution on the VPS and connected nodes. `tailmon` allows running without `TAILMON_PASSWORD` in local dev mode.

## Decision
Enforce layered security gates for all terminal functionality:
1. **Hard-disable when auth is off**: If `TAILMON_PASSWORD` is empty, the terminal is completely disabled—no WebSocket upgrades are accepted (`403 Forbidden`), `snapshot.terminal_enabled` is `false`, and the UI hides all terminal controls.
2. **Optional Secondary PIN (`TAILMON_TERMINAL_PIN`)**: When configured, users must also unlock the terminal via `POST /api/terminal/unlock`, which verifies the PIN in constant time and issues an `HttpOnly`, `SameSite=Lax` HMAC-SHA256 cookie (`tailmon_term_auth`, domain-separated via `"tailmon-term-pin-v1"`).
3. **Same-Origin WebSocket Enforcement**: `websocket.Accept(w, r, nil)` rejects cross-origin WebSocket handshakes to prevent CSWSH (Cross-Site WebSocket Hijacking).
4. **Authenticated Assets**: Vendored `xterm.js` assets under `/assets/xterm/` also sit behind `isAuthenticated`.

## Alternatives Considered
- **Always allow terminal even without `TAILMON_PASSWORD`**: Unsafe if `tailmon` is accidentally bound to a non-loopback interface.
- **Mandatory separate password always**: Adds friction when `TAILMON_PASSWORD` is already strong; making `TAILMON_TERMINAL_PIN` optional gives operators the choice.

## Consequences
- Operators deploying to production must set `TAILMON_PASSWORD` (and optionally `TAILMON_TERMINAL_PIN`).
- Terminal sessions cannot be triggered from third-party origins or unauthenticated requests.
