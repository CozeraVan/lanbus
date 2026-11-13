# Architecture

lanbus has two modes that share the same binaries and adapters:

1. **Direct company network** — symmetric peer nodes, no hub. Each node listens on its own port (default `8790`), discovers peers, and exchanges encrypted chat/project requests. Company membership removes pairwise setup; a human approves each join once, on any existing member.
2. **Hub orchestration** — one hub, N pull workers, evidence-checked task lifecycle. Everything below the "Direct company network" section describes this mode unless stated otherwise.

## Direct company network

```
node A (mac-agent) ◄──encrypted peer link──► node B (a-agent)
        ▲                                        ▲
        └──────────────► node C ◄────────────────┘
              joins via A or B; one human approval
```

- **Symmetric roles**: every node can discover, ask, receive messages, and delegate. `lanbus agent` is the entry point; `lanbus worker --direct` remains compatible. There is no host/worker hierarchy and no central token server.
- **Identity and transport**: Ed25519 node identities, X25519 key agreement, AES-GCM encrypted payloads. Company credentials are delivered through the encrypted join channel. Private files use `0700` directories, `0600` files, and atomic writes on POSIX.
- **Membership**: `lanbus company create` starts a company; `lanbus company join --to <member>` applies through any existing member. The request appears only on that member's machine, where a person approves once (`lanbus allow REQUEST_ID --pair`, or the local approval page / desktop notification). Approval, denial, and revocation accept only loopback requests on the target.
- **Request policies**: `company` (default) lets members communicate freely while non-members still need human pairing; `each` requires a human "Approve once" for every chat/task, showing source and target instance; `pair` is the legacy persistent pairing with optional session scope (30 idle minutes or target restart expiry).
- **Local MCP entry**: `lanbus-mcp` runs on the node's machine against `LANBUS_PEER_DIR` and exposes `lanbus_agent_discover` / `lanbus_agent_send` / `lanbus_agent_delegate` / `lanbus_agent_result` / `lanbus_company_list` / `lanbus_company_join`. The company secret never goes into MCP configuration.
- **Instance disambiguation**: display names and instance labels distinguish multiple agents/windows on one machine; ambiguity errors list host, agent, instance, cwd, and endpoint instead of silently picking the first candidate.
- **Trust boundary**: all members share the company secret. Compromise of one member may compromise the whole company; leaving does not revoke grants already stored by other nodes. Project tasks are confined to approved working directories, but the agent CLI is not a full OS sandbox.

Python (`src/lanbus/peer.py`, `company.py`) and Go (`go/internal/peer`) implement the same protocol; cross-language membership and chat are covered by `cross-version/company_test.py`.

## Hub-mode components

| Component | Process | Responsibility |
|---|---|---|
| Hub | one, on an always-on machine | task table, state machine, lease reaper, auth, log/artifact storage, SSE |
| Worker | one per environment (N) | pull a task, run the local agent / verify / browser / discovery / ask, stream logs, collect diff and artifacts, submit; owns a real process tree and cancellation |
| Main agent | your CLI/desktop app | decompose, pass context, dispatch, watch logs, judge, rework |
| Subagent shell | inside the main agent | only calls MCP tools and returns evidence — never edits files |

## Why workers pull instead of being called

Push (hub → worker HTTP) requires every sub-environment to listen and accept inbound connections: a port per machine, a firewall rule per machine, and a reachability problem every time an IP changes (WSL NAT, macOS sleep, DHCP).

Pull inverts it: each worker opens outbound connections to one address. The hub is the only listening surface. If a worker dies, its lease expires and the hub requeues the task for another worker of that env — no retry storm, no lost task.

Cost: latency of up to one poll interval (~2 s). Acceptable for agent tasks that take tens of seconds to minutes.

## State machine

```
                    ┌──────────────── rework (retry+1, feedback appended)
                    ▼
queued ──claim──► claimed ──► running ──submit──► submitted ──approve──► approved
   ▲                 │          │                  │
   │                 │          │                  ├──fail──────────────► failed
   │                 └──────────┴─ lease lost ─────┘
   │                    recoveries+1 (requeue)              cancel ──► cancelled
   └──────────────────────────────────────────────────────────────────────
```

Invariants:

1. `claim` is atomic: `UPDATE ... WHERE id=? AND status='queued'`, so two workers racing cannot both get one task. Every claim gets a random `claim_token`.
2. Only the caller (main agent) may invoke `lan_review`. Workers have no route to it.
3. `submitted` is **not** success. Success is `approved`.
4. A submission must match both `worker` and `claim_token`, and the task must still be `claimed`/`running`. After a lease recovery it may also arrive while the task is `queued`, if its token matches `last_claim_token` and no new worker has claimed it yet; this preserves completed work across a hub outage. After cancel or rework, old submissions are ignored.
5. `submitted` is idempotent: a retried submit is ignored, never downgraded to `failed`.
6. A `rework` never loses the reviewer's history: `review_note` accumulates feedback; the next round overwrites evidence fields.
7. `cancelled` is terminal. The worker is asked to kill its process tree; any late submit is rejected.

## Lease and crash recovery

`claim` writes `lease_expires_at = now + task.lease_s` (default 120 s). While an agent, verify command, or browser helper runs, the worker renews the lease every ~3 s through `POST /worker/progress`; the same endpoint is the cancellation poll.

A hub reaper thread scans `claimed`/`running` tasks every `--reap-s` (default 10 s). When a lease expires it:

- increments `recoveries`;
- clears `worker` / `claim_token` / `composed_prompt`;
- puts the task back into `queued` (status `task.recovered`), preserving `retry` and `feedback`;
- after `max_recoveries` (default 3, per task), marks it `failed` with a review note.

When a worker restarts it immediately calls `POST /worker/recover` so its own orphaned tasks are requeued without waiting for the full lease.

The old worker may still be alive (partitioned network, zombie agent). Its `progress`/`log`/`submit` calls fail the claim-token check and are ignored.

## Live logs

The worker merges stdout and stderr into a line reader and sends batches to `/worker/log` every ~0.7 s or 40 lines. Each line carries a per-task monotonically increasing `seq`; the hub uses `INSERT OR IGNORE` so retries are idempotent.

- `lan_logs(task_id, since, wait_s)` polls increments and can long-poll while the task is active.
- `GET /tasks/<id>/logs?since=N` exposes the same data to scripts.
- `lan_result` includes a 60-line tail.
- SSE emits `task.log` events for dashboards.
- A local copy is written under `~/.lanbus/logs/` on the worker for post-mortems.

## Evidence model

Every submission carries independent signals:

| Field | Source | Why it is trustworthy |
|---|---|---|
| `result` | agent stdout/stderr | raw, truncated only at 20 kB |
| `verify_ok` + `verify_out` | the acceptance command the **caller** specified, executed on the worker | the worker cannot choose it; the exit code is not self-reported prose |
| `diff` | `git diff HEAD` inside `cwd` | measured from the real tree, not described |
| `changed` | `git status --porcelain` | catches untracked files and deletions |
| artifacts | uploaded bytes + SHA-256 | reviewer can open exactly what the worker produced, across machines |
| logs | streamed lines with seq | shows what happened, not only the final summary |

## Artifacts

The worker collects files matching:

1. task `artifact_globs`;
2. worker `--artifact-glob` / built-in defaults (`.lanbus-shot.*`, `screenshot*.png`, `build.log`, `artifacts/**/*.png`, `playwright-report/**/*.png`);
3. the browser task's `out` / `pdf` paths.

Files modified since the task started are read, size-capped, and sent inline as base64 in `/worker/submit`. The hub decodes, verifies SHA-256, stores them under `artifacts/<task_id>/<relative-name>`, and records MIME + size. This works when the hub and worker do **not** share a filesystem (macOS, separate Windows boxes).

`lan_result` returns artifacts + authenticated download URLs; images also become MCP `image` content blocks. `lan_artifact` can list or read one artifact, returning text or an image block.

## Context handoff

A remote worker is a fresh CLI process and cannot see the main agent's conversation. Before launching the agent, the worker composes:

1. task metadata (id, env, cwd, kind, retry, verify, artifact globs);
2. the dispatcher-provided `context`;
3. worker-local `.lanbus/context.md`, `.lanbus/rules.md`, optional `--context-file`, plus `git rev-parse` and `git status --porcelain`;
4. the task `prompt`;
5. accumulated `feedback`;
6. non-interactive constraints.

The exact text is returned in `lan_result.composed_prompt` and streamed to the log as `context`.

## Local multi-agent chat

`lan_chat` is a hub-side relay, not a new worker type. It takes 2–4 envs and 1–6 rounds; for each turn it creates a normal `agent` task, waits for the submission, reads `result`, and appends the cleaned reply to the transcript passed to the next env. This keeps every turn inspectable with `lan_result`/`lan_logs` and interruptible with `lan_interrupt`.

The prompt tells the participant to answer as a dialogue turn and not to edit files. CLI boilerplate (Claude warning line, Kimi version/session trailer) and DeepSeek reasoning prefixes are removed for the transcript only; the raw task result remains stored.

Each conversation is persisted in the `conversations` table (`turns` as JSON). Reusing `conversation_id` appends a new round while keeping the prior transcript; `lan_conversations` and `GET /conversations/<id>` read it back.

## Discovery without preinstall

`discovery.py` is a separate, best-effort reconnaissance layer:

| Evidence | Local | Remote |
|---|---|---|
| process list | full: Claude/Codex/Cursor/Kimi/Ollama/... + executable paths; Windows also reads running WSL processes | not visible (use a worker on the Mac) |
| listening ports + PID | full | only open ports |
| HTTP fingerprints | full | Ollama, OpenAI-compatible, Open WebUI, ComfyUI, llama.cpp, vLLM, Gradio, Jupyter, Chrome DevTools, MCP, lanbus, FastAPI/OpenAPI AI APIs |
| prompt / feedback | spawn CLI or local API | only APIs the service already exposes (`lan_ask`) |

It scans the local `/24` by default (or `--cidr`), with 35 AI/dev ports and short TCP timeouts; ARP entries are merged when no CIDR is given. HTTP probes are limited to a handful of paths and only after a TCP port answers. No exploit, no remote write.

The hub tool `lan_discover` runs locally on the hub by default, or dispatches a `kind=discover` task to a worker (workers report `discover` in `caps`). The worker uploads the full report as `discovery.json`; `lan_ask` can then send a prompt to a discovered OpenAI-compatible/Ollama endpoint without installing anything on that host.

Limits are explicit in the report: a GUI-only agent with no listening port is only visible to the machine it runs on. Remote visibility requires an exposed API, a lanbus worker, or OS-level access (SSH/WMI) that lanbus does not assume.

## Execution transport and interruption

Task execution goes through the worker process tree, not through synthetic UI keystrokes:

- worker starts the CLI/headless agent in a new process group;
- `/worker/progress` is polled during execution; `lan_cancel` / `lan_interrupt` sets cancellation;
- the worker kills the whole tree (Windows Job Object `KILL_ON_JOB_CLOSE`/`TerminateJobObject`, `killpg` on POSIX), so minimizing, losing focus, or backgrounding the window does not affect interruption;
- a claim token prevents the interrupted execution from submitting after a requeue.

GUI automation is intentionally not the execution path. It can make a message visible in an existing desktop window, but it has no reliable interrupt semantics and is tied to window focus. `lan_interrupt` is only meaningful for tasks tracked by the hub/worker.

## Adapters

A worker adapter is just "how to start one agent CLI non-interactively". Built-in
`--agent` values:

| value | how the prompt is delivered |
|---|---|
| `claude` | one-shot print mode with an auto-accept edit permission mode; prompt on stdin |
| `codex` | non-interactive `exec` mode, skip repo check; prompt on stdin |
| `kimi` | single-argv prompt via the `lanbus.prompt_cli` helper (avoids shell quoting / line-length limits) |
| `deepseek` | headless mode with stdin |
| `shell` | run the prompt itself as a shell command |
| `echo` | connectivity check |

Anything else goes through `--cmd`: `{prompt_file}` is replaced by a temp file
holding the composed prompt, otherwise the prompt is written to stdin. Use
`--cmd` for a desktop app's bundled CLI, a vendor wrapper, or an agent whose flags
differ from the built-ins.

## PTY mode

`lanbus-worker --pty` attaches the agent command to a real pseudo-terminal via `github.com/aymanbagabas/go-pty`: ConPTY on Windows, Unix PTY elsewhere. PTY output is streamed into the same per-task log, and cancellation still closes the Job Object / process group. The normal pipe mode remains the default because `-p`/stdin agents are more deterministic there.

## Web dashboard

`GET /ui` returns a self-contained HTML dashboard (no CDN, no external assets). It asks for the agent token in the browser, then uses the same authenticated APIs as CLI/MCP: `/api/envs`, `/conversations`, `POST /chat`. The token is kept in `localStorage`; the hub never embeds it in HTML.

## Native Go hub (Phase 2a)

`go/cmd/lanbusd` is a native hub subset sharing the same worker REST contract and
SQLite shape. It supports register/claim/progress/log/submit/recover, MCP
dispatch/envs/list/result/await/logs/review/cancel/interrupt/artifact, SSE, and
artifact files. `tests/test_go_hub.py` runs the same fake worker against the Python
hub and `lanbusd` and compares status/exit/verify/result. The Go hub is the
default on port 8788; the Python hub remains available as a fallback for
browser tasks and legacy compatibility.

## Local worker supervisor

`lanbus local` reads `~/.lanbus/local.json`, skips envs already online, and starts the configured Go/Python/WSL workers detached. PID files live under `~/.lanbus/local/`. On Windows the login startup script calls `lanbus local up`; WSL entries are launched through `wsl.exe -d <distro> -e bash -lc ...`.

## Human approval for remote agents

A task targeting a remote worker is inserted as `waiting_confirmation`, not
`queued`. The hub exposes `GET /api/confirmations` and `POST /confirm/approve`
only to the operator token (`~/.lanbus/operator-token.txt`); the main-agent token
cannot approve. Approval is scoped to an env for a TTL and queues that env's
waiting tasks. `lan_chat` refuses to start until every remote participant has an
active approval.

Workers report `cwd` so the operator can distinguish multiple agents in the same
project or agents in unrelated projects. Local envs (same hostname as the hub)
stay on the fast path; WSL and Windows workers are generally local by hostname.

## Wails desktop shell

`go/cmd/lanbus-gui` (Windows) embeds the same dashboard HTML in a Wails/WebView2
window and proxies authenticated API requests to the running hub. A systray icon
provides open-panel, open-in-browser, and quit actions; closing the window hides
it to the tray. The production build uses `-tags production -H=windowsgui`.

## Browser capability

`browser.py` runs inside the worker:

- **Playwright** (preferred): `goto`, `click`, `fill`, `press`, `select`, `check`, `wait`, `wait_for`, `wait_for_load`, `evaluate`, `reload`, screenshot, PDF.
- **Headless Chrome/Edge fallback**: `goto` + screenshot only.

`lan_shot` creates a `kind=browser` task with `required_caps=["browser"]`, so a worker without browser capability cannot claim it. `lan_envs` exposes `has_browser` and exact `caps`.

## Shared filesystem shortcut

When WSL mounts the Windows drive (`/mnt/d/proj` ↔ `D:\proj`), both workers operate on **one** tree. No sync, no copy, no artifact transfer is needed; the main agent reads the same files it already has. Only genuinely separate machines need artifact upload — and that is now built in.

## Concurrency

- Hub: `ThreadingHTTPServer`, one thread per request; SQLite in WAL mode with a short critical section around `claim`, log append, and submission.
- Tasks are ordered by `priority DESC, retry ASC, created_at ASC`; fan-out is explicit (`lan_dispatch_many`), not inferred.
- Practical ceiling on a LAN: dozens of workers. The bottleneck is the agent CLI each worker spawns, not the hub.

## Security positions

- **Agent role** token: MCP, SSE, task logs, artifact download.
- **Worker role** token: `/worker/*` only (register/claim/progress/log/submit/heartbeat/recover/leave). When no separate worker token is configured, both roles use the agent token for compatibility.
- **Per-worker identity**: optional `--worker-tokens` / `--worker-tokens-file` maps a token to one worker name; the worker name is read before auth, so a token for `win-ui` cannot heartbeat/claim/submit as `wsl-core`.
- **Ownership**: a worker can only submit/progress/log a task whose `worker` and `claim_token` match.
- `Host` must be loopback or an IP literal. A hostname is rejected, which neutralises DNS-rebinding attempts.
- TLS is optional (`--tls-cert`, `--tls-key`); without it, assume a trusted LAN or use WireGuard/Tailscale.
- Artifacts are written under the data directory only, with `..` removed and a realpath containment check; downloads require the agent token.
- Browser actions are powerful. Only dispatch `lan_shot` to workers you trust; URLs and JS execute on the worker host.
