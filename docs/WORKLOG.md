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
- **Python**：88/88（2026-11-12 追加 echo 回归测试后，`pytest tests -q`）。
- **跨机实测**：Windows→Mac claude、WSL→Go claude、Win→Mac echo、revoke→401→自愈重配对 均 ok（上述为此前跨机验证记录，不是本轮 macOS 单机复测）。

### 2026-11-12 追加缺陷收口

- Go `peer.ServerConfig.Port=0` 不再被覆盖为 8790，测试及程序化调用可使用随机空闲端口；CLI 默认仍为 8790。全量 Go 测试不再与本机已有服务争用端口。
- Python echo 空命令直接回显原消息，与 Go 行为一致，并新增中文回显回归测试。
- Go 直连的 prompt 文件路径按平台引用；Windows echo/shell 改用 `cmd.exe` 参数数组执行，支持含空格的 Temp 路径。macOS 已验证命令构造、相关单测与 Windows worker 交叉编译，未进行 Windows 实机复测。
- 修正 `worker --direct` 帮助中的参数名为 `--direct-port`。
- 本轮验证：Python 88/88、Go `./...`、`go vet ./...`、Windows worker 交叉编译、`pip check`、`git diff --check` 均通过；原 PID 4857 的 8790 服务保持运行。

## 三、未完成 / 遗留缺陷与边界（如实标注）

1. **magpie 网关隧道区别处理（未修网关本体）**：magpie 对隧道来源请求走系统代理拨号失败、本地走直连成功。本轮以「Mac claude 直连云端上游」绕开，**未修改 magpie 本体逻辑**；若需经 magpie 路由的其它隧道场景仍会复现，需排查 magpie 代理 bypass。
2. **codex 模型托管块可能被重置**：`model="MiniMax-M3"` 位于 `# >>> teamorouter-codex` 托管块，该工具日后可能重置模型；`responses_websocket=false` 在托管块外，持久有效。codex 仍有非致命 responses websocket stderr 警告（exit 0）。
3. **dsh headless permission preset 警告**：`configure defaultPreset explicitly`，非致命、不影响回复；可配置 headless profile `defaultPreset` 消除（未做）。
4. **macOS 本地网络隐私（TCC）**：sshd 无 Full Disk Access 不可读 TCC.db，以实测可达性代替；全新 Mac 若复现阻塞需在「隐私与安全性→本地网络」手动授权。
5. **Mac claude 直连依赖**：依赖 qwen 上游可达与密钥有效（密钥取自本机 magpie `providers.json`，**不入库**）；上游策略/密钥变更需同步 Mac `~/.claude/settings.json`。
6. **deepseek 仅 Windows 实测**：macOS/WSL 的 deepseek 适配器未跨机实测。
7. **Windows 含空格 Temp 路径未实机复测**：命令构造、单元测试和交叉编译已通过，仍需在 Temp 路径含空格的 Windows 环境做一次 echo/shell 端到端确认。

## 四、关键配置变更（本机，不入库）

- Mac `~/.claude/settings.json`：直连 qwen Anthropic 上游 + 模型 qwen4-max-preview（密钥略）。
- Mac `~/Library/LaunchAgents/com.lanbus.peer.plist`：`--agent claude`（原 echo）。
- Windows `~/.codex/config.toml`：`model="MiniMax-M3"`、`[features] responses_websocket=false`。
- Windows 计划任务 `lanbus-direct-go-agent`（go-agent:8791，Administrator/Limited）。

## 五、运行态基线

- hub `:8788`；win-agent `:8790`（Python，claude）；go-agent `:8791`（Go，claude，计划任务）；mac-agent `:8790`（Go，claude，launchd）。

## 六、产品化（可上线成品）推进 — 2026-11-12

目标：从「功能可用」推进到「可签名分发、一键安装、可运维」的成品。路线 P1→P4。

### P1 Go 产品 CLI + hub 加固 ✅ 完成（本机实测）
- `internal/version`：单一版本源，ldflags `-X` 注入，与 `__version__` 对齐。
- `internal/backup` + `lanbus backup/restore`：归档 bus.db(WAL)+token+config，zip-slip 安全恢复；往返单测 + 真实备份通过。
- `internal/daemon` + `lanbus daemon install/uninstall/status`：Windows 当前用户登录任务（受限令牌自动回退 HKCU Run，免管理员）/ macOS launchd / Linux systemd user；渲染函数单测 + Windows 安装→状态→卸载闭环实测通过。
- hub TLS 服务端（`--tls-cert/--tls-key`，自签证书握手单测通过）；pprof 独立 loopback 监听（`--pprof`，非环回拒绝，单测通过）。
- 强 token 默认：`lanbusd` 无 token 时自动生成随机令牌持久化，`--allow-dev-token` 才允许 devtoken。
- 新 `go/cmd/lanbus` 产品 CLI：version/healthcheck/backup/restore/daemon；冒烟通过。

### P2 发行流水线 + 跨平台打包 ✅ 完成（三平台安装器实编 + 实机验证）
- Makefile：版本注入 LDFLAGS + `go-cli`/`go-cli-all`/`release` 目标。
- `.github/workflows/release.yml`：`v*` 标签触发，全平台 CLI 交叉编译 + Windows GUI + wheel + SHA256SUMS + GitHub Release（YAML 校验通过）。
- **16 个交叉编译目标全过**。三平台安装器**实编**：
  - Windows `dist/lanbus-1.9.0-setup.exe`（18.3MB，Inno Setup 实编；**静默安装→5 二进制 v1.9.0→静默卸载无残留**闭环通过）。
  - macOS `lanbus-1.9.0-macos-arm64.pkg`（13.7MB，Mac 实机 pkgbuild/productbuild）。
  - Linux `lanbus_1.9.0_amd64.deb`（8.4MB，WSL 实机 dpkg-deb，control/contents 有效）。
- Windows 便携包 `dist/lanbus-1.9.0-windows-amd64.zip`（含 5 二进制，CLI 运行正常）。
- 签名/更新脚本核验：`sign.ps1`（signtool+PFX+SHA256+时间戳）、`update.ps1`（下载+SHA256 校验+替换）逻辑就绪，待用户证书。

### P3 文档 ✅ 完成
- `docs/install.md`（三平台安装 + daemon 服务化 + 健康检查/备份）、`docs/release.md`（流水线 + 安装器 + 签名/公证占位 + 验收清单）、`docs/security.md`（令牌/TLS/审批/pprof/备份）。

### P4 回归收口 ✅
- Go `go vet ./...` + `go test ./...` 全绿（含 backup/daemon/hub TLS+pprof 新测试）；Python 88/88。
- **daemon 三平台实机端到端**：Windows（HKCU Run/计划任务，免管理员）/ macOS（launchd state=running）/ Linux（systemd user active）；均 install→status→healthcheck(ok, v1.9.0)→uninstall 闭环通过。

### 产品化剩余（仅用户侧机密/授权）
1. **代码签名/公证证书**（Windows PFX、Apple Developer ID）——用户机密，脚本已占位就绪，不入库。
2. **首次 tag 发行**：`git tag v1.9.x && git push origin v1.9.x` 触发 release.yml 产出正式 artifacts（需提交/推送授权）。