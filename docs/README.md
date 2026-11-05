# Documentation

Bilingual entry points:

- [Overview (English)](overview.en.md)
- [总览（中文）](overview.zh-CN.md)

Reference:

- [Architecture](architecture.md) — components, pull model, state machine, lease
  recovery, logs, evidence, artifacts, discovery, PTY, approvals, desktop shell.
- [Protocol](protocol.md) — worker REST, MCP tools, SSE, artifacts, auth.
- [Troubleshooting](troubleshooting.md) — common Windows/WSL/macOS/agent issues.
- [Go migration](go-migration.md) — why the product core is Go and what remains.
- [Cross-machine case study](case-studies/cross-machine-test.md) — a real
  Windows ↔ macOS run with registration, dialogue, remote shell, and interrupt.
- [Go README](../go/README.md) — native scanner, worker, hub, and GUI.
- [Packaging](../packaging/windows/README.md) — portable ZIP, Inno Setup, signing,
  updater.
- [MCP client preset](../integrations/README.md) — optional sub-agent prompts and
  slash-command text for any MCP client.
- [Examples](../examples) — local supervisor, macOS launchd, WSL systemd,
  Windows scheduled task.
