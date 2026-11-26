# Tailscale Monitor (`tailmon`) Domain Glossary

This glossary defines the canonical terms used across `tailmon` documentation, backend code, and frontend UI for fleet telemetry and the in-app SSH terminal.

## Core Terms

- **Fleet Node**: A machine reported by `tailscale status --json` (either `Self` or a `Peer`), represented by `Device` in [main.go](file:///Users/adityahimawan/Development/tailscale-monitor/main.go).
- **Probe Target**: An SSH command/alias or local execution path used by `Collector` for non-interactive (`BatchMode=yes`) telemetry collection (OS version, CPU, RAM, disk, load average, uptime).
- **Terminal Target**: An allowlisted, server-resolved shell destination for a Fleet Node—either the local login shell (for `IsSelf`) or an `ssh` command derived strictly from `TAILMON_SSH_*` or `TAILMON_SSH_TARGETS`.
- **Target Allowlist**: The server-side set of valid Terminal Targets. Clients pass only a canonical `device` identifier and can never supply arbitrary hosts, usernames, ports, or SSH flags.
- **Terminal Session**: An ephemeral 1:1 pairing of one authenticated WebSocket connection and one OS PTY + child process (`ssh` or local login shell), managed by `TerminalManager`.
- **PTY (Pseudo-Terminal)**: An OS virtual terminal allocated via `github.com/creack/pty` that provides full interactive TTY semantics (`vim`, `htop`, `tmux`, `Ctrl+C`, and window resize via `SIGWINCH`).
- **Terminal PIN**: An optional secondary secret (`TAILMON_TERMINAL_PIN`) layered on top of `TAILMON_PASSWORD`, verified via `POST /api/terminal/unlock` and stored as an `HttpOnly` HMAC-SHA256 cookie (`tailmon_term_auth`).
- **Control Frame**: A WebSocket text frame carrying JSON (such as `{"type":"resize","cols":N,"rows":M}` or `{"type":"ping"}`), distinct from binary frames which carry raw PTY I/O bytes.
- **Terminal Dock**: The collapsible, vertically resizable, multi-tab bottom drawer in [index.html](file:///Users/adityahimawan/Development/tailscale-monitor/index.html) hosting `xterm.js` instances, expandable to full-screen.
