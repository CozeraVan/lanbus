# Protocol

Two surfaces on one port: **MCP** for agents (`POST /mcp`), **REST** for workers (`POST /worker/*`), plus read-only task logs/artifacts over GET.

Base URL: `http://HUB_LAN_IP:8788` (or `https://` with `--tls-cert/--tls-key`). Every endpoint except `/health` requires:

```
Authorization: Bearer <token>
```

Two token roles:

| Token | Set by | May call |
|---|---|---|
| agent | `--token` / `LANBUS_TOKEN` / `token.txt` | `/mcp`, `/events`, task logs, artifact download, and `/worker/*` as operator |
| worker | `--worker-token` / `LANBUS_WORKER_TOKEN` / `worker-token.txt` | `/worker/*` only (cannot call MCP/read) |
| per-worker | `--worker-tokens` JSON or `--worker-tokens-file` | `/worker/*` only for the matching `name` in the body |

If no worker token is configured, the worker uses the agent token. Example per-worker file:

```json
{"win-ui": "token-for-win", "wsl-core": "token-for-wsl"}
```

The body `name` is read before worker auth; a token bound to `win-ui` is rejected for `wsl-core`. Unmapped names fall back to the shared worker token. `Host` must be `localhost`, a loopback address, or an IP literal — hostnames get `403`.

---

## MCP (`POST /mcp`)

Stateless streamable HTTP. No session id is issued and none is required, so multiple clients can share one endpoint. Notifications get `202` with an empty body; requests get `200` with a single JSON-RPC response.

Supported methods: `initialize`, `ping`, `tools/list`, `tools/call`. Unknown methods return `-32601`.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize",
 "params":{"protocolVersion":"2025-06-18","capabilities":{}}}
```

Tool results are a list of content blocks: one `text` block containing JSON, plus `image` blocks for image artifacts where applicable.

### Tool schemas

| Tool | Required | Optional |
|---|---|---|
| `lan_dispatch` | `prompt` | `env`, `title`, `context`, `cwd`, `verify`, `max_retry`=2, `timeout_s`=900, `lease_s`=120, `max_recoveries`=3, `kind`, `browser`, `artifact_globs`, `required_caps`, `priority`, `parent_id`, `wait_s` |
| `lan_dispatch_many` | `tasks[]` | `wait_s` |
| `lan_chat` | `message` (`envs[]` for a new chat) | `envs[]`, `rounds`=2, `conversation_id`, `context`, `title`, `timeout_s`=240, `total_timeout_s`=540 |
| `lan_conversations` | — | `conversation_id`, `limit`=20 |

`lan_dispatch` to a remote target returns `status=waiting_confirmation` and a
`target` object until an operator approves the env. `lan_chat` similarly returns
`human_confirmation_required` instead of starting a conversation. Operator
endpoints (`/api/confirmations`, `/confirm/approve`, `/confirm/revoke`) require
the separate operator token.
| `lan_shot` | `url` | `env`, `cwd`, `out`=`.lanbus-shot.png`, `full_page`, `viewport`, `actions`, `wait_ms`, `timeout_ms`, `context`, `wait_s` |
| `lan_await` | `task_ids[]` | `wait_s`=60 |
| `lan_result` | `task_id` | `include_images`=true |
| `lan_logs` | `task_id` | `since`, `tail`=200, `limit`, `wait_s` |
| `lan_artifact` | `task_id` | `name` |
| `lan_discover` | — | `env`, `cidr`, `ports`, `deep`, `timeout`, `http_timeout`, `max_hosts`, `include_local`, `include_processes`, `include_wsl`, `scanner_bin`, `wait_s` |
| `lan_ask` | `prompt` | `host`, `port`, `endpoint`, `model`, `service`, `api_key`, `tls`, `env`, `timeout_s`, `max_tokens`, `wait_s` |
| `lan_review` | `task_id`, `verdict` (`approve`\|`rework`\|`fail`) | `feedback` |
| `lan_list` | — | `status`, `env`, `kind`, `limit`=30 |
| `lan_envs` | — | — |
| `lan_cancel` | `task_id` | `reason` |
| `lan_interrupt` | `task_id` | `reason` |

Domain errors (unknown id, bad verdict, task in the wrong state) come back as JSON `{"error": ...}` inside a normal text result. Only **protocol** faults (unknown tool, missing required argument) produce `-32602`.

### Dispatch

```json
{"name":"lan_dispatch","arguments":{
  "env":"windows","title":"改按钮圆角",
  "prompt":"把 app.txt 的 radius 改成 12px",
  "context":"项目 Web UI 在 D:\\proj\\webui；只改 app.txt；验收命令必须退出 0。",
  "cwd":"D:\\proj\\webui",
  "verify":"npm run build",
  "artifact_globs":["dist/**/*.png","build.log"],
  "max_retry":2,"lease_s":120,"wait_s":120}}
```

Returns `{"task_id":"...","done":[...],"pending":[...],"results":[{slim task}]}`.
`wait_s=0` returns immediately with `status: queued`.

`context` is stored separately and composed into the remote prompt by the worker (together with `.lanbus/context.md`, `.lanbus/rules.md`, and git state). `kind` is `agent` (default), `shell` (prompt is a shell command), `browser` (the worker's built-in browser helper), `discover` (the worker runs the LAN AI-agent scanner and uploads `discovery.json`), or `ask` (the worker calls `lan_ask` against an endpoint reachable only from its network).

`status: submitted` means **waiting for your judgement**, not success.

### Result

```json
{"name":"lan_result","arguments":{"task_id":"a1b2c3d4e5f6"}}
```

Returns the full record:

- `status`, `retry`, `recoveries`, `cancel_requested`, `worker`
- `result` (agent output, ≤20 kB), `log_tail` (last 60 lines), `log_count`
- `diff` (≤200 kB), `changed`, `verify_ok`, `verify_out`
- `context`, `composed_prompt` (exact text sent to the agent)
- `artifacts[]` with `name`, `mime`, `size`, `sha256`, and authenticated `url`
- `prompt`, `feedback`, `review_note`

Image artifacts are also returned as MCP `image` content blocks (max 4, ≤8 MB each). Set `include_images:false` to skip them.

### Logs

```json
{"name":"lan_logs","arguments":{"task_id":"a1b2c3d4e5f6","since":0,"tail":200}}
```

Returns `{status, logs:[{seq,stream,text,created_at}], next_since, log_count, running}`.
Pass `next_since` as `since` for incremental reads. With `wait_s>0` while the task is active, the call long-polls until new lines arrive or the timeout expires.

Streams: `stdout`, `stderr` (merged into stdout on the worker), `system`, `context`, `verify`, `browser`, `hub`.

### Artifact

```json
{"name":"lan_artifact","arguments":{"task_id":"a1b2c3d4e5f6","name":"shot.png"}}
```

Without `name`, returns the artifact list. With `name`, images become `image` content; text/JSON/XML returns a `text` field; other binary types return metadata plus `artifact.url`.

Download:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  http://HUB:8788/artifacts/a1b2c3d4e5f6/shot.png -o shot.png
```

### Shot

```json
{"name":"lan_shot","arguments":{
  "env":"windows","url":"http://localhost:3000/login",
  "out":".lanbus-shot.png","full_page":true,
  "actions":[
    {"fill":["#email","a@b.c"]},
    {"click":"button[type=submit]"},
    {"wait":1000}
  ]}}
```

Creates a `kind=browser` task with `required_caps=["browser"]`; only a worker that reported browser capability can claim it. The screenshot is uploaded automatically and returned as an image block by `lan_result`.

### Discover

```json
{"name":"lan_discover","arguments":{
  "cidr":"192.168.1.0/24",
  "ports":"11434,1234,8000-8100",
  "include_processes":true,
  "wait_s":0}}
```

Without `env`, the scan runs on the hub host and returns the report directly. With `env`, it dispatches a `kind=discover` task to that worker and returns:

```json
{"task_id":"...","env":"windows","status":"submitted",
 "summary":{"hosts_in_scope":254,"hosts_alive":8,"hosts_with_agents":2},
 "report":{"interfaces":[],"local_agents":[],"hosts":[],"agents":[]}}
```

The full report is also stored as `artifacts/<task_id>/discovery.json`. Report fields: `interfaces`, `local_agents`, `local_services`, `hosts`, `agents`, `summary`, `notes`.

`POST /chat` accepts the same arguments as `lan_chat` for non-MCP clients. `GET /conversations` lists persisted conversations; `GET /conversations/<id>` returns turns and transcript.

Fingerprints include Ollama, OpenAI-compatible (`/v1/models`), LM Studio, Open WebUI, ComfyUI, llama.cpp, vLLM, Gradio, Jupyter, Chrome DevTools, MCP `serverInfo`, lanbus `/health`, FastAPI/OpenAPI AI APIs (`/openapi.json`), and auth-required HTTP. The scan does not exploit services or modify remote state. A GUI-only agent with no listening port is visible only to the host it runs on.

### Ask

```json
{"name":"lan_ask","arguments":{
  "endpoint":"192.168.1.50:11434",
  "prompt":"用一句话介绍自己",
  "model":"qwen2.5"}}
```

Tries OpenAI `/v1/chat/completions`, then Ollama `/api/chat` / `/api/generate`. Returns `{ok, endpoint, api, model, reply}`. The discovered host does **not** need lanbus or MCP installed. `service:"ollama"` forces the Ollama path; `api_key` adds a Bearer header; `endpoint:"https://..."` or `tls:true` uses HTTPS with certificate verification disabled (LAN self-signed).

Add `env:"windows"` to run the request from that worker (useful when only that machine can reach the endpoint). The worker returns `ask.json` and the hub merges its fields (`ok`, `reply`, ...) into the tool result.

### Review and cancel

```json
{"name":"lan_review","arguments":{
  "task_id":"a1b2c3d4e5f6","verdict":"rework",
  "feedback":"build 没过：app.txt 里还是 4px，改成 12px 再交"}}
```

- `approve` → `approved` (terminal; only from `submitted`)
- `rework` → `queued`, `retry+1`, feedback appended to the next prompt; if `retry >= max_retry` → `failed`
- `fail` → `failed` (terminal); if currently running, the worker is notified to kill the process
- `lan_cancel` / `lan_interrupt` → `cancelled` (terminal); the worker kills its process tree; late submits are ignored. `lan_interrupt` is the explicit Ctrl+C-equivalent and does not depend on window focus/background status.

---

## Worker REST

### `POST /worker/register`

```json
{"name":"win-ui","env":"windows","agent":"claude",
 "caps":["claude","browser:playwright"],"version":"1.9.0",
 "pid":1234,"hostname":"DESKTOP"}
```

Idempotent (upsert). Sets `last_seen`; `lan_envs` reports `online` when `last_seen` is under 45 s old. `caps` is used by `required_caps` matching; `browser` matches `browser:playwright` and `browser:headless`. The response includes `previous_task` (the task id this worker was running before a restart) so the worker can immediately call `/worker/recover` with `force:true` instead of waiting for the lease.

### `POST /worker/claim`

```json
{"name":"win-ui","env":"windows"}
```

`{"task": {...}}` or `{"task": null}`. Claims the oldest queued task matching `env` and `required_caps`; an empty task `env` is claimable by any worker. Atomic; a lost race returns `null`, not an error. The returned task includes:

- `claim_token` — required on every subsequent progress/log/submit;
- `log_seq` — append logs after this sequence;
- `lease_s`, `timeout_s`, `context`, `kind`, `browser`, `discover_spec`, `artifact_globs`.

Claiming moves the task to `running` and sets `lease_expires_at`.

### `POST /worker/progress`

```json
{"name":"win-ui","task_id":"...","claim_token":"..."}
```

Renews the lease and returns `{"ok":true,"cancel":[],"status":"running"}`. If the task was cancelled, requeued, or the token is stale, returns `{"ok":false,"invalid":true}` (or `stale`) — the worker must terminate the process and not submit. Workers call this every ~3 s.

### `POST /worker/log`

```json
{"name":"win-ui","task_id":"...","claim_token":"...",
 "lines":[{"seq":41,"stream":"stdout","text":"compiling..."}]}
```

Appends lines with `INSERT OR IGNORE` on `(task_id, seq)`, renews the lease, returns `{"ok":true,"next_seq":41}`. If the task is no longer active, returns `{"ok":false,"stale":true}`.

### `POST /worker/submit`

```json
{"name":"win-ui","task_id":"...","claim_token":"...","status":"submitted",
 "exit_code":0,"verify_ok":true,
 "verify_out":"$ npm run build\n(exit 0)\n...",
 "result":"agent stdout...","diff":"diff --git ...",
 "changed":"M src/app.css","composed_prompt":"# lanbus ...",
 "artifacts":[
   {"name":"shot.png","mime":"image/png","size":1234,
    "sha256":"...","data":"<base64>"}
 ]}
```

`status` is `submitted` on agent success, `failed` if the agent process failed (non-zero exit or timeout), `cancelled` if the worker was told to stop. A failing **acceptance command** still submits (as `submitted`) with `verify_ok:0` — that is exactly what the reviewer needs to rework.

Artifacts may be:

- inline base64 (`data`) — works across machines;
- `{"path": "..."}` or a plain path — legacy shared-filesystem mode;
- with `name` containing subdirectories (`dist/icon.png`), stored safely under `artifacts/<task_id>/`.

The hub verifies SHA-256 when supplied, enforces size/count limits, and rejects submissions unless `worker` + `claim_token` match an active task. A duplicate submit returns `{"ok":true,"ignored":"already submitted"}`. If the lease expired and the task went back to `queued`, a late submit is accepted only when its token matches `last_claim_token` and the task has not been claimed again — so an outbox flush after a hub outage does not lose finished work. After cancel/rework, old tokens are invalid.

### `POST /worker/heartbeat`

```json
{"name":"win-ui"}
```

Returns `{"ok":true,"cancel":["task-id", ...]}` so an idle/executing worker learns about cancellations quickly.

### `POST /worker/recover`

```json
{"name":"win-ui","max_age_s":5}
```

Called after registration. Requeues `claimed`/`running` tasks owned by this worker whose heartbeat is older than `max_age_s` (or whose lease expired), incrementing `recoveries`. Returns `{"recovered":[...],"failed":[...]}`.

### `POST /worker/leave`

Sets the worker offline/current task empty on graceful shutdown.

---

## Read-only HTTP

### `GET /tasks/<id>/logs?since=N&tail=200`

Agent-token auth. Same payload as `lan_logs`; supports `wait_s` for long-polling.

### `GET /artifacts/<task_id>/<name>`

Agent-token auth. Returns the raw bytes with `Content-Type`, `X-Content-SHA256`, and `Content-Disposition`. Without `<name>`, returns the artifact list.

### `GET /discover` / `GET /agents`

Agent-token auth. Query parameters map to `lan_discover` (`cidr`, `ports`, `deep`, `include_local`, `include_processes`, `timeout`, `http_timeout`, `max_hosts`, `env`, `wait_s`). Returns the same report as the MCP tool.

### `POST /ask`

Agent-token auth. Body is the `lan_ask` arguments (`host`, `port`, `endpoint`, `prompt`, `model`, `service`, `api_key`, `env`, `timeout_s`). Returns the reply JSON. This is for scripts/GUI clients that do not speak MCP.

---

## Events (`GET /events`)

Server-sent events, `text/event-stream; charset=utf-8`, one-second heartbeat comments. Supports `Last-Event-ID` and `?since=N`.

```
id: 41
event: task.rework
data: {"id":"a1b2c3d4e5f6","retry":1,"feedback":"build 没过"}
```

Event types: `task.created`, `task.claimed`, `task.running`, `task.submitted`, `task.approved`, `task.failed`, `task.cancelled`, `task.rework`, `task.recovered`, `task.log`, `task.fanout`, `worker.online`.

Discovery output is ordinary task evidence: `result` contains a 20 kB JSON summary, and the full report is the `discovery.json` artifact.

`task.log` payload: `{"id": "...", "seq": 42, "stream": "stdout", "text": "<last line, truncated>"}`.

## Health (`GET /health`)

`GET /ui` serves the dashboard HTML (public); `GET /api/envs` is authenticated.

No auth. `{"ok":true,"ts":"...","version":"1.9.0","counts":{"queued":0,"running":1,...}}` — used by Docker healthcheck and CI wait loops.
