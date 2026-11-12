# Windows 实机验证清单

本文用于验证 `fix/hub-state-parity` 分支在 Windows 上的构建、双 Hub 协议、Worker 执行、状态机、租约恢复和网络安全行为。自动化测试与实机场景都必须通过；不要只用单元测试代替 Windows 进程、防火墙和控制台验证。

## 1. 验证基线

- [ ] Windows 10/11 x64，记录完整系统版本。
- [ ] Python 3.12（项目最低要求为 Python 3.9）。
- [ ] Go 1.25 或兼容的更新版本。
- [ ] Git 可用；PowerShell 中从仓库根目录执行命令。
- [ ] 使用独立测试数据目录，不碰正式 `~/.lanbus` 数据。
- [ ] 使用随机测试 token，不使用 `devtoken`，不把 token 写入日志或截图。
- [ ] 记录当前提交或工作区差异，确保测试的是同一版代码。

```powershell
$Root = (Get-Location).Path
$env:LANBUS_TOKEN = "REPLACE_WITH_AGENT_TOKEN"
$env:LANBUS_WORKER_TOKEN = "REPLACE_WITH_WORKER_TOKEN"
$env:PYTHONUTF8 = "1"
$env:PYTHONIOENCODING = "utf-8"
$SmokeData = Join-Path $env:TEMP "lanbus-windows-smoke"
New-Item -ItemType Directory -Force $SmokeData | Out-Null
python --version
go version
git status --short
```

## 1.1 一键自动验证

在仓库根目录、已经激活且安装本项目及 dev 依赖的 Python 环境中运行：

```powershell
# 完整验证：依次执行 Go Hub + Go Worker、Python Hub + Python Worker
pwsh -File scripts/validate-windows.ps1 -KeepReports

# 已构建环境的快速复测，仅验证 Go 组合
pwsh -File scripts/validate-windows.ps1 -SkipBuild -Hub go

# 包含两个独立 Worker 的强制结束及恢复场景
pwsh -File scripts/validate-windows.ps1 -IncludeRecovery -KeepReports
```

Windows PowerShell 5.1 可将 `pwsh` 换成 `powershell`。脚本不自动安装依赖、不修改防火墙、不读取或复用正式 `~/.lanbus`，每个组合使用独立临时目录、端口和随机 token。

参数：

- `-Hub both|go|python`：默认 `both`，选择 Hub；Worker 使用对应实现，交叉组合仍按第 3 节人工验证。
- `-SkipBuild`：跳过构建和既有测试，只执行实时验证。
- `-IncludeRecovery`：增加 Worker 崩溃、租约恢复和旧 Claim 拒绝场景。
- `-ReportDirectory <path>`：指定报告父目录。
- `-KeepReports`：成功后保留报告；失败时始终保留报告和日志。

报告包含 `summary.json`、`summary.md`、各组合 `results.json` 及 Hub／Worker 日志。日志可能包含测试程序输出，应按敏感信息处理。任一命令、断言或清理失败时退出码为 1，全部通过为 0。

以下项目仍需按人工清单验证：四种交叉组合、真实浏览器与图片产物、跨机访问、防火墙、GUI／托盘和正式 agent 适配器。macOS／Linux 上的静态检查不能视为 Windows 实机通过。

## 2. 构建与自动化回归

### 2.1 Python 环境

```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install --upgrade pip
python -m pip install -e ".[dev]"
lanbus --version
lanbus doctor
```

- [ ] 虚拟环境激活成功，`lanbus` 来自当前虚拟环境。
- [ ] `lanbus doctor` 能运行；不存在的可选 agent／浏览器依赖应明确显示为未安装，而不是导致崩溃。

### 2.2 Go 测试与原生二进制

```powershell
Set-Location go
go test -count=1 ./...
go vet ./...
go build -trimpath -o ../bin/lanbusd.exe ./cmd/lanbusd
go build -trimpath -o ../bin/lanbus-worker.exe ./cmd/lanbus-worker
go build -trimpath -o ../bin/lanbus-scan.exe ./cmd/lanbus-scan
Set-Location $Root
```

- [ ] Go 全部包测试通过，且 `-count=1` 确保不是缓存结果。
- [ ] `go vet` 无错误。
- [ ] 三个 `.exe` 均成功生成。
- [ ] 若失败，区分工具链／依赖问题与源码失败，保留完整输出，不跳过失败包。

### 2.3 Python 与双实现协议测试

先完成 Go Hub 构建，再运行 Python 测试，确保双实现测试不会因缺少二进制而跳过。

```powershell
python -m compileall -q src tests
python -m pytest tests -q
python -m pytest tests/test_go_hub.py tests/test_hub_regressions.py -v
```

- [ ] 当前分支全量 Python 测试全部通过，无跳过、无失败，并记录通过数量。
- [ ] Go Hub 双实现测试实际执行。
- [ ] 零值、审批／打回、恢复竞态回归测试全部通过。
- [ ] `git diff --check` 无空白错误。

## 3. 四种 Hub／Worker 组合

每次只启动一个 Hub 和一个 Worker，使用独立数据目录及相同 env：`windows-smoke`。每个组合都执行第 4—8 节的核心场景。

| 组合 | Hub | Worker | 必测范围 |
|---|---|---|---|
| A | Go | Go | 执行、日志、验收、审批、恢复、取消 |
| B | Go | Python | 同上，另测浏览器与产物 |
| C | Python | Go | 执行、日志、验收、审批、恢复、取消 |
| D | Python | Python | 同上，另测浏览器与产物 |

### Go Hub

```powershell
.\bin\lanbusd.exe --bind 127.0.0.1 --port 8788 `
  --data-dir "$SmokeData\go-hub" `
  --token $env:LANBUS_TOKEN --worker-token $env:LANBUS_WORKER_TOKEN
```

### Python Hub

```powershell
lanbus hub --bind 127.0.0.1 --port 8788 `
  --data-dir "$SmokeData\python-hub" `
  --token $env:LANBUS_TOKEN --worker-token $env:LANBUS_WORKER_TOKEN
```

### Go Worker

```powershell
.\bin\lanbus-worker.exe --name win-smoke --env windows-smoke `
  --hub http://127.0.0.1:8788 --token $env:LANBUS_WORKER_TOKEN `
  --cwd $Root --agent echo
```

### Python Worker

```powershell
lanbus worker --name win-smoke --env windows-smoke `
  --hub http://127.0.0.1:8788 --token $env:LANBUS_WORKER_TOKEN `
  --cwd $Root --agent echo
```

- [ ] `http://127.0.0.1:8788/health` 返回正常状态。
- [ ] `lan_envs` 只显示预期 Worker，名称、env 和 online 状态正确。
- [ ] Worker 不监听额外入站端口。

## 4. 基本任务与证据链

通过 MCP 客户端调用 `lan_dispatch`：

```json
{
  "env": "windows-smoke",
  "kind": "shell",
  "prompt": "python --version",
  "verify": "python --version",
  "max_retry": 2,
  "max_recoveries": 1,
  "lease_s": 30,
  "wait_s": 120
}
```

随后调用 `lan_result`、`lan_logs`，最后调用 `lan_review(verdict="approve")`。

- [ ] 状态依次为 `queued → running → submitted → approved`。
- [ ] `exit_code=0`、`verify_ok=true`，结果和验证输出非空。
- [ ] stdout／stderr 顺序和编号正确，重复请求不会重复写入日志。
- [ ] Windows 路径、中文路径和包含空格的路径均能作为 `cwd` 使用。
- [ ] 执行一个验证命令必然失败的任务，`verify_ok=false`，不得误标为验证通过。
- [ ] 执行一个包含中文及 emoji 的长输出任务，Hub 不崩溃，日志和结果不是无效 UTF-8，也没有因截断产生半个字符。
- [ ] Python Worker 上传文本及图片产物后，能通过 Hub 下载，文件名和内容一致。

## 5. 审批、打回与零值边界

以下场景在 Go Hub 和 Python Hub 上分别执行。

### 非 submitted 状态

- [ ] 对仍处 `queued` 的任务执行 `approve` 和 `rework`，均返回错误，状态保持 `queued`。
- [ ] 对已 `approved` 的任务再次 `rework`，返回错误，终态不被覆盖。

### 显式零值

- [ ] 创建 `max_retry=0, max_recoveries=0` 的任务，`lan_result` 中两个值都仍为 `0`。
- [ ] `max_retry=0` 的任务第一次从 `submitted` 执行 `rework` 后直接进入 `failed`，不得重新排队。
- [ ] 负数配置按 `0` 处理，不恢复成默认值。

### 正常打回

- [ ] `max_retry=2` 的任务第一次打回后进入 `queued`，`retry` 增加 1。
- [ ] 连续两轮反馈均保留在 `feedback` 和下一次 composed prompt 中。
- [ ] 打回后旧的 `result`、`diff`、`verify_out`、退出码及产物记录被清理，不混入下一轮证据。

## 6. 崩溃恢复、旧 Claim 与 Hub 重启

使用长任务，将 `lease_s` 设为允许的小值（例如 10 秒），确认 Worker 已领取任务后强制结束 Worker 进程：

```powershell
Stop-Process -Id <WORKER_PID> -Force
```

分别验证：

- [ ] `max_recoveries=0`：第一次租约过期即进入 `failed`，不会重新排队。
- [ ] `max_recoveries=1`：第一次过期进入 `queued`，`recoveries=1`；重启 Worker 后重新领取并最终提交。
- [ ] 重新领取后生成新的 Claim Token；旧 Worker 的进度、日志和提交不能改变新任务状态。
- [ ] 旧 Worker 的延迟 outbox 提交不得覆盖已重新领取或已取消的任务。
- [ ] 任务运行期间重启 Hub，并复用原数据目录；任务、反馈、日志序号和产物记录保持完整。
- [ ] Hub 重启后 Worker 能重新注册并恢复，不产生重复任务或重复日志。

## 7. 取消与进程树

- [ ] 对长任务调用 `lan_cancel`，任务进入 `cancelled`，远程进程树结束。
- [ ] 对长任务调用 `lan_interrupt`，行为与取消一致，不依赖窗口焦点。
- [ ] 取消后的延迟提交被忽略，状态不会回到 `submitted`。
- [ ] Go Worker 使用 Windows Job Object 时，子进程及孙进程均被结束；Python Worker 结果一致。
- [ ] 如启用 `--pty`，另行验证 ConPTY 下交互式 CLI 的输出与取消行为。

## 8. Host、token 与局域网访问

```powershell
curl.exe -i http://127.0.0.1:8788/health
curl.exe -i -H "Host: evil.example.com" http://127.0.0.1:8788/health
curl.exe -i -H "Authorization: Bearer $env:LANBUS_TOKEN" http://127.0.0.1:8788/api/envs
curl.exe -i -H "Host: evil.example.com" -H "Authorization: Bearer $env:LANBUS_TOKEN" http://127.0.0.1:8788/api/envs
curl.exe -i -H "Authorization: Bearer $env:LANBUS_WORKER_TOKEN" http://127.0.0.1:8788/api/envs
```

- [ ] `/health` 无 token 可访问，外部 Host 下仍保持公开语义。
- [ ] `/api/envs` 使用 agent token 和 IP 字面量 Host 时成功。
- [ ] 非公开路由使用外部域名 Host 时返回 `403`。
- [ ] 无 token、错误 token 和 worker token 均不能访问 agent API。
- [ ] 将 Hub 绑定到 `0.0.0.0` 后，局域网另一台机器可通过 `http://HUB_LAN_IP:8788` 访问。
- [ ] Windows 防火墙仅允许 Private 配置文件的 TCP 8788，不对 Public 网络开放。
- [ ] Worker 跨机连接成功；Worker 所在机器无需新增入站规则。
- [ ] 测试结束删除临时防火墙规则，不将 8788 端口暴露到互联网。

## 9. 可选：桌面 GUI

安装 WebView2 Runtime 后：

```powershell
Set-Location go
go build -tags production -ldflags="-H=windowsgui -s -w" `
  -o ../bin/lanbus-gui.exe ./cmd/lanbus-gui
Set-Location $Root
.\bin\lanbus-gui.exe --hub http://127.0.0.1:8788
```

- [ ] 窗口和 WebView2 内容正常加载。
- [ ] 关闭窗口进入托盘，托盘可重新打开面板、打开浏览器和退出。
- [ ] GUI 代理的 API 仍执行 token 与 Host 校验。

## 10. 通过标准与结果记录

发布或合并前必须满足：

- [ ] 四种 Hub／Worker 组合的核心场景全部通过。
- [ ] 自动化测试无失败、无意外跳过。
- [ ] 无状态越权、旧 Claim 覆盖、重复日志、进程残留或中文截断异常。
- [ ] 所有失败均有 issue 或书面说明，不以“本机正常”代替修复。

记录模板：

```text
Windows 版本：
Python／Go 版本：
测试提交或差异：
Hub／Worker 组合：
自动化测试结果：
实机场景结果：
失败与日志位置：
未验证项目及原因：
验证人／日期：
```
