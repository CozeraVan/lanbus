# lanbus 总览

[English overview](overview.en.md) · [返回 README](../README.zh-CN.md) · [架构](architecture.md) · [协议](protocol.md)

lanbus 用两种互补模式连接局域网里的 AI agent：

- **直连公司成员网络（默认）** —— 无 Hub 的对称 agent 节点。任一节点创建公司；其他机器向任一现有成员申请加入，人工批准一次后即可双向对话、派发项目任务，通信走加密对等链路。本机 agent 通过 `lanbus-mcp` 使用同一身份。
- **Hub 编排** —— 主 agent（A agent CLI、B agent CLI、C agent 桌面端，或任何支持 MCP 的客户端）把任务派给运行在其他机器上的 worker，再用可机器校验的证据决定通过还是打回重做。

## 1. 原理

### 对称对等节点，人工把关的成员制（直连模式）

每个节点都能发现、询问、接收消息和派发任务——没有 host/worker 主次之分，也没有中心
token 服务器。节点用 Ed25519 身份认证，载荷经 X25519/AES-GCM 加密。加入公司需要任一
现有成员机器上的人工批准一次；非成员不能借公司路径绕过审批。策略：`company`（成员
自由通信）、`each`（每次请求都需人工批准）、`pair`（旧持续配对）。公司 secret 由全部
成员共享，成员资格本身就是信任边界。

### 一个入站面，多个主动拉活的 worker（Hub 模式）

只有 **hub** 对外监听。每个 **worker** 主动向 hub 建立出站连接并拉取任务。这样
Windows/WSL/macOS 都不需要单独的入站防火墙规则；DHCP、休眠、NAT 和 IP 变化也更
容易处理。

```text
主 agent ──MCP──► hub :8788 ◄──long-poll── worker（Windows）
                             ◄──long-poll── worker（WSL）
                             ◄──long-poll── worker（macOS）
```

### 任务结论必须带证据

远端 agent 说“做好了”不构成证据。一个任务完成时包含：

- worker 的 `exit_code` 与实时日志；
- 调用方指定的验收命令在 worker 上执行的 `verify_ok`；
- 从真实工作区采集的 `diff` / `changed`；
- 带 SHA-256、MIME 的产物；
- 调用方显式的 `lan_review(approve | rework)` 决定。

执行任务的 agent 不能自己验收。打回意见会自动拼进下一轮 prompt。

### 安全的远端对话

发往远端 env 的任务先进入 `waiting_confirmation`，worker 不能领取。必须由人用
独立的 **operator token**（`~/.lanbus/operator-token.txt`）确认目标；主 agent 的
token 不能审批。面板会显示每个目标的 hostname、agent、`cwd`，因此同项目多个 agent
或不同项目的 agent 可以被人准确区分。

与 hub 同主机的本地 env（包括常见 WSL worker）走快速路径；远端确认有 TTL，可撤销。

### 零安装侦查

内置扫描器结合三类证据：

1. 本机进程（agent CLI 与桌面端可执行文件、本地模型运行时、图像/UI 工具；
   Windows 还会读取运行中的 WSL 进程）；
2. 本机 PID 到端口的映射；
3. 局域网 TCP 探测与 HTTP 指纹（本地模型运行时、OpenAI-compatible 服务、
   对话/Web UI、推理服务、Notebook 服务、浏览器 DevTools、MCP `serverInfo`、
   lanbus `/health`、FastAPI/OpenAPI AI API）。

纯 GUI、不监听端口的 agent 只能由它所在机器的进程扫描看到；在那台机器跑 worker 或
`lanbus discover` 即可补齐。

## 2. 实现方式

### 组件

| 组件 | 语言 | 职责 |
|---|---|---|
| `lanbusd` | Go | 默认 hub：SQLite 任务、worker API、MCP、SSE、产物、operator 审批、Web UI |
| `lanbus-worker` | Go | 拉取 worker：进程监督、Job Object/PTY、各 agent CLI 适配器（或 `--cmd`）、discover/ask、产物 |
| `lanbus-scan` | Go | 零安装局域网扫描器 |
| `lanbus-gui` | Go + Wails/WebView2 | Windows 桌面窗口 + 托盘，内嵌 dashboard |
| `lanbus` | Python | CLI、直连对等/公司节点、`lanbus-mcp` 本机 MCP 入口、侦查回退、浏览器助手、Python worker、Python hub 回退 |
| `lanbus-worker`（direct） | Go | 对称对等节点，与 Python 实现同一套公司协议 |
| `lanbus-hub` | Python | 参考 hub，browser/legacy 任务回退 |

### 数据与状态

- SQLite 位于 `--data-dir`（默认 `~/.lanbus`）：`tasks`、`workers`、`task_logs`、
  `artifacts`、`events`、`approvals`、`conversations`。
- 状态：`queued`、`claimed`、`running`、`waiting_confirmation`、`submitted`、
  `approved`、`failed`、`cancelled`、`rework`。
- 原子 claim + 随机 `claim_token`；heartbeat 续租；reaper 回收过期租约并限制恢复次数。
- cancel/rework 后旧提交被拒绝；恢复后仍匹配最后 claim token 的迟到提交可被接受。
- 产物写入 `<data-dir>/artifacts/<task>/<name>`，通过带鉴权的 `/artifacts/...` 提供。

### 协议面

- Worker REST：`/worker/register`、`/worker/heartbeat`、`/worker/claim`、
  `/worker/progress`、`/worker/log`、`/worker/submit`、`/worker/recover`、
  `/worker/leave`。
- MCP：`initialize`、`ping`、`tools/list`、`tools/call`。
- 工具：`lan_dispatch`、`lan_dispatch_many`、`lan_envs`、`lan_list`、
  `lan_result`、`lan_await`、`lan_logs`、`lan_review`、`lan_cancel`、
  `lan_interrupt`、`lan_artifact`、`lan_chat`、`lan_conversations`、
  `lan_discover`、`lan_ask`、`lan_shot`。
- SSE：`GET /events`。
- UI：`GET /ui`；别名 `/api/envs`、`/api/tasks`、`/conversations`、
  `POST /chat`。
- Operator：`GET /api/confirmations`、`POST /confirm/approve`、
  `POST /confirm/revoke`。
- 直连对等（端口 8790，加密）：`/peer/health`、`/peer/pair`、
  `/peer/pair/status`、`/peer/request`、`/peer/inbox`、`/peer/approve`、
  `/peer/deny`、`/peer/revoke`、`/peer/ui`。本机 MCP 工具：
  `lanbus_agent_discover`、`lanbus_agent_send`、`lanbus_agent_delegate`、
  `lanbus_agent_result`、`lanbus_company_list`、`lanbus_company_join`。

### 进程控制

- Windows：Job Object + `KILL_ON_JOB_CLOSE`；`TerminateJobObject`。
- POSIX：进程组 SIGTERM，随后 SIGKILL。
- `lan_interrupt` 杀整个进程树，而不只是父进程。
- `--pty` 在 Windows 使用 ConPTY，在 macOS/Linux 使用 Unix PTY，服务 TUI 型 agent。

## 3. 适用范围

**适合**

- 一个私有信任域 / 局域网，通常是单个 /24；
- 一个主 agent + 一个或多个 CLI/headless agent；
- Windows、WSL/Linux、macOS worker；
- 可以用退出码、diff、日志或产物验收的任务；
- 本机多 agent 对话，以及人工确认后的跨机器对话。

**不适合**

- 把 hub 直接暴露到公网；
- 在没有 worker 的机器上控制纯 GUI 应用；
- 绕过 agent 自身权限或服务条款；
- 在自有机器之外充当通用远程 shell 服务；
- 替代对 agent 输出的人工/安全审查。

**运维注意**

- hub 需要开放一个入站 TCP 端口（默认 `8788`）。
- macOS 14+ 需要给 Terminal/worker 开启本地网络权限。
- `operator-token.txt` 不要提交到仓库或放到共享盘。
- worker token 代表该 worker 的任务执行权限，按凭据管理。
- 浏览器任务需要带 Chrome/Edge 或 Playwright 的 Python worker。

## 4. 使用方法

### 直连公司成员网络

```bash
lanbus company create --name mac-agent --company-name "我的团队"
lanbus agent --name mac-agent --env macos --direct --cwd ~/project --agent claude
# 另一台机器上，先启动自己的节点：
lanbus company join --to mac-agent --name win-agent   # 在 mac-agent 本机人工批准一次
lanbus chat --to mac-agent --message "介绍一下你的环境"
lanbus run --to win-agent --cwd D:/proj --message "跑一下测试"
```

本机 MCP 入口（配置中不含 secret）：

```json
{"mcpServers": {"lanbus": {"command": "lanbus-mcp",
  "env": {"LANBUS_PEER_DIR": "/Users/me/.lanbus/peers/mac-agent"}}}}
```

### 安装与启动（Hub 模式）

```bash
pip install .
make go-hub go-worker go-gui
bin/lanbusd --bind 0.0.0.0 --port 8788 --data-dir ~/.lanbus \
  --token AGENT_TOKEN --worker-token WORKER_TOKEN
```

首次启动会生成 `~/.lanbus/operator-token.txt`。

### 启动 worker

```bash
bin/lanbus-worker --name win-a-cli --env windows \
  --hub http://HUB:8788 --token WORKER_TOKEN --cwd D:/proj --agent ADAPTER
```

本机 supervisor（Go/Python/WSL）：

```bash
lanbus local init
lanbus local up
lanbus local status
```

### 主 agent 工具

```text
lan_envs
lan_dispatch(env, prompt, context, cwd, verify, wait_s)
lan_logs(task_id, since)
lan_result(task_id)
lan_review(task_id, approve|rework, feedback)
lan_chat(envs, message, rounds)
```

远端目标会返回 `waiting_confirmation` 或 `human_confirmation_required`，直到人工确认：

```bash
lanbus confirm list
lanbus confirm approve --env macos-a-cli --ttl 1800
```

### Web / 桌面

```bash
lanbus web                 # 打开 dashboard
lanbus gui                 # Wails 窗口 + 托盘（Windows）
```

### 构建、测试、打包

```bash
make test                  # Python 测试
make go-test               # Go 测试
python -m pytest -q        # 全量（需要 Go hub 二进制）
python scripts/package_windows.py
```

真实 Windows ↔ macOS 运行（含人工确认和远端进程终止）见
[跨机器案例](case-studies/cross-machine-test.md)。
