# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [SemVer](https://semver.org/).

## [1.9.1] - 2026-11-13

### Added
- **Zero-config direct agent peering**: encrypted Python/Go peer protocol, discovery, persistent pairing, session grants, target-side human approval, denial and revocation.
- **Native product operations**: `lanbus` CLI, daemon installation, backup/restore, optional hub TLS and loopback-only pprof.
- **Native peer approval entry**: `lanbus peer-ui --name NAME` opens the local approval page without Python and shares loopback-only metadata parsing with the Windows tray.
- **Release packaging**: tag-driven Windows Setup EXE/portable ZIP, macOS PKG, Linux DEB, raw binaries, wheel and SHA-256 checksums, with test, version and required-artifact gates.

### Changed
- Python wheels read the single `__version__` source dynamically; release tags inject the same version into Go binaries.
- Windows portable `lanbus.cmd` now invokes the bundled native `lanbus.exe` instead of requiring Python.
- Python regression suite expanded to 91 tests, including release-asset checks.

### Fixed
- Stabilized direct peer execution, agent adapters, cross-machine conversations and peer-state handling.
- `lanbus healthcheck` now rejects non-200 responses, invalid or non-object JSON, missing `ok`, and `ok=false` instead of treating any HTTP response as healthy.

## [1.9.0] - 2026-11-05

### Added
- **Phase 2a Go hub (`lanbusd`)**: native health, worker register/heartbeat/claim/progress/log/submit/recover/leave, MCP tools `lan_dispatch`, `lan_envs`, `lan_list`, `lan_result`, `lan_await`, `lan_logs`, `lan_review`, `lan_cancel`, `lan_interrupt`, `lan_artifact`.
- **SQLite task store** via `modernc.org/sqlite`, lease reaper, claim tokens, idempotent submit, review/rework, cancellation.
- **SSE** `/events` and artifact storage/download under `<data-dir>/artifacts`.
- **Dual-run compatibility test**: the same Python fake worker runs against the Python hub and `lanbusd`; status/exit/verify/result are compared.
- **Go hub + Go worker integration** verified locally.
- **Phase 3 desktop shell**: `go/cmd/lanbus-gui` Wails/WebView2 window + systray (open panel / browser / quit), embedded dashboard, token auto-injection, close-to-tray.
- **Browser worker regression**: local `win-browser` Python worker; `lan_shot` produces PNG and Go hub returns MCP image content.
- **Go hub image results**: `lan_result`/`lan_artifact` return MCP image blocks.
- **Human confirmation gate**: remote targets require a separate operator approval before dispatch/chat; `waiting_confirmation` state; `lanbus confirm` CLI; dashboard approval panel; agent token cannot approve.
- **`lanbus web` / `lanbus gui`** commands; portable Windows package, Inno Setup script, signing helper, SHA-256 update helper.

### Changed
- Makefile `go-hub` / `go-hub-all`; `go-gui`; CI builds `lanbusd`.
- Python/Go versions 1.9.0; tests now 39.
- Go hub now implements `lan_chat`, `lan_discover`, `lan_ask`, `lan_shot`, `lan_conversations`, `lan_dispatch_many`, and serves `/ui`; it is the default on port 8788. Python hub remains a fallback.

## [1.8.0] - 2026-11-04

### Added
- **PTY mode**: Go worker `--pty` uses ConPTY on Windows and Unix PTY elsewhere via `github.com/aymanbagabas/go-pty`; Job Object/process-group cancellation still applies. PTY smoke test runs in `go test`.
- **Web dashboard**: `GET /ui` serves a self-contained conversation UI; `GET /api/envs`; token stays in browser localStorage. Conversation list/detail/start/resume use the existing authenticated APIs.
- **Local worker supervisor**: `lanbus local init|up|down|status`; config at `~/.lanbus/local.json`; skips envs already online; supports Go/Python workers and WSL entries; PID/log files under `~/.lanbus/local/`.
- **Startup templates**: hidden Windows startup script + example config/docs.

### Changed
- Go worker version 1.8.0; `go.mod` requires Go 1.25 (go-pty); CI Go updated to 1.25.
- Python tests now 35 (web UI + local supervisor).
- Version 1.8.0; docs/changelog updated.

## [1.7.0] - 2026-11-04

### Added
- **Persistent conversations**: `conversations` table stores title, envs, seed, context, rounds, turns, and status.
- **Resume**: `lan_chat(conversation_id=...)` appends new rounds while preserving prior transcript and task IDs.
- **Read/list**: `lan_conversations` MCP tool, `GET /conversations`, `GET /conversations/<id>`, and `lanbus chat --list`.
- **CLI**: `--conversation ID` for continuing, `--list` for history.
- **Tests**: chat test now covers persistence, listing, and resume (same 33-test suite).

### Changed
- `lan_chat` schema requires `message`; `envs` is required only for a new conversation.
- Version 1.7.0; docs/changelog updated.

## [1.6.0] - 2026-11-04

### Added
- **Local multi-agent chat**: `lan_chat` MCP tool, `POST /chat`, and `lanbus chat` CLI. The hub relays 2–4 environments through 1–6 rounds, feeding the prior transcript into each turn.
- **Conversation cleanup**: strips Claude/Kimi CLI boilerplate and DeepSeek reasoning prefixes, keeping the dialogue body.
- **Test**: real hub + two fake workers cover turn order, transcript handoff, and task IDs; suite now 33 tests.

### Changed
- CLI usage and MCP tool set now include `lan_chat`.
- Docs, changelog, publishing notes updated; version 1.6.0.

## [1.5.0] - 2026-11-04

### Added
- **Windows Job Objects** in the Go worker: child processes are assigned to a job with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; `lan_interrupt` uses `TerminateJobObject` and closing the worker also kills surviving children.
- **Native Go worker `discover` task**: reuses `internal/scanner`, returns `discovery.json`, and registers the `discover` capability.
- **Native Go worker `ask` task**: OpenAI-compatible `/v1/models` + `/v1/chat/completions`, Ollama `/api/tags` + `/api/chat` + `/api/generate`, optional Bearer token/TLS; returns `ask.json`.
- **Go tests** for OpenAI and Ollama ask paths.
- **Job Object / process crash test**: killing the worker also terminates its child process tree.

### Changed
- Go worker version string 1.5.0; Windows children use `CREATE_NO_WINDOW`.
- Worker remains compatible with the Python hub; `browser` tasks stay on the Python worker until ConPTY/PTY work.
- Docs/README/CHANGELOG updated; version 1.5.0.

## [1.4.0] - 2026-11-04

### Added
- **Initial Go worker (Phase 1)**: `go/cmd/lanbus-worker` + `go/internal/worker`, speaking the existing hub protocol. register/claim/progress/log/submit/heartbeat/recover/outbox, streamed logs, prompt composition, git diff/status, inline base64 artifacts.
- **Native cancellation**: Windows process-tree kill via `taskkill /T`; Unix process-group SIGTERM/SIGKILL. `lan_interrupt` works while the window is unfocused/backgrounded.
- **Go worker adapters**: claude, codex, kimi, deepseek, shell, echo, custom `--cmd` with `{prompt_file}`.
- **Cross-built worker binaries**: Windows amd64, macOS arm64/amd64, Linux amd64.
- **Go tests** for the worker artifact glob matcher and scanner.
- **CI** builds/tests the Go scanner and worker.

### Changed
- Phase 1 marked initial in `docs/go-migration.md`; Python worker remains the fallback for browser/discover/ask.
- Version 1.4.0; docs/README updated.

## [1.3.0] - 2026-11-04

### Added
- **Native Go scanner (Phase 0)**: `go/cmd/lanbus-scan` + `go/internal/scanner` implement interfaces, ARP, local process/listener detection, WSL processes, TCP scan, HTTP(S) fingerprints, OpenAPI AI APIs, os_guess and the same report shape as Python.
- Python `discovery.py` auto-detects `bin/lanbus-scan*`, `lanbus-scan` on PATH, `LANBUS_SCANNER_BIN`, or `--scanner-bin` / `scanner_bin`; Python remains the fallback.
- `make go-scan`, `make go-scan-all`, `make go-test`; Go scanner tests.
- Cross-built scanner artifacts for Windows amd64, macOS arm64/amd64, Linux amd64.

### Changed
- `docs/go-migration.md` Phase 0 marked implemented; `go/README.md` documents the scanner.
- Version 1.3.0; docs and tests updated.

## [1.2.1] - 2026-11-04

### Added
- **`lan_interrupt`** MCP tool + `lanbus interrupt` CLI: explicit Ctrl+C-equivalent for lanbus tasks. It uses the worker cancellation path and kills the whole process tree, independent of window focus/background state.
- **Kimi worker adapter**: `--agent kimi` uses `lanbus.prompt_cli` to pass the prompt file as one argv to `kimi -p`, avoiding shell quoting and newline issues on Windows/macOS.
- **DeepSeek worker adapter**: `--agent deepseek` uses `dsh headless -` over stdin.
- **macOS worker templates**: `examples/macos/com.lanbus.worker.plist`, `com.lanbus.worker-go.plist`, and `start-worker.sh`.
- **OS guess** in discovery: AirTunes/AirPlay ports (`5000/7000/62078/548`) mark a host as `macos`; SMB/RDP ports mark Windows.
- **Tests**: 32 tests, including `lan_interrupt` and OS guess.

### Changed
- Task execution and interruption are explicitly worker/process-tree based; GUI keyboard injection is documented as best-effort notification only.
- macOS Kimi CLI no-response troubleshooting: run a worker on the Mac or `lanbus discover` locally; a Windows scan only sees open ports.
- Version 1.2.1; docs, examples and changelog updated.

## [1.2.0] - 2026-11-04

### Added
- **Zero-preinstall LAN AI agent discovery** (`discovery.py`): local process scan (Claude/Codex/Cursor/Kimi/DeepSeek/Ollama/LM Studio/...) with executable paths and Windows WSL process scopes, local listener→PID mapping, `/24` TCP scan with AI/dev ports, and HTTP fingerprints for Ollama, OpenAI-compatible, LM Studio, Open WebUI, ComfyUI, llama.cpp, vLLM, Gradio, Jupyter, Chrome DevTools, MCP `serverInfo`, lanbus `/health`, and FastAPI/OpenAPI AI APIs.
- **MCP tools**: `lan_discover` (local or `env`-remote via `kind=discover`) and `lan_ask` (prompt a discovered Ollama/OpenAI-compatible endpoint without installing anything there; `env` runs from that worker's network).
- **CLI**: `lanbus discover [--cidr] [--ports] [--deep] [--json] [--watch]`, `lanbus ask --endpoint ... --prompt ...`.
- **Worker capability `discover`**: discovery runs inside a worker and uploads the full `discovery.json` artifact; `required_caps` prevents old workers from claiming it.
- **Per-worker tokens** (`--worker-tokens` / `--worker-tokens-file`) and worker-restart `previous_task` force recovery.
- **Tests**: 6 discovery tests (fake Ollama/OpenAI/MCP/lanbus services, MCP scan, direct ask, remote worker scan).

### Changed
- Discovery report explicitly separates local process evidence from remote network evidence and states the GUI-only limitation.
- Version 1.2.0; docs/README/integrations updated.

## [1.1.0] - 2026-11-04

### Fixed
- **Cancellation now reaches the remote process.** `lan_cancel` writes a cancel flag and the worker polls `/worker/progress`; on cancel it kills the entire process tree (`taskkill /T` on Windows, process group on POSIX). A late `/worker/submit` is rejected, so a cancelled task cannot be resurrected as `submitted`/`failed`.
- **Stale workers cannot overwrite recovered tasks.** Every claim gets a random `claim_token`; progress/log/submit must match both worker name and token. Requeued or cancelled tasks reject the old execution's evidence.
- **Duplicate submits are idempotent.** A retried `/worker/submit` no longer downgrades a `submitted` task to `failed`.
- **Empty-`env` tasks are claimable.** A task dispatched without `env` is now matched by any worker (previously it could never be claimed).
- **Artifacts from separate machines.** Workers upload files inline as base64; the hub no longer needs to read the worker's local filesystem. Subdirectories (`dist/icon.png`) and MIME types are preserved.
- **Windows short-path / symlink artifact names.** Relative names are computed from `abspath`, not `realpath`, so nested artifacts keep their path.
- **`.log`/`.txt` artifacts are treated as text**, not opaque binary.

### Added
- **Real-time logs**: `/worker/log` batches + `task_logs` table + `lan_logs` MCP tool + `GET /tasks/<id>/logs` + `task.log` SSE events + `lanbus logs --follow`. A local copy lands in `~/.lanbus/logs/`.
- **Lease, reaper and auto-requeue**: `lease_expires_at`, `/worker/progress`, configurable `lease_s`, `max_recoveries`, `task.recovered` events, and `/worker/recover` on worker restart.
- **Reliable submission**: local outbox under `~/.lanbus/outbox/<name>/`, exponential retry, background flush.
- **Context handoff**: `lan_dispatch.context`, worker-composed prompt with `.lanbus/context.md`, `.lanbus/rules.md`, `--context-file`, git branch/status, verify command, artifact globs, feedback and non-interactive constraints; `lan_result.composed_prompt` exposes exactly what the remote agent saw.
- **Artifact pipeline**: task `artifact_globs`, worker `--artifact-glob`, default screenshot/build-log patterns, authenticated `GET /artifacts/<task_id>/<name>`, `lan_artifact`, and image artifacts returned as MCP `image` content.
- **Browser capability**: built-in `browser.py` with Playwright actions (`goto/click/fill/press/select/check/wait/evaluate/reload/screenshot/pdf`) and Chrome/Edge headless screenshot fallback; `lan_shot` MCP tool; `required_caps=["browser"]` scheduling; `lan_envs.has_browser`.
- **Role-separated tokens**: `--worker-token` / `LANBUS_WORKER_TOKEN`; worker token cannot access MCP/read endpoints; optional per-worker `--worker-tokens` (`{name: token}`) binds an identity to one worker name; optional `--allowed-envs`.
- **Optional TLS**: hub `--tls-cert`/`--tls-key`; worker `--ca`, `--insecure`.
- **Operational CLI**: `lanbus doctor`, `lanbus logs`, `lanbus shot`, `lanbus browser --detect`.
- **Tests**: 9 new feature tests covering live logs, real cancel, context composition, cross-machine artifacts + image content, `lan_shot` task shape, lease recovery, worker-restart force recovery, claim-token/stale-submit rejection, and role-separated tokens.

### Changed
- Hub schema auto-migrates old databases (`ALTER TABLE` for new columns); old `~/.lanbus/bus.db` keeps working.
- `/health` now reports all statuses plus version.
- `lan_envs` returns parsed `caps`, `has_browser`, and worker version/PID/hostname.
- Task scheduling respects `priority`, `required_caps`, then retry/created time.
- README, protocol, architecture, troubleshooting and Claude Code integration updated for the new workflow.

## [1.0.0] - 2026-11-04

### Added
- **Hub** (`lanbus hub` / `lanbus-hub`): stdlib-only HTTP server exposing MCP (stateless streamable HTTP) to agents plus a REST surface for workers. SQLite (WAL) task store, per-task artifact directory, SSE event stream, `/health` counters.
- **Worker** (`lanbus worker` / `lanbus-worker`): outbound-only pull worker. Takes a queued task, runs a local agent CLI, executes the task's acceptance command, collects `git diff` / `git status`, submits agent output + verify result + diff + artifacts.
- **Review loop**: `lan_review(approve|rework|fail)`. `rework` requeues with `retry + 1` and appends the reviewer's feedback to the next prompt; exceeding `max_retry` fails the task instead of looping.
- **Fan-out**: `lan_dispatch_many` dispatches to several environments in one call.
- **Agent adapters**: `claude` (`claude -p --permission-mode acceptEdits`), `codex`, `shell`, `echo`, plus arbitrary `--cmd` with `{prompt_file}` substitution or stdin delivery.
- **Auth**: mandatory bearer token on all agent/worker endpoints; token auto-generated on first run.
- **Host allowlist**: loopback plus IP literals only, blocking DNS-rebinding style access.
- **Claude Code integration**: `.mcp.json.example`, `lan-dispatcher` and `lan-verifier` subagent definitions, `/lan` slash command.
- **Packaging**: `pyproject.toml` (PEP 621), console scripts, wheel build, Dockerfile, docker-compose, Makefile.
- **Tests**: protocol + full dispatch → submit → rework → redo → approve → fan-out loop against a real hub and two real workers.
- **CI**: GitHub Actions matrix across Ubuntu/Windows/macOS plus a wheel build with `twine check` and an install smoke test.

### Known limitations
- Remote agents cannot be Claude Code child processes; the local subagent is a dispatcher shell and remote work runs via `claude -p`, so prompts must be self-contained.
- Single hub, no HA; SQLite is the source of truth.
- No built-in TLS — deploy behind WireGuard/Tailscale or a reverse proxy.
