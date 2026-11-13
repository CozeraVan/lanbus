# lanbus

[![CI](https://github.com/CozeraVan/lanbus/actions/workflows/ci.yml/badge.svg)](https://github.com/CozeraVan/lanbus/actions/workflows/ci.yml)

**一个入口，派活到局域网里所有其它环境的 agent。**

[English](README.md) · [中文](README.zh-CN.md) · [Overview](docs/overview.en.md) · [中文总览](docs/overview.zh-CN.md) · [架构](docs/architecture.md) · [协议](docs/protocol.md)

你跟一个主 agent 说话（A agent CLI、B agent CLI、C agent 桌面端，或任何会说 MCP 的客户端），它把任务派给跑在别的机器上的 agent —— Windows 负责 Web UI、WSL 负责 Linux 构建、macOS 负责原生打包 —— 然后**核对真实改动**，决定通过还是打回重做。

零硬依赖，只用 Python 标准库，不出局域网。可选 Playwright 解锁真实浏览器动作；本机 Chrome/Edge 提供降级截图。内置扫描器可以在**不往对方机器装任何东西**的前提下，发现局域网里已经打开的 AI agent。

```
                    你 ──► 主 agent（A agent CLI）
                                  │ MCP: lan_dispatch / lan_logs / lan_result / lan_review
                                  ▼
                        ┌───────────────────┐
                        │  lanbus hub :8788 │  SQLite 任务表 + 状态机
                        └───┬───────────┬───┘
              REST（主动拉）│           │  REST（主动拉）
                        ┌───▼────┐  ┌───▼────┐
                        │ win-ui │  │ mac-ui │
                        │windows │  │ macos  │
                        └───┬────┘  └────────┘
                            │ 同一台机器
                        ┌───▼─────┐
                        │wsl-core │
                        │  wsl    │
                        └─────────┘
```

worker 是**主动去 hub 拉活**的，所以只有 hub 那台机器需要开入站端口，三个子环境都不用配防火墙。

---

## 为什么需要它

三个环境各跑一个 agent 时，你本人就成了消息总线：在一个窗口里拿任务描述，复制到另一个窗口，做完再自己肉眼验一遍。lanbus 把这个回路换成可机器判定的证据：

| 没有 lanbus | 有 lanbus |
|---|---|
| 你手工粘贴任务 | `lan_dispatch(env, prompt, context, cwd, verify)` |
| 远程 agent 说「做好了」 | `verify_ok` = **你的**验收命令在远程机器上的退出码 |
| 你只能相信它的总结 | `diff` + `changed` 从真实工作区采出来 |
| 凭感觉判断 | `lan_review(approve \| rework, feedback)`，意见会自动拼进下一轮 prompt |
| 看不到远程在干什么 | `lan_logs` / `lanbus logs --follow` 实时流式 stdout+stderr |
| 取消只是改数据库 | `lan_cancel` 通知 worker 终止整个进程树 |
| worker 崩了任务卡死 | 租约 + 回收器自动重排，旧 claim token 的提交会被拒绝 |

子 agent 在物理上没有自证通过的权限：`lan_review` 只有调用方（主 agent）能调。

---

## 第一步：先看局域网里已经有哪些 agent

```bash
lanbus discover                 # 本机进程/服务 + 本机所在 /24
lanbus discover --cidr 192.168.1.0/24 --json
lanbus discover --hub http://HUB:8788 --env windows
```

三类证据源：

1. **本机进程** —— agent CLI 与桌面进程（A agent CLI、B agent CLI、C agent 桌面端，以及本地模型运行时、图像/UI 工具等），按可执行文件名与命令行匹配；Windows 下还会读取正在运行的 WSL 进程（`scope=wsl:<发行版>`）；
2. **本机监听端口** —— PID / 进程 / 端口对应；
3. **LAN 扫描 + HTTP 指纹** —— 本地模型运行时（`/api/tags`）、OpenAI-compatible `/v1/models`、对话/Web UI、推理服务、Notebook 服务、浏览器 DevTools、MCP `serverInfo`、lanbus `/health`，以及 FastAPI/OpenAPI AI API（`/openapi.json`）。

内置 Phase 0 Go 扫描器；Python 会自动回退到纯 Python 扫描。执行 `make go-scan` 构建原生扫描器（macOS/Linux 交叉编译用 `make go-scan-all`），报告里 `engine` 会显示 `go` 或 `python`。

Phase 1.5 还提供原生 **Go worker**：同一套 hub 协议、实时日志、产物上传，`lan_interrupt` 直接杀进程树：

```bash
bin/lanbus-worker --name go-win --env windows --hub http://HUB:8788 --token WORKER_TOKEN --cwd "D:\proj" --agent ADAPTER
```

用 `make go-worker` / `make go-worker-all` 构建。Go worker 已支持 `agent`、`shell`、`discover`、`ask`，包括原生局域网侦查和 OpenAI-compatible 模型端点调用；browser 任务仍由 Python worker 处理。

加 `--pty` 可在真实 PTY 上运行 agent（Windows 用 ConPTY，其他平台用 Unix PTY）：

```bash
bin/lanbus-worker --name tui --env tui --hub http://HUB:8788 --token TOKEN --cwd D:/proj --agent ADAPTER --pty
```

PTY 模式保留全屏/TUI CLI 的交互，同时继续使用 Job Object/进程组取消。

### 原生 Go hub（Phase 2）

原生 hub 子集已经可用：

```bash
make go-hub
bin/lanbusd --bind 127.0.0.1 --port 8789 --data-dir /tmp/lanbus-go   --token TOKEN --worker-token TOKEN
```

已实现 worker REST 协议（register/claim/progress/log/submit/recover）、SQLite 持久化、
MCP `lan_dispatch` / `lan_dispatch_many` / `lan_envs` / `lan_list` / `lan_result` /
`lan_await` / `lan_logs` / `lan_review` / `lan_cancel` / `lan_interrupt` /
`lan_artifact` / `lan_chat` / `lan_conversations` / `lan_discover` / `lan_ask` /
`lan_shot`、SSE `/events`、产物存储和 `/ui`。

`tests/test_go_hub.py` 做双跑验证：同一个 Python fake worker 分别连接 Python hub 和
`lanbusd`，对比 status、exit code、verify 和任务结果。Go hub 现在是 8788 上的默认 hub；
Python hub（`lanbus hub` / `start-hub-python.cmd`）保留为 browser/legacy 回退。

### Agent 公司成员网络（无需 Hub 或 token）

直连模式下没有 host／worker 主次之分：每个节点都能发现、询问、接收消息和派发任务。`lanbus agent` 是对称节点入口，旧的 `lanbus worker --direct` 保持兼容。显示名和实例标签用于区分同机多个 agent／窗口：

```bash
# 任一机器创建公司；也可在已有成员机器上执行加入命令
lanbus company create --name mac-agent --company-name "我的团队"
lanbus agent --name mac-agent --env macos --direct --cwd ~/project --agent claude \
  --display-name "后端 Claude" --instance-label backend
# 桌面通知仍保留，但不自动打开审批页时追加 --no-open-approval
```

另一台机器向**任一现有成员**申请加入；申请只在被请求成员的机器上出现，人工选择“批准加入”一次：

```bash
lanbus company join --to mac-agent --name a-agent
# 之后第三个节点也可以向 a-agent 申请，而不是必须回到创建者
lanbus company join --to a-agent --name c-agent
```

加入后同公司成员可直接双向通信，不再两两配对或逐次审批：

```bash
lanbus chat --to mac-agent --message "请介绍你的环境，并给出协作方案"
lanbus run --to 192.168.1.236:8790 --cwd /Users/me/project --message "检查项目并运行测试"
lanbus company show --name a-agent
lanbus company leave --name a-agent
```

本机 agent 通过 MCP 使用同一身份。把下面配置放进 MCP 客户端，`LANBUS_PEER_DIR` 必须指向该节点的数据目录；**不要写入公司 secret**：

```json
{
  "mcpServers": {
    "lanbus": {
      "command": "lanbus-mcp",
      "env": {"LANBUS_PEER_DIR": "/Users/me/.lanbus/peers/a-agent"}
    }
  }
}
```

提供 `lanbus_agent_discover`、`lanbus_agent_send`、`lanbus_agent_delegate`、`lanbus_agent_result`、`lanbus_company_list`、`lanbus_company_join` 六个工具。MCP 是本机 agent 的入口，不是中心服务器；首次加入仍需目标人工批准。

策略说明：

- `company`（默认）：同公司成员自由通信；非成员仍走人工配对，不能借公司路径绕过审批。
- `each`：严格模式，每次对话／任务都显示“来源请求指挥目标实例”，选择“批准一次”后才执行；也可用 `lanbus allow REQUEST_ID --name mac-agent`。
- `pair`：旧持续配对模式。非成员首次配对可用 `lanbus allow REQUEST_ID --name mac-agent --pair --scope chat`，或 `--session --scope project --cwd ~/project` 仅授权本次会话。会话授权空闲 30 分钟或目标重启后失效。

同一机器有多个同名 agent 时，可直接使用唯一实例标签：`lanbus chat --to backend --message "..."`；重名错误会列出机器、agent、实例、目录和 endpoint，不会默认选第一个。默认扫描包含端口 `8790`、`8791`，也可显式使用 IP:port。`lanbus revoke PEER_ID --name mac-agent` 可在本机撤销该来源的授权。

直连采用 Ed25519 身份、X25519 密钥协商和 AES-GCM 加密；公司加入凭证也经加密通道下发。私密文件在 POSIX 下使用 `0700` 目录、`0600` 文件和原子写入；审批、拒绝、撤销只接受目标本机回环请求。项目任务限制在批准的 cwd 内，但底层 agent CLI 不等于完整 OS 沙箱，授权前仍应检查来源和请求内容。**公司 secret 由全部成员共享，任一成员被攻陷都可能危及整个公司；离开公司不会自动撤销其他节点已保存的授权。**

### Hub 模式（兼容已有部署）

已有 Hub 部署仍可使用 `lanbus connect HOST` 保存按 Hub 隔离的 agent token，然后运行不带 `--to` 的 `lanbus chat --message`，自动选择 2–4 个在线 Worker。显式 `--hub`、`--token`、`--envs` 优先；远程人工确认继续使用 `lanbus web`。这套兼容流程与新的 Agent 直连互不影响。

### 本机多 agent 对话

每个本机 agent 启一个 worker（Linux 侧 A agent CLI、Windows 侧 B agent CLI、C agent 桌面端、WSL agent 各自一个 env），然后由 hub 按顺序接力：

```bash
lanbus worker --name win-a-cli --env win-a-cli --hub http://127.0.0.1:8788 --agent ADAPTER_A --cwd D:/proj
lanbus worker --name win-b-cli --env win-b-cli --hub http://127.0.0.1:8788 --agent ADAPTER_B --cwd D:/proj
lanbus worker --name win-c-ui  --env win-c-ui  --hub http://127.0.0.1:8788 --agent ADAPTER_C --cwd D:/proj

lanbus chat --envs win-a-cli,win-b-cli,win-c-ui --message "介绍一下你最擅长的能力" --rounds 2
```

同样能力对应 MCP 工具 `lan_chat`（2–4 个 env，1–6 轮）和 `POST /chat`。每一轮都是普通 lanbus 任务，`lan_logs`、`lan_result`、`lan_interrupt` 和 review 仍可用于单轮。

对话会持久化。可以列出、读取或续聊：

```bash
lanbus chat --list
lanbus chat --conversation c123abc --message "继续，总结刚才的结论" --rounds 1
```

MCP：`lan_conversations(conversation_id=...)`。REST：`GET /conversations`、`GET /conversations/<id>`。

### Web 面板

hub 在 `http://HUB:8788/ui` 提供零依赖面板（HTML 公开，API 使用 agent token）：

- 在线环境与能力
- 持久化对话列表与 transcript
- 新建 2–4 个 agent 的对话
- 继续已有对话

面板使用与 MCP/CLI 相同的接口：`GET /api/envs`、`GET /conversations`、`POST /chat`。

### 远程 agent 人工确认

跨机器目标在派单或发起对话前必须由人工确认；本机 env 走快速路径，远端目标任务进入
`waiting_confirmation`。

```bash
lanbus confirm list
lanbus confirm approve --env macos-a-cli --ttl 1800
lanbus confirm revoke --env macos-a-cli
```

审批 token 与主 agent token 分离：

```text
~/.lanbus/operator-token.txt
```

Web 面板 / GUI 会显示远端目标的 `hostname`、`agent`、`cwd`，点击“确认此 agent”；
主 agent 持有的 agent token 不能审批。同一项目有多个 agent 时，给每个 worker 不同的
env/name（cwd 可相同），由人在面板中选择确切的 env。

### 桌面 GUI / 托盘（Windows）

```bash
make go-gui
bin/lanbus-gui.exe --hub http://127.0.0.1:8788
```

Wails/WebView2 窗口 + 托盘菜单，内嵌同一套 dashboard，并把带 token 的 API 请求
代理到运行中的 hub。关闭窗口会隐藏到托盘，托盘可重新打开、在浏览器打开 `/ui`
或退出。

```bash
lanbus web          # 在默认浏览器打开 dashboard
lanbus gui          # 启动 bin/lanbus-gui.exe
make package        # 在 dist/ 生成 Windows 便携包
```

便携包和可选 Inno Setup 安装脚本在 `packaging/windows/`；包含签名脚本
`sign.ps1` 和带 SHA-256 校验的更新脚本 `update.ps1`。

### 本机 worker 常驻

```bash
lanbus local init          # 写入 ~/.lanbus/local.json
lanbus local up            # env 离线时才启动，登录自启可重复执行
lanbus local status
lanbus local down
```

`local up` 会跳过已经在线的 env，适合放进登录启动项；支持 Go worker、Python worker 和 WSL 条目（`"wsl": "Ubuntu-24.04"`）。示例见 `examples/local/README.md`。

被发现的机器不需要装 lanbus/MCP。对暴露了 HTTP API 的端点可以立刻提问：

```bash
lanbus ask --endpoint 192.168.1.50:11434 --prompt "用一句话介绍自己"
lanbus ask --endpoint 192.168.1.60:1234 --prompt "review this diff" --model qwen2.5
```

重要边界：**纯 GUI、不监听端口的 agent**（C agent 桌面端）只能在它运行的那台机器上通过进程扫描看到；要从其它机器看到它，需要那台机器跑一个 lanbus worker，或在本机执行 `lanbus discover`。远程侦查天然只能看到网络暴露出来的东西。

## 从发行版安装（成品产品）

[GitHub Releases](https://github.com/CozeraVan/lanbus/releases) 提供三平台预编译安装器，**无需 Python**：

| 平台 | 产物 | 安装 |
|---|---|---|
| Windows | `lanbus-<ver>-setup.exe` / 便携 `.zip` | 运行安装器，或解压后 `start-hub.cmd` |
| macOS | `lanbus-<ver>-macos-arm64.pkg` | `sudo installer -pkg … -target /` |
| Linux | `lanbus_<ver>_amd64.deb` | `sudo dpkg -i …` |

每个包内含原生二进制 `lanbus`（CLI）、`lanbusd`（hub）、`lanbus-worker`、`lanbus-scan`，以及 Windows GUI；发行附 `SHA256SUMS.txt`。详见 [docs/install.md](docs/install.md)。

### 产品 CLI 与服务化

原生 `lanbus` CLI 免 Python 运维：

```bash
lanbus version                             # 注入的发行版本号
lanbus healthcheck --url http://HUB:8788   # 仅健康 hub 返回 exit 0
lanbus peer-ui --name my-agent             # 打开本机直连 agent 审批页
lanbus backup                              # 归档 SQLite(WAL)+令牌 到 ~/.lanbus/backups/
lanbus restore ~/.lanbus/backups/lanbus-<ts>.zip
lanbus daemon install                      # hub 自启：Windows 任务 / macOS launchd / Linux systemd
lanbus daemon status
lanbus daemon uninstall
```

`lanbusd` 首次运行自动生成**强随机 agent 令牌**（持久化到 `~/.lanbus/token.txt`）；仅开发用 `--allow-dev-token`。可选 TLS（`--tls-cert` / `--tls-key`）与仅环回的 pprof 监听（`--pprof 127.0.0.1:6060`）。见 [docs/security.md](docs/security.md)。

发行由 `.github/workflows/release.yml` 在 `v*` 标签触发（跨平台二进制 + Windows 便携 ZIP／Setup EXE + macOS PKG + Linux DEB + wheel + 校验和）。见 [docs/release.md](docs/release.md)。

---

## 快速开始（3 分钟）

### 1. hub —— 放在常驻那台机器（通常是 Windows）

```bash
pip install .
make go-hub
bin/lanbusd --bind 0.0.0.0 --port 8788 --data-dir ~/.lanbus   --token 主agent口令 --worker-token worker专用口令
```

`lanbusd` 是默认 hub；Python hub 保留用于 browser/legacy 兼容：
`lanbus hub --port 8789 ...`（或 `start-hub-python.cmd`）。

`--worker-token` 可选。不传时 worker 与主 agent 共用一个口令（最省事的单口令模式）。传了之后 worker 口令只能调 `/worker/*`，不能调 `/mcp`、`/events`；主 agent 口令仍可操作 worker。需要真正的一机一口令时，用 `--worker-tokens '{"win-ui":"token1","wsl-core":"token2"}'`（或 `--worker-tokens-file`），口令只对对应 worker 名生效。

```powershell
New-NetFirewallRule -DisplayName "lanbus" -Direction Inbound -Protocol TCP `
  -LocalPort 8788 -Action Allow -Profile Private
ipconfig | findstr IPv4        # 记下 IPv4，下文叫 HUB_LAN_IP
```

运行数据（SQLite、产物、自动生成的口令）落在 `~/.lanbus`，可用 `LANBUS_DATA_DIR` 或 `--data-dir` 改。需要 TLS 时加 `--tls-cert` / `--tls-key`。

Docker 方式：

```bash
LANBUS_TOKEN=... LANBUS_WORKER_TOKEN=... docker compose up -d
```

### 2. worker —— 每个环境一个

```powershell
# Windows
lanbus worker --name win-ui --env windows --hub http://HUB_LAN_IP:8788 `
  --token worker专用口令 --cwd D:\你的项目\webui --agent ADAPTER
```
```bash
# WSL
lanbus worker --name wsl-core --env wsl --hub http://HUB_LAN_IP:8788 \
  --token worker专用口令 --cwd /mnt/d/你的项目 --agent ADAPTER
# macOS
lanbus worker --name mac-ui --env macos --hub http://HUB_LAN_IP:8788 \
  --token worker专用口令 --cwd ~/你的项目 --agent ADAPTER
```

`--agent` 选一个内置适配器（适配器清单见[架构文档](docs/architecture.md#adapters)）；`shell` 把 prompt 当命令执行，`echo` 用于连通性检查。`--cmd` 可接任意你已有的 CLI：

```bash
--cmd 'my-agent --print {prompt_file}'   # {prompt_file} 换成 prompt 文件路径
--cmd 'my-agent --headless -'             # 留空 stdout 之外的 stdin 用法则直接走 stdin
```

每个 worker 还自带：

- **实时日志**：agent 的 stdout/stderr 逐行上传；用 `lan_logs` 或 `lanbus logs --follow` 看。
- **真取消**：`lan_cancel` 通知 worker 杀整个进程树；带旧 claim token 的迟到提交会被 hub 拒绝。
- **租约 + 自动重排**：worker 中途崩溃，租约过期后 hub 自动把任务放回队列（默认 120 秒，可按任务改）。
- **产物上传**：截图/日志以 base64 上传到 hub，跨机器不再依赖共享文件系统。
- **浏览器助手**：`pip install playwright && playwright install chromium`（或装 Chrome/Edge）后，`lan_shot` 可点击、填表并截图；`lanbus doctor` 会报告当前能力。
- **即时侦查**：`lanbus discover` / `lan_discover` 在不碰对方的前提下发现局域网 AI agent；`lan_ask` 可直接向 OpenAI-compatible 模型端点提问。
- **上下文交接**：`context` + 本地 `.lanbus/context.md` + git 状态会自动拼进 prompt。

WSL 连 Windows：Win11 22H2+ 在 `%UserProfile%\.wslconfig` 写

```ini
[wsl2]
networkingMode=mirrored
```

然后 `wsl --shutdown`，之后两边都能用 `localhost`。不开镜像网络就取 `ip route show default | awk '{print $3}'`，但那个 IP 重启会变。

### 3. 主 agent —— A agent CLI（或任意 MCP 客户端）

```bash
cd 你的项目根
cp -r /path/to/lanbus/integrations/mcp-client-preset/.mcp.json.example .mcp.json
# 改 HUB_LAN_IP 与口令，再把 MCP 客户端指过来
cp -r /path/to/lanbus/integrations/mcp-client-preset/agents   .mcp/agents
cp -r /path/to/lanbus/integrations/mcp-client-preset/commands .mcp/commands
```

`integrations/mcp-client-preset` 只是一份现成预设（子 agent 提示词 + 斜杠命令文本），
可自行适配你的客户端。任何 MCP 客户端都能用：把 `http://HUB_LAN_IP:8788/mcp`
与 agent 口令注册进去，工具列表一致。首次运行时批准一次项目级 MCP 服务器，然后：

```
/lan windows 把登录页按钮圆角从 4px 改成 12px，跑 npm run build，过了截图给我
```

它会自己查环境、派单、看实时日志、等结果、读 `verify_out` 与 `diff`、判定、不过就带意见打回，通过了汇报。

<details>
<summary>等价的 MCP 配置（任何 MCP 客户端）</summary>

```json
{
  "mcpServers": {
    "lanbus": {
      "type": "http",
      "url": "http://HUB_LAN_IP:8788/mcp",
      "headers": { "Authorization": "Bearer 主agent口令" }
    }
  }
}
```
</details>

---

## MCP 工具

| 工具 | 作用 |
|---|---|
| `lan_envs` | 在线 worker、能力（`browser`）与可用 `env` 清单，派单前先调 |
| `lan_dispatch` | 派一张单；`context` 会自动拼进 prompt；`wait_s>0` 阻塞到需要验收 |
| `lan_dispatch_many` | 一次扇出到多个环境 |
| `lan_shot` | 浏览器任务：URL + 点击/填表/执行 JS + 截图 |
| `lan_await` | 等一批任务到可验收状态 |
| `lan_result` | 完整记录：输出、日志尾部、`diff`、`changed`、`verify_out`、产物；图片以 MCP image content 返回 |
| `lan_logs` | 增量实时日志（`since` 续传 / `wait_s` 长轮询） |
| `lan_artifact` | 列出/读取产物；图片与文本内联返回 |
| `lan_discover` | 无插件侦查局域网 AI agent：本机进程 + 端口 + HTTP 指纹；`env` 可从指定 worker 扫描 |
| `lan_ask` | 向发现的 OpenAI-compatible 模型端点发 prompt 并取回反馈 |
| `lan_review` | 验收闸门：`approve` / `rework`（带意见）/ `fail` |
| `lan_list` | 按状态、环境、类型过滤 |
| `lan_cancel` | 取消；会终止远端进程树 |
| `lan_interrupt` | 任务的 Ctrl+C 等价物：杀 worker 进程树，不依赖窗口焦点/前后台 |

状态机：

```
queued ──claim──► claimed ──► running ──submit──► submitted
  ▲                     │                         │
  │                     └─ 租约丢失 ──► queued    ├─ approve ─► approved
  │                        (recoveries+1)         ├─ fail ────► failed
  └──────── rework（feedback，retry+1）──────────┘
                                                  └─ cancel ──► cancelled
```

打回会把 `retry+1` 并重新排队；超过 `max_retry`（默认 2）直接置 `failed`，不会无限重试。worker 租约丢失会 `recoveries+1` 重排；超过 `max_recoveries`（默认 3）判失败。`cancelled` 是终态：worker 会收到通知并杀进程树，旧 claim token 的迟到提交一律忽略。

---

## HTTP 接口（给脚本和看板用）

除 `/health` 外全部要求 `Authorization: Bearer <token>`。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/health` | 各状态计数，免鉴权 |
| GET | `/events` | 任务/worker/日志事件 SSE 流（支持 `Last-Event-ID`） |
| GET | `/tasks/<id>/logs?since=N` | 增量执行日志 |
| GET | `/discover?cidr=...&ports=...` | 无插件侦查（也支持 `?env=windows`；`/agents` 同义） |
| POST | `/ask` | 直接向发现的 AI 端点提问 |
| GET | `/artifacts/<task_id>/<name>` | 需鉴权的产物下载 |
| POST | `/mcp` | MCP（stateless streamable HTTP） |
| POST | `/worker/register` | worker 注册 + 能力上报 |
| POST | `/worker/claim` | 原子领取任务；返回 `claim_token` |
| POST | `/worker/progress` | 续租 + 取消轮询 |
| POST | `/worker/log` | 批量上传流式日志 |
| POST | `/worker/submit` | 交回结果 + verify + diff + 内联/base64 产物 |
| POST | `/worker/heartbeat` | 心跳 + 取消提示 |
| POST | `/worker/recover` | worker 重启后交还自己的遗留任务 |

侦查本身不会在被扫描主机上开监听端口：它运行在 hub/worker/CLI 一侧，只做标准 TCP/HTTP 探测。

---

## 安全

- **双角色 token**：主 agent token（`--token`）用于 MCP/读/产物；worker token（`--worker-token`）只能调 `/worker/*`。不配置 worker token 时两者共用，保持向后兼容。可选 `--worker-tokens` 给每个 worker 独立口令，一个口令只对一个 worker 名生效。
- **worker 归属校验**：submit/progress/log 必须匹配当前任务的 worker 名和随机 `claim_token`；旧 worker 不能把已重排/已取消的任务救活。
- `Host` 头白名单：只接受 `localhost`/回环与 **IP 字面量**，挡住「域名解析到你内网 IP」这类 DNS rebinding 攻击。
- 可选 TLS：hub 用 `--tls-cert` + `--tls-key`；worker 默认正常校验证书，私有 CA 用 `--ca FILE`，仅在可信 LAN 才用 `--insecure`。
- worker 只出不进：Windows/WSL/macOS 上不监听任何端口。
- `lanbus discover` 是主动 TCP/HTTP 扫描，只在你被允许检查的网络上运行；它不利用漏洞、不修改远端状态。
- **不要**把 8788 映射到公网。要跨公网就套 WireGuard/Tailscale。

---

## 可靠性模型

- **租约**：claim 时写入 `lease_expires_at`；worker 在 agent/verify/浏览器运行期间每 ~3 秒通过 `/worker/progress` 续租。
- **回收器**：hub 把过期 `claimed`/`running` 任务重排，最多 `max_recoveries` 次，之后带证据判失败。
- **claim token**：每次 claim 一个随机 token；重排/取消后旧执行交回的结果会被拒绝。
- **outbox**：任务结束时 hub 不可达，worker 把提交写到 `~/.lanbus/outbox/<name>/`，联网后自动补交；若期间租约已过期、任务回到 queued 且还没被重新领取，hub 仍会接受这份已完成的结果。
- **执行通道优先 worker**：任务由 worker 的进程树承载，`lan_cancel` / `lan_interrupt` 直接杀进程树；窗口失焦、最小化、后台运行都不影响。UI 键盘注入只用于可见性通知，不作为可靠执行/中断通道。
- **日志**：按批流式上传，seq 去重；本地副本在 `~/.lanbus/logs/`。

---

## 上下文交接

远程 agent 是新起的 CLI 进程，lanbus 会自动拼一份自包含 prompt：

1. task id / env / cwd / kind / retry / verify / 产物 glob；
2. 你传给 `lan_dispatch` 的 `context`（背景、路径、约束、验收标准）；
3. worker 本地 `.lanbus/context.md`、`.lanbus/rules.md`、可选 `--context-file`，以及 git 分支/状态；
4. 你的 `prompt`；
5. 上一轮 `feedback`；
6. 非交互执行约束。

`lan_result` 会返回最终拼好的 prompt，能看清远端 agent 到底收到了什么。

---

## 已知边界

1. 对话上下文不会自动魔法共享。lanbus 现在会注入结构化 `context`、本地 `.lanbus/context.md` 和 git 状态，但只存在于你本地聊天里的细节仍需显式传入。
2. worker 走各 CLI 的非交互一次性模式（`-p` / `exec` / `headless`）；需要授权的动作会失败而不是弹窗。把权限模式或预授权写进该 CLI 自己的配置；内置适配器在 CLI 支持时会自动带上非交互权限参数。
3. WSL 的 `/mnt/d/...` 与 Windows 的 `D:\...` 是同一份文件，不需要同步；但两边同时改同一个文件会互相踩，派单时按文件划分。
4. WSL→Windows 需要镜像网络或动态取宿主 IP，见上文。
5. headless 浏览器降级只支持 `goto` + 截图。点击/填表/执行 JS 需要 Playwright（`pip install playwright && playwright install chromium`）。
6. 浏览器任务要求 worker 具备 `browser` 能力；`lan_envs` 会返回 `has_browser` 与 `caps`。
7. macOS 上的 agent CLI 需要在那台 Mac 上运行 worker（`--agent ADAPTER` 或 `--cmd`）或本机 `lanbus discover`；Windows 扫描器只能看到 Mac 的开放端口，看不到 GUI/CLI 进程。macOS 首次还需在“隐私与安全性 → 本地网络”允许终端/worker。
8. 纯 UI 桌面窗口没有稳定的可编程输入/中断接口。lanbus 的任务执行走 CLI/headless/worker；GUI 自动化只用于尽力通知。
7. 远程侦查只能看到在扫描端口上应答、或暴露 HTTP(S) API 的主机。纯 GUI agent 只有在本机进程扫描中可见；要覆盖某台机器，需要在那台机器跑 lanbus worker 或直接执行 `lanbus discover`。443 端口会用 HTTPS 指纹；自定义 HTTPS 端口默认不探测。
8. 侦查是启发式的：开放端口可能属于无关软件，OpenAI-compatible 端点也可能只是推理服务而不是完整工具型 agent。发送敏感 prompt 前先确认。

---

### macOS worker

如果 Mac 上的 agent CLI 连不上，通常是因为那台 Mac 没有 worker。在 Mac 上执行：

```bash
export PATH="$HOME/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
lanbus doctor
lanbus worker --name mac-a-cli --env macos --hub http://HUB_LAN_IP:8788 --token WORKER_TOKEN --cwd /Users/me/proj --agent ADAPTER
```

也可以使用原生 Go worker（Mac 不需要 Python）：`bin/lanbus-worker-darwin-arm64`（Apple Silicon）或 `bin/lanbus-worker-darwin-amd64`，配合 `examples/macos/com.lanbus.worker-go.plist`（原生 Go worker）或
`examples/macos/com.lanbus.worker.plist`（Python worker），改 `ADAPTER` 与路径即可。

也可以先跑 `examples/macos/start-worker.sh` 前台自检。连接后 `lan_envs` 会显示 `mac-a-cli`（caps 含适配器名），之后 `lan_dispatch(env="macos", ...)` 就能在 Mac 当前项目运行；`lan_cancel`/`lan_interrupt` 会杀它的进程树。

## 开发与验证

```bash
pip install -e ".[dev]"
pytest -v                     # 107 项：协议、派单/打回、实时日志、取消/中断、租约恢复、产物、角色 token、侦查、对话、Web UI、本地 worker 管理、Go hub 工具/双跑、远程确认、直连 peering、公司成员网络、MCP 入口、桌面通知、逐次审批、发布产物
make go-test                  # Go 扫描器 + worker + hub + backup + daemon 测试
make go-scan                  # 构建 bin/lanbus-scan，Python 自动检测
make go-worker                # 构建 bin/lanbus-worker
python -m pip wheel . --no-deps --no-build-isolation -w dist
```

测试会真起 hub 和多个 worker（随机端口），用真实 MCP JSON-RPC 驱动，不打桩。

## 文档

- [安装](docs/install.md) —— 预编译安装器、服务化、健康检查/备份
- [发行](docs/release.md) —— 标签流水线、安装器、签名/公证
- [安全](docs/security.md) —— 令牌、TLS、审批、pprof、备份
- [架构](docs/architecture.md) —— 组件、为什么用拉取、状态机、证据模型
- [协议](docs/protocol.md) —— 全部端点、工具 schema、错误码、SSE 事件
- [排错](docs/troubleshooting.md) —— 现象 → 原因 → 处理
- [工作日志](docs/WORKLOG.md) —— 完成度与已验证边界
- [Go 迁移评估](docs/go-migration.md) —— 是否/为何迁移到 Go，参考 magpie 的架构
- [Go 扫描器与 worker](go/README.md) —— Phase 0/1 原生二进制
- [English README](README.md) · [发布指南](PUBLISHING.md) · [变更记录](CHANGELOG.md) · [贡献指南](CONTRIBUTING.md)

## 许可

MIT
