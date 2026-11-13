# Documentation

Bilingual entry points:

- [Overview (English)](overview.en.md)
- [总览（中文）](overview.zh-CN.md)

Reference:

- [Architecture](architecture.md) — direct company network (symmetric peers,
  membership, policies) plus hub-mode components, pull model, state machine,
  lease recovery, logs, evidence, artifacts, discovery, PTY, approvals, desktop shell.
- [Protocol](protocol.md) — direct peer protocol (`/peer/*`, port 8790), hub
  worker REST, MCP tools, SSE, artifacts, auth.
- [Troubleshooting](troubleshooting.md) — common Windows/WSL/macOS/agent issues.
- [Windows 实机验证清单](windows-validation.md) — 构建、双 Hub／Worker、状态机、恢复与安全验证；含 `scripts/validate-windows.ps1` 一键自动验证入口。
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
