# Windows PowerShell 自动验证脚本设计

日期：2026-11-12
分支：`fix/hub-state-parity`

## 目标与范围

新增一个无额外测试依赖的 PowerShell 脚本，将 Windows 实机验证清单中可自动执行的部分封装为可重复运行的验证流程。脚本面向开发者和发布前验证，不替代跨机、防火墙、GUI／托盘等人工检查。

本轮只新增验证工具及文档，不修改 MCP、REST、数据库结构或 Hub／Worker 产品逻辑，不自动提交、推送、合并，也不修改 Windows 防火墙和系统设置。

## 方案

采用独立脚本 `scripts/validate-windows.ps1`，使用 Windows PowerShell 5.1 和 PowerShell 7 均可用的语法。通过 `Invoke-RestMethod` 或 .NET HTTP API 调用 MCP／REST，通过 `Start-Process` 启动 Hub 与 Worker，通过自定义断言返回明确失败信息，不引入 Pester。

不采用“仅包装现有测试”的方式，因为它无法验证真实进程、端口和 token 边界；不采用 Pester，因为额外依赖会提高发布验证成本。

## 命令行接口

```powershell
pwsh -File scripts/validate-windows.ps1 `
  [-Hub both|go|python] `
  [-SkipBuild] `
  [-IncludeRecovery] `
  [-ReportDirectory <path>] `
  [-KeepReports]
```

参数语义：

- `Hub`：默认 `both`，依次验证 Go Hub 和 Python Hub；单 Hub 模式用于定位问题。
- `SkipBuild`：跳过依赖安装、编译和既有测试，仅使用当前环境中的二进制及 Python 包执行实时验证。
- `IncludeRecovery`：增加强制结束 Worker 的恢复场景；默认不执行破坏性进程场景。
- `ReportDirectory`：指定报告父目录；默认在系统临时目录创建唯一目录。
- `KeepReports`：成功后仍保留报告；失败时无论是否指定都保留报告。

参数非法时在启动任何进程前失败并返回非零退出码。帮助文本给出最小运行示例。

## 验证阶段

### 1. 预检与构建

默认模式依次执行：

1. 检查 Python、Go、Git 和所需项目文件。
2. 复用当前 Python 环境；不自动创建或激活虚拟环境，不自动安装依赖，避免修改用户全局环境。
3. 执行 `python -m compileall -q src tests`。
4. 执行 `go test -count=1 ./...`、`go vet ./...`。
5. 构建 `bin/lanbusd.exe`、`bin/lanbus-worker.exe` 和 `bin/lanbus-scan.exe`。
6. 执行 `python -m pytest tests -q` 及显式双 Hub／回归测试。

`SkipBuild` 跳过上述命令，但仍检查 Python、Hub／Worker 启动入口及必要文件。若当前平台不是 Windows，构建阶段在运行任何修改前明确拒绝；仓库维护者可使用单独的静态测试验证脚本内容，但不能将其宣称为 Windows 实机通过。

### 2. 隔离运行环境

每个 Hub 使用独立运行目录、数据目录、空闲端口、随机 agent token 和随机 worker token。token 可通过启动参数传给子进程，但控制台输出、报告和脚本自行记录的命令日志必须脱敏，不得写入 token。

通过 `System.Net.Sockets.TcpListener` 获取空闲端口，并在启动后再次检查实际可连接性。该方式存在端口被其他进程抢先使用的固有竞态，因此启动失败必须作为验证失败报告，不能假装成功。

运行目录结构：

```text
<report-root>/
  summary.json
  summary.md
  go-hub/
    hub.log
    worker.log
    data/
    results.json
  python-hub/
    hub.log
    worker.log
    data/
    results.json
```

### 3. 进程生命周期

每个 Hub 组合在独立作用域执行：

1. 启动 Hub，重定向 stdout／stderr 到日志。
2. 轮询 `/health` 直至就绪或超时；超时后输出日志尾部并失败。
3. 启动当前项目提供的 Worker，使用唯一 name／env 和 `echo` 适配器。
4. 通过 MCP `lan_envs` 等待 Worker 在线。
5. 执行该组合的实时验证。
6. 在 `finally` 中结束本脚本启动的 Worker 和 Hub，等待退出；必要时强制结束，避免遗留进程。

脚本只结束自己记录 PID 的进程，不按进程名批量结束其他 lanbus 实例。重新运行时不得复用旧数据目录。

### 4. MCP／REST 客户端

脚本内部提供小函数：

- `Invoke-McpTool`：发送 `tools/call`，解析文本 JSON，传输错误与工具错误分别报告。
- `Invoke-Rest`：执行健康、权限及 Host 检查，可读取非 2xx 状态而不直接丢失响应。
- `Wait-Task`：按任务 ID 轮询直到目标状态或超时，失败时附最后状态。
- `Assert-Equal`、`Assert-True`：记录断言名称、期望值、实际值和所在组合。

每个结果写入当前 Hub 的 `results.json`。任何必需断言失败都使总脚本返回非零；后续独立组合可继续执行，以收集完整报告。

### 5. 实时核心验证

Go Hub 与 Python Hub 均执行：

1. Worker 注册及 `lan_envs` 在线状态。
2. shell 任务从 `queued/running` 到 `submitted`，检查退出码、验证结果、日志及审批后的 `approved`。
3. 创建 `max_retry=0, max_recoveries=0` 的任务并确认零值未变成默认值。
4. 对 `queued` 任务执行 `approve/rework`，确认被拒绝且状态不变。
5. `max_retry=0` 的 submitted 任务第一次 rework 后进入 `failed`。
6. `max_retry=2` 的任务连续打回，确认反馈累计，旧 result、diff、verify 输出和产物记录清理。
7. 中文与 emoji 长输出验证，确认响应为有效 Unicode，日志／结果可读且 Hub 未崩溃。
8. agent token 可访问 agent API；无 token、错误 token、worker token 不可访问 agent API。
9. 外部域名 Host 访问非公开路由返回 403；`/health` 保持公开；IP 字面量 Host 正常。
10. 长任务取消后进入 `cancelled`，延迟提交不得改变终态；检查本脚本启动的执行进程已结束。

浏览器、图片产物和真实 agent 适配器依赖本机可选组件，不纳入默认无依赖 smoke；继续由人工清单验证。

### 6. 可选恢复验证

指定 `IncludeRecovery` 时，对每个 Hub 额外执行：

1. 创建短租约长任务并确认 Worker 已领取。
2. 强制结束该 Worker PID。
3. `max_recoveries=0` 的任务第一次过期进入 `failed`。
4. `max_recoveries=1` 的任务第一次过期重新 queued，`recoveries=1`。
5. 启动替代 Worker，确认重新领取、Claim Token 变化并最终提交。
6. 模拟旧 Claim 的进度、日志和提交，确认不能覆盖新执行或 cancelled 终态。

恢复场景只在隔离目录运行，不接触用户既有 Worker。

## 报告与失败语义

- 控制台输出当前阶段、组合和失败断言，但不打印 token。
- `summary.json` 包含开始／结束时间、平台、参数、各阶段状态、断言结果和报告路径。
- `summary.md` 提供便于人工阅读的通过／失败表格及日志位置。
- 任一构建命令、启动、超时或必需断言失败，退出码为 `1`；全部通过为 `0`。
- 失败时保留所有报告和日志；默认成功后删除临时运行目录，`KeepReports` 可保留。
- 清理失败必须显示警告；如果遗留本脚本启动的进程，则总结果失败。

## 测试策略

### 当前 macOS 环境可执行

- PowerShell 可用时执行解析器语法检查；不可用时明确记录未执行，不声称完成 PowerShell 解析验证。
- 使用静态回归测试检查参数、默认隔离路径、非 Windows 防误跑、`finally` 清理、敏感信息不落入命令行日志等契约。
- 运行 `git diff --check` 和现有 Python／Go 测试，确认新增脚本不影响产品代码。

### Windows 实机必须执行

- Windows PowerShell 5.1 与 PowerShell 7 各运行默认流程。
- 分别运行 `-Hub go`、`-Hub python`、`-SkipBuild`。
- 运行 `-IncludeRecovery -KeepReports`，确认崩溃恢复和报告内容。
- 人为制造一个错误 token 或缺失二进制，确认非零退出、日志保留及无进程残留。
- 运行后检查正式 `~/.lanbus` 未改变，未新增防火墙规则。

## 文档

在 `docs/windows-validation.md` 增加一键自动验证用法、参数和报告位置说明，保留人工检查部分。在 `docs/README.md` 已存在的清单入口中补充脚本入口，不修改其他发布文档。

## 非目标

- 不实现跨平台 Bash 版本。
- 不自动安装 Python、Go、Pester、Playwright 或 WebView2。
- 不自动修改防火墙、签名、安装器或系统计划任务。
- 不用 macOS 静态检查代替 Windows 实机通过结论。
