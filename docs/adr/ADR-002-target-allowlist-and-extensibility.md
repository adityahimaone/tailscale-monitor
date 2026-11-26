# ADR-002: Target Allowlist and Extensibility

- **Status**: Accepted
- **Date**: 2026-11-26

## Context
The dashboard displays all nodes in the tailnet, including phones, offline nodes, and nodes without SSH access. Exposing an arbitrary `user@host` SSH form in a web UI would expand the attack surface to SSRF/pivoting if credentials were ever compromised.

## Decision
Restrict the in-app terminal to **pre-configured Fleet Nodes only**, resolved strictly on the server:
- Keep existing `TAILMON_SSH_MAC`, `TAILMON_SSH_TOKYO`, `TAILMON_SSH_INSTABOX`, and `TAILMON_SSH_VPS` variables, plus `IsSelf` local shell.
- Add `TAILMON_SSH_TARGETS`, a comma-separated `host=ssh_target` map (e.g., `db-vps=root@100.64.0.5,worker=ssh -p 2222 ubuntu@worker-ts`) so new VPS or Linux/macOS nodes can be onboarded without code changes.
- Clients pass only a canonical `device` identifier (`GET /api/terminal/ws?device=...`); the server matches it against the allowlisted Fleet Nodes and never accepts raw hostnames, usernames, or SSH flags from the client.

## Alternatives Considered
- **Any online Tailscale node with fallback to `user@tailscale-ip`**: Unpredictable usernames across nodes and exposes nodes not intended for shell access.
- **Arbitrary ad-hoc SSH form**: High security risk (allows pivoting to arbitrary internal/external hosts).

## Consequences
- Every new SSH-accessible node must be configured in `TAILMON_SSH_TARGETS` (or `TAILMON_SSH_*`) and appear in `tailscale status --json`.
- Zero risk of client-side SSH argument or target injection.
