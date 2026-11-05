# lanbus

[![CI](https://github.com/CozeraVan/lanbus/actions/workflows/ci.yml/badge.svg)](https://github.com/CozeraVan/lanbus/actions/workflows/ci.yml)

**One agent entry. Sub-agents everywhere else on your LAN.**

[English](README.md) · [中文](README.zh-CN.md) · [Overview](docs/overview.en.md) · [中文总览](docs/overview.zh-CN.md) · [Architecture](docs/architecture.md) · [Protocol](docs/protocol.md)

You talk to a single main agent - an **A agent CLI**, a **B agent CLI**, a **C agent desktop**, or anything that speaks MCP. It dispatches work to agents running on your other machines — Windows for the Web UI, WSL for the Linux build, macOS for native packaging — then **verifies what actually changed** and either accepts the result or sends it back for rework.

Zero mandatory dependencies. Standard library only. Never leaves your LAN. Optional Playwright unlocks real browser actions; Chrome/Edge headless gives fallback screenshots. A built-in scanner finds AI agents already running on the LAN **without installing anything on them**.

```
                    you ──► main agent (A agent CLI)
                                  │ MCP: lan_dispatch / lan_logs / lan_result / lan_review
                                  ▼
                        ┌───────────────────┐
                        │  lanbus hub :8788 │  SQLite task table + state machine
                        └───┬───────────┬───┘
              REST (pull)   │           │   REST (pull)
                        ┌───▼────┐  ┌───▼────┐
                        │ win-ui │  │ mac-ui │
                        │windows │  │ macos  │
                        └───┬────┘  └────────┘
                            │ same machine
                        ┌───▼─────┐
                        │wsl-core │
                        │  wsl    │
                        └─────────┘
```

Workers **pull** from the hub, so only the hub machine needs an inbound port. Sub-environments need no firewall rules at all.

---

## Why this exists

Running agents in three environments usually means being the human message bus: you ask one agent for a task, then copy its text into the other agent's window, then eyeball the result yourself. lanbus replaces that loop with machine-checkable facts:

| Without lanbus | With lanbus |
|---|---|
| you paste the task across | `lan_dispatch(env, prompt, context, cwd, verify)` |
| the remote agent says "done" | `verify_ok` = exit code of **your** acceptance command, run on the remote machine |
| you trust the summary | `diff` + `changed` collected from the real working tree |
| you judge by vibes | `lan_review(approve \| rework, feedback)` — feedback is appended to the retry prompt |
| you cannot see the remote run | `lan_logs` / `lanbus logs --follow` streams stdout+stderr live |
| "cancel" only changes a database row | `lan_cancel` signals the worker, which kills the whole process tree |
| a crashed worker strands the task | lease + reaper requeue it, with a new claim token |

A remote agent physically cannot approve its own work: only the caller can call `lan_review`.

---

## First: who is already on the LAN?

```bash
lanbus discover                 # local processes + services + local /24
lanbus discover --cidr 192.168.1.0/24 --json
lanbus discover --hub http://HUB:8788 --env windows
```

It combines three evidence sources:

1. **local processes** — agent CLI and desktop processes (A agent CLI, B agent CLI, C agent desktop, local model runtimes, image/UI tools), matched by executable name and command line; on Windows it also reads running WSL processes (`scope=wsl:<distro>`);
2. **local listening ports** — PID/process/port mapping;
3. **LAN scan + HTTP fingerprints** — local model runtimes (`/api/tags`), OpenAI-compatible `/v1/models`, chat/web UIs, inference servers, notebook servers, browser DevTools, MCP `serverInfo`, lanbus `/health`, and FastAPI/OpenAPI AI APIs (`/openapi.json`).

A native Go scanner is included as Phase 0; Python falls back to the pure-Python scanner automatically. Build it once with `make go-scan` (or `make go-scan-all` for macOS/Linux cross-builds). The report includes `engine: "go"` or `engine: "python"`.

A native **Go worker** is also available as Phase 1.5. It speaks the same hub protocol, streams logs, uploads artifacts, and handles `lan_interrupt` by killing the process tree:

```bash
bin/lanbus-worker --name go-win --env windows --hub http://HUB:8788   --token WORKER_TOKEN --cwd "D:\proj" --agent ADAPTER
```

Build with `make go-worker` / `make go-worker-all`. The Go worker now handles `agent`, `shell`, `discover`, and `ask` tasks, including native LAN discovery and OpenAI-compatible runtime calls. Browser tasks remain on the Python worker.

Add `--pty` to run agents on a real PTY (ConPTY on Windows, Unix PTY elsewhere):

```bash
bin/lanbus-worker --name tui --env tui --hub http://HUB:8788   --token TOKEN --cwd D:/proj --agent ADAPTER --pty
```

PTY mode keeps full-screen/TUI CLIs working and still uses the same Job Object/process-group cancellation.

### Native Go hub (Phase 2)

A native hub subset is available as `lanbusd`:

```bash
make go-hub
bin/lanbusd --bind 127.0.0.1 --port 8789 --data-dir /tmp/lanbus-go   --token TOKEN --worker-token TOKEN
```

It implements the worker REST protocol (register/claim/progress/log/submit/recover),
SQLite persistence, MCP `lan_dispatch` / `lan_dispatch_many` / `lan_envs` /
`lan_list` / `lan_result` / `lan_await` / `lan_logs` / `lan_review` / `lan_cancel` /
`lan_interrupt` / `lan_artifact` / `lan_chat` / `lan_conversations` /
`lan_discover` / `lan_ask` / `lan_shot`, SSE `/events`, artifact storage, and `/ui`.

Dual-run is covered by `tests/test_go_hub.py`: the same Python fake worker runs
against the Python hub and `lanbusd`, comparing status, exit code, verify flag,
and task result. The Go hub is now the **default** on port 8788; the Python hub
remains available as a fallback (`lanbus hub` / `start-hub-python.cmd`) for
browser-task and legacy compatibility.

### Local multi-agent chat

Start one worker per local agent - a Linux-side A agent CLI, a Windows-side B agent CLI, a C agent desktop, a WSL agent - each with its own env name, then let the hub relay a conversation:

```bash
lanbus worker --name win-a-cli --env win-a-cli --hub http://127.0.0.1:8788 --agent ADAPTER_A --cwd D:/proj
lanbus worker --name win-b-cli --env win-b-cli --hub http://127.0.0.1:8788 --agent ADAPTER_B --cwd D:/proj
lanbus worker --name win-c-ui  --env win-c-ui  --hub http://127.0.0.1:8788 --agent ADAPTER_C --cwd D:/proj

lanbus chat --envs win-a-cli,win-b-cli,win-c-ui --message "introduce your strongest capability" --rounds 2
```

The same feature is the MCP tool `lan_chat` (2–4 envs, 1–6 rounds) and `POST /chat`. Every turn is a normal lanbus task, so `lan_logs`, `lan_result`, `lan_interrupt`, and review still work on the individual turn.

Conversations are persisted. List them, read one, or continue it:

```bash
lanbus chat --list
lanbus chat --conversation c123abc --message "继续，总结刚才的结论" --rounds 1
```

MCP: `lan_conversations(conversation_id=...)`. REST: `GET /conversations`, `GET /conversations/<id>`.

### Web dashboard

The hub serves a zero-dependency dashboard at `http://HUB:8788/ui` (public HTML; APIs use the agent token):

- online environments and capabilities
- persisted conversation list and transcript
- start a new 2–4 agent conversation
- resume an existing conversation

The UI uses the same endpoints as MCP/CLI: `GET /api/envs`, `GET /conversations`, `POST /chat`.

### Human confirmation for remote agents

Cross-machine targets require an explicit human approval before a task or
conversation can start. The hub keeps local envs on the fast path and puts
remote-target tasks in `waiting_confirmation`.

```bash
lanbus confirm list
lanbus confirm approve --env macos-a-cli --ttl 1800
lanbus confirm revoke --env macos-a-cli
```

The approval token is separate from the main-agent token:

```text
~/.lanbus/operator-token.txt
```

The Web dashboard / GUI shows remote targets with `hostname`, `agent`, and
`cwd`; use the "确认此 agent" button. The agent token cannot approve targets.
For multiple agents in the same project, give each worker a different env and
name, with the same or different `--cwd`; the human chooses the exact env in
the approval panel.

### Desktop GUI / tray (Windows)

```bash
make go-gui
bin/lanbus-gui.exe --hub http://127.0.0.1:8788
```

Wails/WebView2 window + tray menu. It embeds the same dashboard and proxies
authenticated API calls to the running hub. Closing the window hides it to the
tray; the tray menu reopens it, opens `/ui` in the default browser, or quits.

```bash
lanbus web          # open the dashboard in the default browser
lanbus gui          # launch bin/lanbus-gui.exe
make package        # portable Windows zip under dist/
```

The portable package and optional Inno Setup installer are under `packaging/windows/`.
Signing (`sign.ps1`) and a SHA-256 update helper (`update.ps1`) are included.

### Persistent local workers

```bash
lanbus local init          # write ~/.lanbus/local.json
lanbus local up            # start configured workers if their env is offline
lanbus local status
lanbus local down
```

`local up` skips workers whose env is already online, so it is safe to run from login startup. It supports Go workers, Python workers, and WSL entries (`"wsl": "Ubuntu-24.04"`). See `examples/local/README.md`.

No lanbus/MCP installation is required on the discovered machines. For endpoints that expose an HTTP API you can immediately query them:

```bash
lanbus ask --endpoint 192.168.1.50:11434 --prompt "用一句话介绍自己"
lanbus ask --endpoint 192.168.1.60:1234 --prompt "review this diff" --model qwen2.5
```

Important limit: a **GUI-only** agent that opens no port (a C agent desktop) can only be seen as a process on the machine where it runs. To see it from another machine, run a lanbus worker there or run `lanbus discover` on that host. Remote discovery is inherently limited to what the network exposes.

## Quick start (3 minutes)

### 1. Hub — on the always-on machine (usually Windows)

```bash
pip install .
make go-hub
bin/lanbusd --bind 0.0.0.0 --port 8788 --data-dir ~/.lanbus   --token choose-a-secret --worker-token a-different-worker-secret
```

`lanbusd` is the default hub. The Python hub remains available for browser/legacy
compatibility: `lanbus hub --port 8789 ...` (or `start-hub-python.cmd`).

`--worker-token` is optional. If omitted, workers use the same token as the main agent (simplest single-secret setup). With it, a worker token can only call `/worker/*`, never `/mcp` or `/events`; the agent token can still operate workers. For true per-worker identity, pass `--worker-tokens '{"win-ui":"token1","wsl-core":"token2"}'` (or `--worker-tokens-file`); a token then only works for its registered name.

```powershell
New-NetFirewallRule -DisplayName "lanbus" -Direction Inbound -Protocol TCP `
  -LocalPort 8788 -Action Allow -Profile Private
```

Data (SQLite + artifacts + generated token) goes to `~/.lanbus`. Override with `LANBUS_DATA_DIR` or `--data-dir`. TLS is available with `--tls-cert` / `--tls-key`.

Or with Docker:

```bash
LANBUS_TOKEN=... LANBUS_WORKER_TOKEN=... docker compose up -d
```

### 2. Worker — one per environment

```powershell
# Windows
lanbus worker --name win-ui --env windows --hub http://HUB_LAN_IP:8788 `
  --token a-different-worker-secret --cwd D:\proj\webui --agent ADAPTER
```
```bash
# WSL
lanbus worker --name wsl-core --env wsl --hub http://HUB_LAN_IP:8788 \
  --token a-different-worker-secret --cwd /mnt/d/proj --agent ADAPTER
# macOS
lanbus worker --name mac-ui --env macos --hub http://HUB_LAN_IP:8788 \
  --token a-different-worker-secret --cwd ~/proj --agent ADAPTER
```

`--agent` selects a built-in adapter for a common agent CLI (see the [adapter reference](docs/architecture.md#adapters)); `shell` runs the prompt as a command and `echo` is a connectivity check. `--cmd` accepts any CLI you already have: `{prompt_file}` expands to a temp file holding the prompt, otherwise the prompt goes to stdin. Use `--cmd` for a C agent desktop's bundled CLI, a vendor-specific wrapper, or an agent whose flags differ from the built-in adapters.

Each worker also gets:

- **Streaming logs** — agent stdout/stderr is uploaded line by line; watch with `lan_logs` or `lanbus logs --follow`.
- **Real cancellation** — `lan_cancel` signals the worker, which kills the whole process tree; late submissions are rejected by claim token.
- **Lease + auto-requeue** — crash the worker mid-task and the hub requeues after the lease expires (default 120s, configurable per task).
- **Artifact upload** — screenshots/logs are base64-uploaded to the hub, so separate machines no longer need a shared filesystem.
- **Browser helper** — `pip install playwright && playwright install chromium` (or install Chrome/Edge) lets `lan_shot` click, fill forms and screenshot. `lanbus doctor` reports what is available.
- **Discovery** — `lanbus discover` / `lan_discover` finds AI agents on the LAN without touching them; `lan_ask` can query OpenAI-compatible model endpoints directly.
- **Context handoff** — `context` + local `.lanbus/context.md` + git state are composed into the prompt automatically.

### 3. Main agent — A agent CLI (or any MCP client)

```bash
cd your-project
cp -r /path/to/lanbus/integrations/mcp-client-preset/.mcp.json.example .mcp.json
# set HUB_LAN_IP + token, then point your MCP client at it
cp -r /path/to/lanbus/integrations/mcp-client-preset/agents   .mcp/agents
cp -r /path/to/lanbus/integrations/mcp-client-preset/commands .mcp/commands
```

`integrations/mcp-client-preset` is only a ready-made preset (sub-agent prompts and
slash-command text) that you can adapt to your client. Any MCP client works:
register `http://HUB_LAN_IP:8788/mcp` with the agent token and the same tools
appear. Approve the project MCP server once when your client asks, then:

```
/lan windows change the login button radius from 4px to 12px, run npm run build, screenshot when green
```

It resolves the environment, dispatches, watches logs, reads `verify_out` and `diff`, judges, sends back with feedback if needed, and reports when accepted.

<details>
<summary>Equivalent MCP config (any MCP client)</summary>

```json
{
  "mcpServers": {
    "lanbus": {
      "type": "http",
      "url": "http://HUB_LAN_IP:8788/mcp",
      "headers": { "Authorization": "Bearer choose-a-secret" }
    }
  }
}
```
</details>

---

## MCP tools

| Tool | Purpose |
|---|---|
| `lan_envs` | online workers, caps (`browser`), available `env` values — call before dispatching |
| `lan_dispatch` | dispatch one task; `context` is composed into the prompt; `wait_s>0` blocks until review |
| `lan_dispatch_many` | fan out to several environments in one call |
| `lan_shot` | browser task: URL + click/fill/evaluate actions + screenshot |
| `lan_await` | wait for tasks to reach a reviewable state |
| `lan_result` | full record: output, log tail, `diff`, `changed`, `verify_out`, artifacts; images as MCP image content |
| `lan_logs` | incremental real-time logs (`since` / `wait_s` long-poll) |
| `lan_artifact` | list/read artifacts; images and text returned inline |
| `lan_discover` | LAN AI-agent reconnaissance without preinstall: local processes + ports + HTTP fingerprints; `env` scans from a worker |
| `lan_ask` | send a prompt to a discovered OpenAI-compatible model endpoint and get its reply |
| `lan_review` | the gate: `approve` / `rework` (+feedback) / `fail` |
| `lan_list` | filter by status / env / kind |
| `lan_cancel` | cancel; stops the remote process tree |
| `lan_interrupt` | Ctrl+C-equivalent for a lanbus task: kill the worker process tree, independent of window focus/background state |

Task states:

```
queued ──claim──► claimed ──► running ──submit──► submitted
  ▲                     │                         │
  │                     └─ lease lost ──► queued  ├─ approve ─► approved
  │                        (recoveries+1)         ├─ fail ────► failed
  └──────── rework (feedback, retry+1) ───────────┘
                                                  └─ cancel ──► cancelled
```

`rework` requeues with `retry+1`; past `max_retry` (default 2) the task becomes `failed` instead of looping forever. A lost worker lease requeues with `recoveries+1`; past `max_recoveries` (default 3) it fails. `cancelled` is terminal: the worker is signalled to kill its process tree, and any late `submit` carrying the old claim token is ignored.

---

## HTTP surface (for scripts and dashboards)

All endpoints except `/health` require `Authorization: Bearer <token>`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | status counts, no auth |
| GET | `/events` | SSE stream of task/worker/log events (`Last-Event-ID` supported) |
| GET | `/tasks/<id>/logs?since=N` | incremental execution logs |
| GET | `/discover?cidr=...&ports=...` | zero-preinstall discovery (also `?env=windows`; `/agents` alias) |
| POST | `/ask` | prompt a discovered AI endpoint |
| GET | `/artifacts/<task_id>/<name>` | authenticated artifact download |
| POST | `/mcp` | MCP (stateless streamable HTTP) |
| POST | `/worker/register` | worker registration + capabilities |
| POST | `/worker/claim` | atomic claim; returns `claim_token` |
| POST | `/worker/progress` | lease renewal + cancellation poll |
| POST | `/worker/log` | batched streaming log lines |
| POST | `/worker/submit` | submit result + verify + diff + inline/base64 artifacts |
| POST | `/worker/heartbeat` | liveness + cancellation hints |
| POST | `/worker/recover` | worker restart hands back its lost tasks |

Discovery itself is not a network listener on the scanned hosts: it runs on the hub/worker/CLI side and only probes standard TCP/HTTP surfaces.

---

## Security

- **Two roles**: agent token (`--token`) for MCP/read/artifacts; worker token (`--worker-token`) for `/worker/*` only. If no worker token is set, both roles share one token for backward compatibility. Optional per-worker tokens (`--worker-tokens`) bind a token to one worker name.
- **Worker ownership**: submit/progress/log only work for the task currently assigned to that worker name and its random `claim_token`; a stale worker cannot resurrect a requeued/cancelled task.
- `Host` header allowlist: only `localhost`/loopback and **IP literals**, which blocks DNS-rebinding style attacks (a hostname resolving to your LAN IP).
- Optional TLS: `--tls-cert` + `--tls-key` on the hub; workers use normal verification, `--ca FILE` for a private CA, or `--insecure` only on a trusted LAN.
- Workers are outbound-only — no listening port on Windows/WSL/macOS.
- `lanbus discover` performs an active TCP/HTTP scan. Run it only on networks you are allowed to inspect; it does not exploit services or modify remote state.
- Do **not** publish port 8788 to the internet. For cross-network use, put it behind WireGuard/Tailscale.

---

## Reliability model

- **Execution transport**: prefer worker tasks (`lan_dispatch`) over UI keystroke injection. Worker tasks have a process tree, a claim token, and a real cancellation path; UI automation only observes/notifies a desktop and is not the execution mechanism.
- **Ctrl+C**: `lan_cancel` / `lan_interrupt` sets a cancellation flag; the worker kills the whole process tree (Windows Job Object or POSIX process group). This works while the window is unfocused or the task is in the background.
- **Lease**: claim sets `lease_expires_at`; the worker renews every ~3s via `/worker/progress` while the agent, verify command, or browser runs.
- **Reaper**: hub requeues expired `claimed`/`running` tasks, up to `max_recoveries`; then fails them with evidence.
- **Claim token**: every claim gets a random token; submits from the old execution are rejected after requeue/cancel.
- **Outbox**: if the hub is unreachable when a task finishes, the worker writes the submission under `~/.lanbus/outbox/<name>/` and retries until accepted. If the lease expired meanwhile, the hub still accepts the late result while the task is queued and not yet re-claimed.
- **Logs**: streamed in batches; sequence numbers make retries idempotent. A local copy is kept under `~/.lanbus/logs/`.

---

## Context handoff

A remote agent is a fresh CLI process, so lanbus composes a self-contained prompt:

1. task id / env / cwd / kind / retry / verify / artifact globs;
2. `context` you pass to `lan_dispatch` (background, paths, constraints, acceptance criteria);
3. worker-local `.lanbus/context.md`, `.lanbus/rules.md`, optional `--context-file`, plus git branch/status;
4. your `prompt`;
5. previous `feedback`;
6. non-interactive execution rules.

`lan_result` returns the exact composed prompt, so you can see what the remote agent actually received.

---

## Known limits

1. Conversation context is not magically shared. lanbus injects structured `context`, local `.lanbus/context.md`, and git state, but if a detail only exists in your local chat you must pass it explicitly.
2. Workers use each CLI's non-interactive one-shot mode (`-p` / `exec` / `headless`); anything needing interactive approval fails instead of prompting. Put the required permission mode or pre-authorization in that CLI's own config. Built-in adapters already pass a non-interactive permission mode where the CLI supports one.
3. If WSL uses `/mnt/d/...` and Windows uses `D:\...`, that is one filesystem — no syncing needed, but concurrent edits to the same file will collide. Partition tasks by file.
4. WSL→Windows needs either mirrored networking (`networkingMode=mirrored` in `.wslconfig`, then `wsl --shutdown`) or the host IP, which changes across reboots.

5. Headless browser fallback only supports `goto` + `screenshot`. Click/fill/evaluate requires Playwright (`pip install playwright && playwright install chromium`).
6. Browser tasks require a worker with a `browser` capability; `lan_envs` reports `has_browser` and `caps`.
7. Remote discovery can only see hosts that answer on scanned TCP ports or expose an HTTP(S) API. GUI-only agents are visible only in local process scans; run a lanbus worker or `lanbus discover` on that host for full coverage. Port 443 is fingerprinted over HTTPS; custom HTTPS ports are not probed by default.
8. Discovery is heuristic: a port can be open for unrelated software, and an OpenAI-compatible endpoint may be an inference server rather than a full tool-using agent. Verify before sending sensitive prompts.
9. An agent CLI on macOS needs a worker running on the Mac (`--agent ADAPTER` or `--cmd`) or a local `lanbus discover`; a Windows scanner cannot see a GUI/CLI process on the Mac without one of those. macOS Local Network permission must allow Terminal/the worker.
10. UI-only desktop windows have no stable programmable input/interrupt API. lanbus uses CLI/headless/worker for task execution; GUI automation is best-effort notification only.

### macOS worker

If an agent CLI is already installed on macOS but is not reachable from Windows, it has no worker. Run this on the Mac:

```bash
export PATH="$HOME/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
lanbus doctor
lanbus worker --name mac-a-cli --env macos --hub http://HUB_LAN_IP:8788 --token WORKER_TOKEN --cwd /Users/me/proj --agent ADAPTER
```

Or use the native Go worker without Python:
`bin/lanbus-worker-darwin-arm64` (Apple Silicon) or `bin/lanbus-worker-darwin-amd64`, with the launchd template
`examples/macos/com.lanbus.worker-go.plist` (native Go worker) or
`examples/macos/com.lanbus.worker.plist` (Python worker); set `ADAPTER` and paths.

Or run `examples/macos/start-worker.sh` in the foreground for a quick self-check.
After it connects, `lan_envs` shows `mac-a-cli` with the adapter capability, and `lan_dispatch(env="macos", ...)` runs in the current project. `lan_discover` from Windows can only see the Mac's open ports; process-level agent detection happens on the Mac via its worker or local `lanbus discover`.

---

## Development

```bash
pip install -e ".[dev]"
pytest -v                     # 39 tests: protocol, dispatch/rework, logs, cancel/interrupt, lease recovery, artifacts, roles, discovery, chat, web UI, local supervisor, Go hub tools/dual-run, remote confirmation
make go-test                  # Go scanner + worker tests
make go-scan                  # native bin/lanbus-scan; Python auto-detects it
make go-worker                # native bin/lanbus-worker
python -m pip wheel . --no-deps --no-build-isolation -w dist
```

Tests spin up a real hub and real workers on free ports and drive actual MCP JSON-RPC — no mocks.

## Docs

- [Architecture](docs/architecture.md) — components, why workers pull, state machine, evidence model
- [Protocol](docs/protocol.md) — every endpoint, tool schema, error codes, SSE events
- [Troubleshooting](docs/troubleshooting.md) — symptom → cause → fix
- [Go migration assessment](docs/go-migration.md) — why/if to move the core to Go, magpie patterns
- [Go scanner and worker](go/README.md) — Phase 0/1 native binaries
- [中文说明](README.zh-CN.md) · [发布指南](PUBLISHING.md) · [变更记录](CHANGELOG.md) · [贡献指南](CONTRIBUTING.md)

## License

MIT
