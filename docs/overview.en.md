# lanbus — overview

[中文总览](overview.zh-CN.md) · [Back to README](../README.md) · [Architecture](architecture.md) · [Protocol](protocol.md)

lanbus is a LAN-wide orchestration bus for AI agents. A main agent (an A agent CLI, a B agent CLI, a C agent desktop,
or any MCP client) dispatches work to agents running on other
machines, then reviews machine-checkable evidence before accepting it.

## 1. Principle

### One inbound surface, many outbound workers

The **hub** is the only listening service. Every **worker** opens outbound
connections to the hub and pulls tasks. That removes per-machine firewall rules
and makes WSL/NAT, sleep, DHCP, and changing IPs manageable.

```text
main agent ──MCP──► hub :8788 ◄──long-poll── worker (Windows)
                             ◄──long-poll── worker (WSL)
                             ◄──long-poll── worker (macOS)
```

### Tasks are evidence, not prose

A remote agent claiming "done" is not evidence. A task completes with:

- `exit_code` and streamed logs from the worker;
- `verify_ok` from a command **the caller chose**, executed on the worker;
- `diff` and `changed` collected from the real working tree;
- uploaded artifacts with SHA-256 and MIME;
- an explicit `lan_review(approve | rework)` decision.

The agent that executes a task cannot approve it. Rework feedback is appended to
the next prompt.

### Safe remote dialogue

A task aimed at a remote env is created in `waiting_confirmation` and cannot be
claimed. A human approves the agent target with a separate **operator token**
(`~/.lanbus/operator-token.txt`); the main agent's token cannot approve. The
dashboard shows each target's hostname, agent, and `cwd`, so multiple agents in
the same project — or agents in other projects — are distinguishable.

Local envs (same host as the hub, including typical WSL workers) stay on the fast
path. Remote approval has a TTL and can be revoked.

### Discovery without installation

The built-in scanner combines:

1. local processes (agent CLI and desktop executables, local model runtimes,
   image/UI tools; Windows also reads running WSL processes);
2. local PID-to-port mappings;
3. LAN TCP probes plus HTTP fingerprints (local model runtimes,
   OpenAI-compatible servers, chat/web UIs, inference servers, notebook servers,
   browser DevTools, MCP `serverInfo`, lanbus `/health`, and FastAPI/OpenAPI AI APIs).

A GUI-only agent that opens no port can only be seen by a process scan on its
own machine. Running a worker or `lanbus discover` there closes that gap.

## 2. Implementation

### Components

| Component | Language | Role |
|---|---|---|
| `lanbusd` | Go | Default hub: SQLite task store, worker API, MCP, SSE, artifacts, operator approvals, Web UI |
| `lanbus-worker` | Go | Pull worker: process supervisor, Job Objects/PTY, agent-CLI adapters (or `--cmd`), discover/ask, artifacts |
| `lanbus-scan` | Go | Zero-install LAN scanner used by discovery |
| `lanbus-gui` | Go + Wails/WebView2 | Windows desktop window + tray, embedded dashboard |
| `lanbus` | Python | CLI, discovery fallback, browser helper, Python worker, Python hub fallback |
| `lanbus-hub` | Python | Reference hub and fallback for browser/legacy tasks |

### Data and state

- SQLite under `--data-dir` (default `~/.lanbus`): `tasks`, `workers`,
  `task_logs`, `artifacts`, `events`, `approvals`, `conversations`.
- States: `queued`, `claimed`, `running`, `waiting_confirmation`, `submitted`,
  `approved`, `failed`, `cancelled`, `rework`.
- Atomic claim with a random `claim_token`; heartbeats update a lease; a reaper
  requeues expired leases and caps recoveries.
- Late submits are rejected after cancel/rework; an idempotent submit is
  accepted if it still matches the last claim token after a recovery.
- Artifacts are stored as `<data-dir>/artifacts/<task>/<name>` and served via
  authenticated `/artifacts/...`.

### Protocol surface

- Worker REST: `/worker/register`, `/worker/heartbeat`, `/worker/claim`,
  `/worker/progress`, `/worker/log`, `/worker/submit`, `/worker/recover`,
  `/worker/leave`.
- MCP: `initialize`, `ping`, `tools/list`, `tools/call`.
- Tools: `lan_dispatch`, `lan_dispatch_many`, `lan_envs`, `lan_list`,
  `lan_result`, `lan_await`, `lan_logs`, `lan_review`, `lan_cancel`,
  `lan_interrupt`, `lan_artifact`, `lan_chat`, `lan_conversations`,
  `lan_discover`, `lan_ask`, `lan_shot`.
- SSE: `GET /events`.
- UI: `GET /ui`; aliases `/api/envs`, `/api/tasks`, `/conversations`,
  `POST /chat`.
- Operator: `GET /api/confirmations`, `POST /confirm/approve`,
  `POST /confirm/revoke`.

### Process control

- Windows: Job Objects with `KILL_ON_JOB_CLOSE`; `TerminateJobObject`.
- POSIX: process groups with SIGTERM, then SIGKILL.
- `lan_interrupt` kills the whole tree, not just the parent process.
- `--pty` uses ConPTY on Windows and Unix PTY elsewhere for TUI agents.

## 3. Scope

**Designed for**

- one trust domain / private LAN, typically one /24;
- one main agent plus one or more CLI/headless agents;
- Windows, WSL/Linux, and macOS workers;
- tasks that can be verified by exit code, diff, logs, or artifacts;
- local multi-agent dialogue and human-approved remote dialogue.

**Not designed for**

- exposing the hub directly to the public internet;
- controlling GUI-only applications without a worker on that host;
- bypassing an agent's own permissions or terms;
- acting as a general remote-shell service outside your own machines;
- replacing code review or security review of agent output.

**Operational notes**

- The hub needs one inbound TCP port (default `8788`).
- macOS 14+ needs Local Network permission for Terminal/the worker.
- Keep `operator-token.txt` out of source control and off shared disks.
- Worker tokens grant task execution on that worker; treat them as credentials.
- Browser tasks need a Python worker with Chrome/Edge or Playwright.

## 4. Usage

### Install and start

```bash
pip install .
make go-hub go-worker go-gui
bin/lanbusd --bind 0.0.0.0 --port 8788 --data-dir ~/.lanbus \
  --token AGENT_TOKEN --worker-token WORKER_TOKEN
```

The hub prints a data directory; the first start generates
`~/.lanbus/operator-token.txt`.

### Start workers

```bash
bin/lanbus-worker --name win-a-cli --env windows \
  --hub http://HUB:8788 --token WORKER_TOKEN --cwd D:/proj --agent ADAPTER
```

For local supervisor (Go/Python/WSL):

```bash
lanbus local init
lanbus local up
lanbus local status
```

### Main-agent tools

```text
lan_envs
lan_dispatch(env, prompt, context, cwd, verify, wait_s)
lan_logs(task_id, since)
lan_result(task_id)
lan_review(task_id, approve|rework, feedback)
lan_chat(envs, message, rounds)
```

Remote targets return `waiting_confirmation` or `human_confirmation_required`
until a human approves:

```bash
lanbus confirm list
lanbus confirm approve --env macos-a-cli --ttl 1800
```

### Web / desktop

```bash
lanbus web                 # open dashboard
lanbus gui                 # Wails window + tray (Windows)
```

### Build, test, package

```bash
make test                  # Python tests
make go-test               # Go tests
python -m pytest -q        # full suite (Go hub binary required)
python scripts/package_windows.py
```

See the [cross-machine case study](case-studies/cross-machine-test.md) for a real
Windows ↔ macOS run, including the approval gate and remote process kill.
