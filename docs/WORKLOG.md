# lanbus 工作日志（长期维护）

> 本文件记录跨会话的工作完成度与遗留缺陷，随版本提交。最近更新：2026-11-12，分支 `fix/hub-state-parity`。

## 一、本轮工作完成度

### 1. 步骤 7 — Go worker 直连协议 + GUI 审批 + 服务自启 + 扫描器原生 peer 探测 ✅ 完成

| 模块 | 状态 | 说明 |
|---|---|---|
| Go 直连协议 `go/internal/peer/` | ✅ | canonical JSON / Ed25519 身份 / X25519+HKDF / AES-256-GCM，与 Python 逐字节互操作（`TestPythonInterop`） |
| Go worker 直连 `go/internal/worker/direct.go` | ✅ | `--direct` 零配置直连，目标侧人工审批，适配器 claude/codex/kimi/deepseek/shell/echo |
| GUI 审批页 `go/internal/peer/ui.go` + `approval.html` | ✅ | loopback-only `/peer/ui`，密钥经 URL fragment；与 Python `static/peer_approval.html` SHA256 一致 |
| Wails 托盘审批入口 `go/cmd/lanbus-gui/main.go` | ✅ | `peerUIEntries()` 可测化 + 单测；rundll32 打开审批页（截屏实证） |
| 扫描器 peer 探测 `go/internal/scanner/probe.go` | ✅ | `/peer/health` 探测 → `lanbus-peer`，basePorts 含 8788/8790/8791 |
| 服务自启 | ✅ | Windows 计划任务（静默提权注册，实测拉起 worker）/ macOS launchd KeepAlive / WSL systemd；见 `docs/autostart.md` 与 `examples/` |
| Python 侧 `peer.py`/`cli.py`/`discovery.py` | ✅ | `/peer/ui`、`peer-ui` 子命令、`_resolve_peer` 按 peer_id 去重+主子网优选、WSL 优先 .exe 扫描器 |

### 2. 五项边界验证 ✅ 完成

| 边界 | 结论 |
|---|---|
| Windows 计划任务提权 | ✅ `ConsentPromptBehaviorAdmin=0` 静默提权注册成功，任务实际拉起 worker（Running） |
| Wails 托盘点击链路 | ✅ 单测 + rundll32 打开「lanbus · 直连审批」标签页（截屏实证） |
| macOS claude 适配器 | ✅ 见缺陷 1 修复，Windows→Mac 端到端回复正常 |
| macOS 本地网络权限 | ✅ macOS 26.5，pf/防火墙禁用，外部持续可达；TCC 需 FDA 不可读，以实测代替 |
| 并行会话共存 | ✅ 另一 claude 会话的 discovery 重构与本轮改动共存，全量测试绿 |

### 3. 各 agent 直连协议端到端实测 ✅ 完成

| agent | 结果 | 备注 |
|---|---|---|
| echo | ✅ | 中文 UTF-8 修复后回复「你好世界」 |
| shell | ✅ | 中文 UTF-8 修复后回复「你好世界」 |
| claude | ✅ | Windows 本机 + macOS 跨机均回复「正常」 |
| codex | ✅ | 换 MiniMax-M3 + 禁 responses websocket 后回复「正常」 |
| kimi | ✅ | v2.5.3 回复「正常」 |
| deepseek | ✅ | dsh headless 回复「正常」（preset 警告非致命） |

### 4. 四项遗留缺陷修复 ✅ 完成

1. **Mac claude 经 magpie 隧道 502** → Mac claude 改直连云端 qwen Anthropic 上游（绕开 magpie 系统代理拨号问题），Windows→Mac 端到端 ok。
2. **codex 上游 403 区域限制** → `~/.codex/config.toml`：`model="MiniMax-M3"` + `[features] responses_websocket=false`，端到端 ok。
3. **Windows echo/shell 中文 GBK 乱码** → `direct.go`：echo 用 UTF-8 临时文件 + `type`（原始字节透传）；shell 用 UTF-8 .bat（首行 `chcp 65001`，cmd 逐行读取）；规避 Go exec 嵌入引号 `\"` 与 cmd OEM 解析问题。
4. **deepseek 适配器可用性** → dsh.cmd 可调用，端到端 ok。

## 二、测试结果（本轮收口）

- **Go**：`internal/peer`、`internal/worker`、`internal/scanner`、`cmd/lanbus-gui` 四包全过（`go test -count=1`）。
- **Python**：87/87（`pytest -o addopts="" -q`）。
- **跨机实测**：Windows→Mac claude、WSL→Go claude、Win→Mac echo、revoke→401→自愈重配对 均 ok。

## 三、未完成 / 遗留缺陷与边界（如实标注）

1. **magpie 网关隧道区别处理（未修网关本体）**：magpie 对隧道来源请求走系统代理拨号失败、本地走直连成功。本轮以「Mac claude 直连云端上游」绕开，**未修改 magpie 本体逻辑**；若需经 magpie 路由的其它隧道场景仍会复现，需排查 magpie 代理 bypass。
2. **codex 模型托管块可能被重置**：`model="MiniMax-M3"` 位于 `# >>> teamorouter-codex` 托管块，该工具日后可能重置模型；`responses_websocket=false` 在托管块外，持久有效。codex 仍有非致命 responses websocket stderr 警告（exit 0）。
3. **dsh headless permission preset 警告**：`configure defaultPreset explicitly`，非致命、不影响回复；可配置 headless profile `defaultPreset` 消除（未做）。
4. **Python worker echo 适配器路径差异**：本轮中文乱码修复针对 Go worker；Python `DEFAULTS["echo"]=""` 走不同路径（空命令返回 skip），其 echo 语义与 Go 不一致，**未在本轮统一**（属既有行为，非乱码缺陷）。
5. **macOS 本地网络隐私（TCC）**：sshd 无 Full Disk Access 不可读 TCC.db，以实测可达性代替；全新 Mac 若复现阻塞需在「隐私与安全性→本地网络」手动授权。
6. **Mac claude 直连依赖**：依赖 qwen 上游可达与密钥有效（密钥取自本机 magpie `providers.json`，**不入库**）；上游策略/密钥变更需同步 Mac `~/.claude/settings.json`。
7. **deepseek 仅 Windows 实测**：macOS/WSL 的 deepseek 适配器未跨机实测。
8. **echo/shell 临时文件方案假设**：依赖系统 Temp 路径无空格（当前 Administrator 满足）；含空格路径需改用短路径或引号安全方案。

## 四、关键配置变更（本机，不入库）

- Mac `~/.claude/settings.json`：直连 qwen Anthropic 上游 + 模型 qwen4-max-preview（密钥略）。
- Mac `~/Library/LaunchAgents/com.lanbus.peer.plist`：`--agent claude`（原 echo）。
- Windows `~/.codex/config.toml`：`model="MiniMax-M3"`、`[features] responses_websocket=false`。
- Windows 计划任务 `lanbus-direct-go-agent`（go-agent:8791，Administrator/Limited）。

## 五、运行态基线

- hub `:8788`；win-agent `:8790`（Python，claude）；go-agent `:8791`（Go，claude，计划任务）；mac-agent `:8790`（Go，claude，launchd）。