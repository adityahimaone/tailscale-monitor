# ADR-005: Session Lifecycle and SSH Authentication

- **Status**: Accepted
- **Date**: 2026-11-26

## Context
Web terminal sessions consume OS file descriptors, PTYs, and child processes. We need predictable resource cleanup, clear persistence semantics, and support for SSH hosts that may prompt for a password, passphrase, or first-time host key acceptance.

## Decision
1. **Ephemeral 1:1 WebSocket-to-PTY Lifecycle**: Each WebSocket connection spawns one PTY + child process. Closing the tab, disconnecting the WebSocket, or hitting the idle timeout terminates the PTY and sends `SIGHUP` (then `SIGKILL` after 2s if needed) to the child process group. Operators needing persistent sessions across disconnects should use `tmux` or `screen` on the target host.
2. **Resource Bounds**: Enforce an idle timeout (`TAILMON_TERM_IDLE_TIMEOUT`, default `30m`) and a maximum concurrent sessions cap (`TAILMON_TERM_MAX_SESSIONS`, default `6`).
3. **Interactive SSH**: Do **not** pass `BatchMode=yes` to interactive terminal `ssh` invocations (unlike `Collector` probes). Pass `-tt -o StrictHostKeyChecking=accept-new -o ServerAliveInterval=15 -o ServerAliveCountMax=3` so interactive passphrase/password prompts work inside `xterm.js`.

## Alternatives Considered
- **Persistent server-side PTY sessions with scrollback ring buffers**: Adds complex session-reattach state, memory retention, and orphan-process risk for minimal gain when `tmux` already solves this cleanly on Linux/macOS.
- **`BatchMode=yes` for terminal**: Breaks hosts that use password auth or encrypted SSH keys without an agent.

## Consequences
- Server resources are bounded and automatically reclaimed on disconnect or idle timeout.
- Refreshing the browser tab closes active web terminal connections (matching standard ephemeral web-console behavior).
