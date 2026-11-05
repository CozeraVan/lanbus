# Security Policy

## Supported versions

Security fixes target the latest `1.9.x` release and `main`.

## Reporting a vulnerability

Please do **not** open a public issue for a vulnerability involving:

- token bypass or operator-approval bypass;
- arbitrary command execution beyond a registered worker;
- path traversal in artifacts;
- credential exposure in logs, URLs, or the dashboard;
- a way to send tasks to an agent the operator did not approve.

Use GitHub's **Security → Report a vulnerability** flow, or contact the
maintainer privately. Include affected version, reproduction steps, and impact.

## Security model

- The hub is intended for a **private LAN / trust domain**, not the public
  internet. Put it behind a VPN or reverse proxy with TLS if needed.
- `token.txt` is the agent/MCP token. `operator-token.txt` is the human approval
  token. Do not commit either.
- Worker tokens grant task execution on that worker. Treat them as credentials.
- Approval is scoped to an env and expires. `lan_chat` refuses remote
  participants without an active approval.
- Local envs (same hostname as the hub) are trusted by default.
- A GUI-only application with no worker is not remotely controllable by lanbus.
