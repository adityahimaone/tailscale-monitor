# ADR-001: Terminal Backend Engine

- **Status**: Accepted
- **Date**: 2026-11-26

## Context
`tailmon` previously used only the Go standard library (`go.mod` had zero external dependencies). Adding an interactive web-based SSH terminal requires:
1. A bidirectional streaming transport between browser and server (WebSocket).
2. Real pseudo-terminal (PTY) allocation on Linux and macOS so interactive programs (`vim`, `htop`, `tmux`, `Ctrl+C`, terminal resize) work properly.
3. SSH connectivity to Fleet Nodes that matches `tailmon`'s existing `~/.ssh/config`, ProxyJump, and key setup.

## Decision
Use two minimal, focused Go dependencies plus the host `ssh` binary:
- `github.com/creack/pty` for cross-platform (Linux/macOS) PTY allocation and `SIGWINCH` resizing.
- `github.com/coder/websocket` for RFC 6455 WebSocket handling with built-in same-origin checks and context cancellation.
- Host `ssh` binary for remote nodes (inheriting `~/.ssh/config`, aliases, ports, and keys) and the local login shell (`$SHELL -l`) for `IsSelf`.

## Alternatives Considered
- **Pure Go SSH (`golang.org/x/crypto/ssh`)**: Does not automatically honor `~/.ssh/config` host aliases, custom `ProxyJump` rules, or SSH agent/config nuances without reimplementing OpenSSH config parsing, and still requires a local PTY for `IsSelf`.
- **Zero-dependency stdlib**: Hand-rolling RFC 6455 WebSocket framing and platform-specific `openpty`/termios syscalls across `darwin` and `linux` adds unnecessary fragility and maintenance burden.

## Consequences
- `go.mod` gains `github.com/creack/pty` and `github.com/coder/websocket`.
- The host running `tailmon` must have OpenSSH `ssh` installed (already required for `Collector` telemetry probes).
- Cross-compilation (`GOOS=linux GOARCH=amd64`) remains clean without `cgo`.
