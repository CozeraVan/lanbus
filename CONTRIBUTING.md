# Contributing

## Setup

```bash
git clone https://github.com/CozeraVan/lanbus && cd lanbus
python -m pip install -e ".[dev]"
pytest -v
```

Python 3.9+ on Windows, Linux, or macOS. No runtime third-party dependencies — please keep it that way; the hub must be droppable onto a bare machine.

## Ground rules

1. **Stdlib only at runtime.** If a feature needs a dependency, it belongs in an optional extra or in a separate adapter.
2. **Tests drive real processes.** `tests/` starts an actual hub and actual workers on free ports and speaks real MCP JSON-RPC. Do not replace this with mocks; the parts that break in production are process boundaries and HTTP semantics.
3. **The review gate stays one-way.** A worker must never be able to approve its own task. Any change that lets a worker reach `lan_review` is rejected.
4. **Add a test with the change.** New tool → protocol test in `tests/test_protocol.py`. New state transition → `tests/test_e2e.py`; logs/cancel/lease/artifacts/roles → `tests/test_features.py`.
5. **Wire-compatible by default.** Adding a tool is fine; renaming or removing one, or changing a status name, needs a major version.

## Layout

```
src/lanbus/hub.py       MCP server + worker REST + SQLite + SSE + lease reaper
src/lanbus/worker.py    pull worker + streaming logs + cancellation + outbox
src/lanbus/browser.py   Playwright/headless browser helper for lan_shot
src/lanbus/discovery.py  zero-preinstall LAN AI-agent reconnaissance (Python fallback)
go/cmd/lanbus-scan/     native Go scanner entry point
go/cmd/lanbus-worker/   native Go worker entry point
go/internal/scanner/    Go interfaces/ARP/process/listener/TCP/HTTP fingerprinting
go/internal/worker/     Go worker protocol/logs/process-tree/artifacts
src/lanbus/cli.py       `lanbus {hub,worker,doctor,logs,shot,browser}`
tests/                  protocol + end-to-end + logs/cancel/recovery/artifacts/discovery
go/                     Go scanner with its own tests (`make go-test`)
integrations/           client-side MCP preset (agent prompts, slash commands, mcp config)
docs/                   architecture, protocol, troubleshooting
```

## Pull requests

- One concern per PR, with the test that proves it.
- `pytest -v` green on your platform; CI runs the matrix.
- If you touch the HTTP surface, update `docs/protocol.md` and both READMEs in the same commit.

## Reporting bugs

Include: OS, Python version, `lanbus --version`, which side failed (hub / worker / client), and the relevant log lines. For worker issues, the agent command (`--cmd` or `--agent`) matters: different CLIs have different non-interactive flags.