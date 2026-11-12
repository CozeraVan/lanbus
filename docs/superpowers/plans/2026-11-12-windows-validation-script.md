# Windows PowerShell Validation Script Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增无额外依赖的 Windows PowerShell 验证脚本，自动完成构建、双 Hub 实时状态机、安全边界、可选恢复和报告清理。

**Architecture:** `scripts/validate-windows.ps1` 内部划分预检、HTTP／MCP、进程生命周期、实时断言和报告五个职责区。现有 pytest 仅做脚本静态契约回归；Windows 实机运行验证真实行为，macOS 不伪报 PowerShell 或 Windows 通过。

**Tech Stack:** Windows PowerShell 5.1／PowerShell 7、.NET HttpWebRequest、Python pytest、Go 1.25。

## Global Constraints

- 不修改 MCP、REST、数据库结构或 Hub／Worker 产品逻辑。
- 不自动安装依赖，不修改防火墙、计划任务或正式 `~/.lanbus`。
- 每个组合使用独立目录、空闲端口、随机 token；报告不得泄露 token。
- 仅清理脚本记录 PID 的进程树；失败返回 1，全部通过返回 0。
- 不自动 commit、merge 或 push；每一步用测试结果作为检查点。
- 当前 macOS 只执行静态验证；Windows 5.1／7 实机结果必须单独记录。

## File Structure

- Create `scripts/validate-windows.ps1`：命令行、构建、HTTP／MCP、Hub／Worker、断言、报告。
- Create `tests/test_windows_validation_script.py`：静态安全与接口契约测试。
- Modify `docs/windows-validation.md`：一键命令、参数、报告及人工边界。
- Modify `docs/README.md`：补充脚本入口。

---

### Task 1: 脚本静态契约测试

**Files:**
- Create: `tests/test_windows_validation_script.py`
- Test: `tests/test_windows_validation_script.py`

**Interfaces:**
- Consumes: 设计规格中的 `-Hub/-SkipBuild/-IncludeRecovery/-ReportDirectory/-KeepReports`。
- Produces: `SCRIPT = Path(.../scripts/validate-windows.ps1)` 及 `script_text()`，供全部静态测试复用。

- [ ] **Step 1: 写失败测试**

```python
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "validate-windows.ps1"


def script_text() -> str:
    return SCRIPT.read_text(encoding="utf-8")


def test_script_has_supported_parameters():
    text = script_text()
    for name in ("Hub", "SkipBuild", "IncludeRecovery", "ReportDirectory", "KeepReports"):
        assert re.search(rf"\${name}\b", text)
    assert 'ValidateSet("both", "go", "python")' in text


def test_script_refuses_non_windows_and_never_changes_firewall():
    text = script_text()
    assert "Win32NT" in text
    assert "Windows 实机" in text
    for forbidden in ("New-NetFirewallRule", "Set-NetFirewallRule", "Register-ScheduledTask"):
        assert forbidden not in text


def test_script_isolated_and_cleans_recorded_process_trees():
    text = script_text()
    for required in ("Get-Random", "TcpListener", "finally", "taskkill", "/T", "/F", "Remove-Item"):
        assert required in text
    assert "Stop-Process -Name" not in text


def test_script_reports_failures_and_redacts_tokens():
    text = script_text()
    assert "summary.json" in text
    assert "summary.md" in text
    assert "exit 1" in text
    assert "ConvertTo-Json" in text
    assert "Write-Host $env:LANBUS_TOKEN" not in text
    assert "Write-Host $AgentToken" not in text
    assert "Write-Host $WorkerToken" not in text


def test_script_covers_state_security_utf8_and_optional_recovery():
    text = script_text()
    for required in ("max_retry", "max_recoveries", "rework", "evil.example.com",
                     "界面🙂", "cancel", "claim_token", "IncludeRecovery"):
        assert required in text
```

- [ ] **Step 2: 验证 RED**

Run: `.venv/bin/python -m pytest tests/test_windows_validation_script.py -q`
Expected: FAIL，首要原因是 `scripts/validate-windows.ps1` 不存在。

- [ ] **Step 3: 运行完整静态文件检查**

Run: `.venv/bin/python -m pytest tests/test_windows_validation_script.py -q`
Expected: 在 Task 2 完成前保持 RED，不通过跳过或放宽断言消除失败。

---

### Task 2: PowerShell 构建、实时验证与报告

**Files:**
- Create: `scripts/validate-windows.ps1`
- Test: `tests/test_windows_validation_script.py`

**Interfaces:**
- Consumes: Task 1 的静态契约及现有 `lanbus hub/worker`、Go `lanbusd/lanbus-worker`。
- Produces: `Invoke-McpTool`、`Invoke-RestGet`、`Invoke-WorkerPost`、`Wait-Task`、`Assert-True`、`Assert-Equal`、`Start-Hub`、`Start-Worker`、`Stop-RecordedTree`、`Write-Summary`。

- [ ] **Step 1: 实现参数、预检和构建**

脚本开头必须包含以下兼容 PowerShell 5.1 的入口，构建命令逐个检查 `$LASTEXITCODE`：

```powershell
[CmdletBinding()]
param(
    [ValidateSet("both", "go", "python")][string]$Hub = "both",
    [switch]$SkipBuild,
    [switch]$IncludeRecovery,
    [string]$ReportDirectory = "",
    [switch]$KeepReports
)
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "该脚本只能在 Windows 实机运行；macOS/Linux 仅做静态检查。"
}
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$script:Results = New-Object System.Collections.Generic.List[object]
$script:Processes = New-Object System.Collections.Generic.List[object]

function Invoke-Checked([string]$File, [string[]]$Arguments, [string]$Cwd) {
    & $File @Arguments 2>&1 | ForEach-Object { "$_" } | Set-Content -Path (Join-Path $script:RunRoot "build.log") -Append -Encoding UTF8
    if ($LASTEXITCODE -ne 0) { throw "命令失败（$LASTEXITCODE）：$File；详见 build.log" }
}
```

默认构建执行 compileall、`go test -count=1 ./...`、`go vet ./...`、三个 Windows 二进制构建、`pytest tests -q` 及 `pytest tests/test_go_hub.py tests/test_hub_regressions.py -v`。`SkipBuild` 只跳过命令，不跳过入口存在性检查。

- [ ] **Step 2: 实现 HTTP、MCP 与状态等待**

`Invoke-RestGet` 使用 `HttpWebRequest`，显式设置 `Host` 和 `Authorization`，捕获 `WebException` 并返回状态码及正文；`Invoke-McpTool` 发送 `tools/call` 并解析 `result.content[0].text`，传输错误直接抛出。核心结构：

```powershell
function Invoke-McpTool([string]$Base, [string]$Token, [string]$Name, [hashtable]$Arguments) {
    $body = @{jsonrpc="2.0"; id=1; method="tools/call"; params=@{name=$Name; arguments=$Arguments}} | ConvertTo-Json -Depth 12 -Compress
    $response = Invoke-RestMethod -Uri "$Base/mcp" -Method Post -ContentType "application/json" -Headers @{Authorization="Bearer $Token"} -Body $body -TimeoutSec 30
    if ($response.error) { throw "MCP transport error: $($response.error | ConvertTo-Json -Compress)" }
    return ($response.result.content[0].text | ConvertFrom-Json)
}
function Wait-Task([string]$Base, [string]$Token, [string]$Id, [string[]]$Statuses, [int]$TimeoutSec = 45) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    do {
        $row = Invoke-McpTool $Base $Token "lan_result" @{task_id=$Id}
        if ($Statuses -contains $row.status) { return $row }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "任务 $Id 等待 $($Statuses -join '/') 超时，最后状态=$($row.status)"
}
```

- [ ] **Step 3: 实现隔离进程与清理**

用 `TcpListener(IPAddress.Loopback, 0)` 分配端口；`Start-Process` 重定向输出并记录 PID；所有 Hub／Worker 在 `finally` 中清理。清理只针对记录 PID：

```powershell
function Stop-RecordedTree([System.Diagnostics.Process]$Process) {
    if ($null -eq $Process -or $Process.HasExited) { return }
    & taskkill /PID $Process.Id /T /F 2>&1 | Out-Null
    $Process.WaitForExit(10000) | Out-Null
    if (-not $Process.HasExited) { throw "无法清理 PID $($Process.Id)" }
}
```

Hub／Worker 启动参数中的 token 不写入控制台或摘要；日志重定向文件属于隔离测试证据，报告提示其可能含敏感输出。

- [ ] **Step 4: 实现核心与可选恢复场景**

使用真实 Worker 验证 shell、日志、验收、审批、UTF-8 和 cancel。使用唯一无真实 Worker 的 env，通过 `/worker/register|claim|submit` 精确构造 submitted 状态，避免真实 Worker 立即重领造成断言竞态。

核心断言必须覆盖：

```powershell
Assert-Equal "显式零重试" 0 $zero.max_retry
Assert-Equal "显式零恢复" 0 $zero.max_recoveries
Assert-True "queued 不可 approve" ($null -ne $queuedApprove.error)
Assert-Equal "零重试第一次打回即失败" "failed" $zeroRetryFailed.status
Assert-True "反馈累计" ($second.feedback.Contains("第一轮反馈") -and $second.feedback.Contains("第二轮反馈"))
Assert-Equal "外部 Host 被拒绝" 403 $foreign.Status
Assert-Equal "health 保持公开" 200 $foreignHealth.Status
Assert-True "中文 UTF-8 可读" ($utf8.result.Contains("界面") -or $utf8.log_tail.Contains("界面"))
```

`IncludeRecovery` 使用两个短租约长任务，强制结束已记录 Worker；断言零恢复失败、一次恢复重排、新 Claim Token 不同、旧进度／日志／提交失效，最后取消新执行并清理替代 Worker。

- [ ] **Step 5: 实现报告与退出码**

每轮组合在 `try/catch/finally` 中记录结果，单组合失败后仍可执行下一组合。写出 `results.json`、`summary.json`、`summary.md`；失败保留目录，成功按 `KeepReports` 决定保留或删除。清理失败计入失败：

```powershell
$failed = @($script:Results | Where-Object { -not $_.Passed })
Write-Summary
if ($failed.Count -gt 0 -or $script:CleanupFailed) {
    Write-Host "验证失败，报告保留于 $script:RunRoot"
    exit 1
}
if (-not $KeepReports) { Remove-Item $script:RunRoot -Recurse -Force }
exit 0
```

- [ ] **Step 6: 验证 GREEN**

Run: `.venv/bin/python -m pytest tests/test_windows_validation_script.py -q`
Expected: 全部通过。

Run: `pwsh -NoProfile -Command "[void][scriptblock]::Create((Get-Content -Raw scripts/validate-windows.ps1))"`（仅当 `pwsh` 已安装）
Expected: 解析成功、退出 0；若未安装，明确记录未执行，不安装新工具。

---

### Task 3: 文档与全量回归

**Files:**
- Modify: `docs/windows-validation.md`
- Modify: `docs/README.md`
- Test: `tests/test_windows_validation_script.py`

**Interfaces:**
- Consumes: Task 2 的最终参数及报告行为。
- Produces: 用户可复制的一键命令和明确的 Windows 实机验证边界。

- [ ] **Step 1: 更新使用文档**

在清单自动化章节前加入：

```powershell
# 完整验证：默认依次测试 Go Hub 与 Python Hub
pwsh -File scripts/validate-windows.ps1 -KeepReports

# 已构建环境的快速复测
pwsh -File scripts/validate-windows.ps1 -SkipBuild -Hub go

# 包含 Worker 强制结束及恢复
pwsh -File scripts/validate-windows.ps1 -IncludeRecovery -KeepReports
```

文档明确：在仓库根目录、已激活且安装本项目的 Python 环境运行；支持 Windows PowerShell 5.1 时可将 `pwsh` 换成 `powershell`；失败报告始终保留；防火墙、跨机、GUI／托盘继续人工验证。

- [ ] **Step 2: 更新文档索引**

将清单索引改为“Windows 实机验证清单与 `scripts/validate-windows.ps1` 自动验证脚本”，不新增无关发布说明。

- [ ] **Step 3: 运行最终验证**

Run:

```bash
.venv/bin/python -m pytest tests/test_windows_validation_script.py -q
.venv/bin/python -m compileall -q src tests
.venv/bin/python -m pytest tests -q
git diff --check
git status --short
```

Expected: 脚本契约测试通过；Python 全量测试通过且无意外跳过；diff 无空白错误。产品 Go 源码本轮未改，沿用本轮变更前最近一次全量 Go 通过结果，但若脚本修改了任何 Go 文件则必须重跑 `go test -count=1 ./... && go vet ./...`。

- [ ] **Step 4: 记录未验证边界**

最终回复只声称 macOS 上实际完成的静态／Python 验证；Windows PowerShell 5.1／7、四种组合和恢复实机结果必须由脚本在 Windows 运行后确认。保留工作区，不提交、不推送。
