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
$script:CleanupFailed = $false
$script:CurrentHub = "setup"
$script:StartedAt = [DateTime]::UtcNow

function New-RunDirectory {
    $parent = if ($ReportDirectory) { $ReportDirectory } else { [IO.Path]::GetTempPath() }
    $resolvedParent = [IO.Path]::GetFullPath($parent)
    New-Item -ItemType Directory -Force -Path $resolvedParent | Out-Null
    $name = "lanbus-validation-{0}-{1}" -f (Get-Date -Format "yyyyMMdd-HHmmss"), (Get-Random -Maximum 1000000)
    $script:RunRoot = Join-Path $resolvedParent $name
    New-Item -ItemType Directory -Path $script:RunRoot | Out-Null
}

function Invoke-Checked([string]$File, [string[]]$Arguments, [string]$Cwd) {
    Push-Location $Cwd
    try {
        & $File @Arguments 2>&1 | ForEach-Object { "$_" } |
            Set-Content -Path (Join-Path $script:RunRoot "build.log") -Append -Encoding UTF8
        if ($LASTEXITCODE -ne 0) {
            throw "命令失败（$LASTEXITCODE）：$File；详见 build.log"
        }
    } finally {
        Pop-Location
    }
}

function Get-FreePort {
    $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function ConvertTo-ArgumentLine([string[]]$Arguments) {
    return (($Arguments | ForEach-Object {
        if ($_ -match '[\s"]') { '"' + ($_ -replace '"', '\"') + '"' } else { $_ }
    }) -join " ")
}

function Start-RecordedProcess([string]$File, [string[]]$Arguments, [string]$Cwd, [string]$OutLog, [string]$ErrLog) {
    $line = ConvertTo-ArgumentLine $Arguments
    $process = Start-Process -FilePath $File -ArgumentList $line -WorkingDirectory $Cwd `
        -RedirectStandardOutput $OutLog -RedirectStandardError $ErrLog -PassThru -WindowStyle Hidden
    $script:Processes.Add($process)
    return $process
}

function Stop-RecordedTree([System.Diagnostics.Process]$Process) {
    if ($null -eq $Process -or $Process.HasExited) { return }
    & taskkill /PID $Process.Id /T /F 2>&1 | Out-Null
    $Process.WaitForExit(10000) | Out-Null
    $Process.Refresh()
    if (-not $Process.HasExited) { throw "无法清理 PID $($Process.Id)" }
}

function Stop-AllRecorded {
    $ordered = @($script:Processes)
    [Array]::Reverse($ordered)
    foreach ($process in $ordered) {
        try { Stop-RecordedTree $process }
        catch {
            $script:CleanupFailed = $true
            Write-Warning $_.Exception.Message
        }
    }
}

function Invoke-RestGet([string]$Uri, [string]$Token = "", [string]$HostName = "") {
    $request = [System.Net.HttpWebRequest]::Create($Uri)
    $request.Method = "GET"
    $request.Timeout = 10000
    $request.ReadWriteTimeout = 10000
    if ($Token) { $request.Headers["Authorization"] = "Bearer $Token" }
    if ($HostName) { $request.Host = $HostName }
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch [System.Net.WebException] {
        $response = $_.Exception.Response
    } catch {
        return [pscustomobject]@{ Status = 0; Body = $_.Exception.Message }
    }
    if ($null -eq $response) { return [pscustomobject]@{ Status = 0; Body = "无 HTTP 响应" } }
    try {
        $reader = New-Object IO.StreamReader($response.GetResponseStream())
        $body = $reader.ReadToEnd()
        $reader.Dispose()
        return [pscustomobject]@{ Status = [int]$response.StatusCode; Body = $body }
    } finally {
        $response.Close()
    }
}

function Invoke-McpTool([string]$Base, [string]$Token, [string]$Name, [hashtable]$Arguments) {
    $body = @{
        jsonrpc = "2.0"; id = 1; method = "tools/call"
        params = @{ name = $Name; arguments = $Arguments }
    } | ConvertTo-Json -Depth 12 -Compress
    $response = Invoke-RestMethod -Uri "$Base/mcp" -Method Post -ContentType "application/json" `
        -Headers @{ Authorization = "Bearer $Token" } -Body $body -TimeoutSec 30
    if ($response.PSObject.Properties["error"]) {
        throw "MCP transport error: $($response.error | ConvertTo-Json -Compress)"
    }
    return ($response.result.content[0].text | ConvertFrom-Json)
}

function Invoke-WorkerPost([string]$Base, [string]$Token, [string]$Operation, [hashtable]$Payload) {
    return Invoke-RestMethod -Uri "$Base/worker/$Operation" -Method Post -ContentType "application/json" `
        -Headers @{ Authorization = "Bearer $Token" } -Body ($Payload | ConvertTo-Json -Depth 12 -Compress) -TimeoutSec 30
}

function Wait-Health([string]$Base, [int]$TimeoutSec = 35) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    do {
        $health = Invoke-RestGet "$Base/health"
        if ($health.Status -eq 200) { return }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Hub 就绪超时：$Base"
}

function Wait-WorkerOnline([string]$Base, [string]$Token, [string]$Name, [int]$TimeoutSec = 35) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    do {
        $envs = Invoke-McpTool $Base $Token "lan_envs" @{}
        $online = @($envs.workers | Where-Object { $_.name -eq $Name -and $_.online })
        if ($online.Count -gt 0) { return }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Worker 未上线：$Name"
}

function Wait-Task([string]$Base, [string]$Token, [string]$Id, [string[]]$Statuses, [int]$TimeoutSec = 45) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    $row = $null
    do {
        $row = Invoke-McpTool $Base $Token "lan_result" @{ task_id = $Id }
        if ($Statuses -contains $row.status) { return $row }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    $last = if ($null -eq $row) { "无结果" } else { $row.status }
    throw "任务 $Id 等待 $($Statuses -join '/') 超时，最后状态=$last"
}

function Assert-True([string]$Name, [bool]$Condition, [string]$Detail = "") {
    $item = [pscustomobject]@{
        Hub = $script:CurrentHub; Name = $Name; Passed = $Condition; Detail = $Detail
        Time = [DateTime]::UtcNow.ToString("o")
    }
    $script:Results.Add($item)
    if ($Condition) {
        Write-Host "[PASS] [$script:CurrentHub] $Name"
    } else {
        Write-Host "[FAIL] [$script:CurrentHub] $Name $Detail"
        throw "断言失败：$Name $Detail"
    }
}

function Assert-Equal([string]$Name, $Expected, $Actual) {
    Assert-True $Name ($Expected -eq $Actual) "期望=$Expected，实际=$Actual"
}

function New-Task([string]$Base, [string]$Token, [string]$Env, [string]$Prompt, [hashtable]$Extra = @{}) {
    $arguments = @{ env = $Env; prompt = $Prompt; kind = "shell"; wait_s = 0 }
    foreach ($key in $Extra.Keys) { $arguments[$key] = $Extra[$key] }
    $created = Invoke-McpTool $Base $Token "lan_dispatch" $arguments
    if ($created.PSObject.Properties["error"]) { throw "dispatch 失败：$($created.error)" }
    return $created.task_id
}

function New-FakeSubmitted([string]$Base, [string]$WorkerToken, [string]$AgentToken, [string]$Env, [hashtable]$Extra, [string]$ResultText) {
    $id = New-Task $Base $AgentToken $Env "state regression" $Extra
    $name = "fake-$Env"
    Invoke-WorkerPost $Base $WorkerToken "register" @{ name = $name; env = $Env; caps = @() } | Out-Null
    $claimed = Invoke-WorkerPost $Base $WorkerToken "claim" @{ name = $name; env = $Env }
    Assert-True "虚拟 Worker 领取任务" ($null -ne $claimed.task -and $claimed.task.id -eq $id)
    $oldArtifact = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("old"))
    Invoke-WorkerPost $Base $WorkerToken "submit" @{
        name = $name; task_id = $id; claim_token = $claimed.task.claim_token
        status = "submitted"; exit_code = 0; verify_ok = $true
        verify_out = "verify-ok"; result = $ResultText; diff = "old-diff"
        changed = ""; composed_prompt = "state regression"
        artifacts = @(@{ name = "old.txt"; mime = "text/plain"; data = $oldArtifact })
    } | Out-Null
    return @{ Id = $id; Name = $name; Env = $Env; Claim = $claimed.task }
}

function Submit-FakeAgain([string]$Base, [string]$WorkerToken, $Fake, [string]$ResultText) {
    $claimed = Invoke-WorkerPost $Base $WorkerToken "claim" @{ name = $Fake.Name; env = $Fake.Env }
    Assert-True "虚拟 Worker 再次领取" ($null -ne $claimed.task -and $claimed.task.id -eq $Fake.Id)
    Invoke-WorkerPost $Base $WorkerToken "submit" @{
        name = $Fake.Name; task_id = $Fake.Id; claim_token = $claimed.task.claim_token
        status = "submitted"; exit_code = 0; verify_ok = $true
        verify_out = "new-verify"; result = $ResultText; diff = "new-diff"
        changed = ""; composed_prompt = "next"; artifacts = @()
    } | Out-Null
    $Fake.Claim = $claimed.task
}

function Get-ClaimToken([string]$DataDir, [string]$TaskId) {
    $code = 'import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); r=c.execute("SELECT claim_token FROM tasks WHERE id=?",(sys.argv[2],)).fetchone(); print(r[0] if r and r[0] else "")'
    $token = & python -c $code (Join-Path $DataDir "bus.db") $TaskId
    if ($LASTEXITCODE -ne 0) { throw "读取隔离数据库 Claim Token 失败" }
    return "$token".Trim()
}

function Start-HubProcess([string]$HubKind, [string]$Base, [int]$Port, [string]$DataDir, [string]$AgentToken, [string]$WorkerToken, [string]$RunDir) {
    if ($HubKind -eq "go") {
        $file = Join-Path $Root "bin\lanbusd.exe"
        $arguments = @("--bind", "127.0.0.1", "--port", "$Port", "--data-dir", $DataDir,
            "--token", $AgentToken, "--worker-token", $WorkerToken)
    } else {
        $file = (Get-Command python).Source
        $arguments = @("-m", "lanbus", "hub", "--bind", "127.0.0.1", "--port", "$Port",
            "--data-dir", $DataDir, "--token", $AgentToken, "--worker-token", $WorkerToken)
    }
    return Start-RecordedProcess $file $arguments $Root (Join-Path $RunDir "hub.out.log") (Join-Path $RunDir "hub.err.log")
}

function Start-WorkerProcess([string]$HubKind, [string]$Base, [string]$WorkerToken, [string]$Env, [string]$RunDir, [string]$Name = "") {
    if (-not $Name) { $Name = "win-smoke-$HubKind" }
    if ($HubKind -eq "go") {
        $file = Join-Path $Root "bin\lanbus-worker.exe"
        $arguments = @("--name", $Name, "--env", $Env, "--hub", $Base, "--token", $WorkerToken,
            "--cwd", $Root, "--agent", "echo")
    } else {
        $file = (Get-Command python).Source
        $arguments = @("-m", "lanbus", "worker", "--name", $Name, "--env", $Env, "--hub", $Base,
            "--token", $WorkerToken, "--cwd", $Root, "--agent", "echo")
    }
    $process = Start-RecordedProcess $file $arguments $Root (Join-Path $RunDir "worker-$Name.out.log") (Join-Path $RunDir "worker-$Name.err.log")
    return @{ Process = $process; Name = $Name }
}

function Invoke-SecurityChecks([string]$Base, [string]$AgentToken, [string]$WorkerToken) {
    Assert-Equal "health 无 token 公开" 200 (Invoke-RestGet "$Base/health").Status
    Assert-Equal "health 外部 Host 保持公开" 200 (Invoke-RestGet "$Base/health" "" "evil.example.com").Status
    Assert-Equal "agent token 可访问 agent API" 200 (Invoke-RestGet "$Base/api/envs" $AgentToken).Status
    Assert-Equal "外部 Host 被拒绝" 403 (Invoke-RestGet "$Base/api/envs" $AgentToken "evil.example.com").Status
    $missing = Invoke-RestGet "$Base/api/envs"
    Assert-True "无 token 被拒绝" ($missing.Status -eq 401 -or $missing.Status -eq 403) "HTTP=$($missing.Status)"
    $wrong = Invoke-RestGet "$Base/api/envs" "wrong-token"
    Assert-True "错误 token 被拒绝" ($wrong.Status -eq 401 -or $wrong.Status -eq 403) "HTTP=$($wrong.Status)"
    $workerOnly = Invoke-RestGet "$Base/api/envs" $WorkerToken
    Assert-True "worker token 不可访问 agent API" ($workerOnly.Status -eq 401 -or $workerOnly.Status -eq 403) "HTTP=$($workerOnly.Status)"
}

function Invoke-StateChecks([string]$Base, [string]$AgentToken, [string]$WorkerToken, [string]$HubKind) {
    $idleEnv = "idle-$HubKind"
    $zeroId = New-Task $Base $AgentToken $idleEnv "zero" @{ max_retry = 0; max_recoveries = 0 }
    $zero = Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $zeroId }
    Assert-Equal "显式零重试" 0 $zero.max_retry
    Assert-Equal "显式零恢复" 0 $zero.max_recoveries
    $negativeId = New-Task $Base $AgentToken $idleEnv "negative" @{ max_retry = -4; max_recoveries = -3 }
    $negative = Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $negativeId }
    Assert-Equal "负重试归零" 0 $negative.max_retry
    Assert-Equal "负恢复归零" 0 $negative.max_recoveries

    $queuedApprove = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $zeroId; verdict = "approve" }
    Assert-True "queued 不可 approve" ($null -ne $queuedApprove.error)
    $queuedRework = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $zeroId; verdict = "rework" }
    Assert-True "queued 不可 rework" ($null -ne $queuedRework.error)
    Assert-Equal "非法审批不改状态" "queued" (Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $zeroId }).status

    $stateEnv = "state-$HubKind"
    $zeroRetry = New-FakeSubmitted $Base $WorkerToken $AgentToken $stateEnv @{ max_retry = 0 } "zero-retry"
    $zeroRetryFailed = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $zeroRetry.Id; verdict = "rework"; feedback = "不允许重试" }
    Assert-Equal "零重试第一次打回即失败" "failed" $zeroRetryFailed.status

    $normal = New-FakeSubmitted $Base $WorkerToken $AgentToken $stateEnv @{ max_retry = 2 } "old-result"
    $firstReview = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $normal.Id; verdict = "rework"; feedback = "第一轮反馈" }
    Assert-Equal "第一次打回重新排队" "queued" $firstReview.status
    $first = Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $normal.Id }
    Assert-True "打回清理旧证据" (-not $first.result -and -not $first.diff -and -not $first.verify_out -and @($first.artifacts).Count -eq 0)
    Submit-FakeAgain $Base $WorkerToken $normal "second-result"
    $secondReview = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $normal.Id; verdict = "rework"; feedback = "第二轮反馈" }
    Assert-Equal "第二次打回重新排队" "queued" $secondReview.status
    $second = Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $normal.Id }
    Assert-True "反馈累计" ($second.feedback.Contains("第一轮反馈") -and $second.feedback.Contains("第二轮反馈"))
    Assert-True "第二轮再次清理证据" (-not $second.result -and -not $second.diff -and -not $second.verify_out)
    Invoke-McpTool $Base $AgentToken "lan_cancel" @{ task_id = $normal.Id; reason = "state regression complete" } | Out-Null

    $terminal = New-FakeSubmitted $Base $WorkerToken $AgentToken $stateEnv @{ max_retry = 1 } "terminal"
    $approved = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $terminal.Id; verdict = "approve" }
    Assert-Equal "submitted 可审批" "approved" $approved.status
    $lateRework = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $terminal.Id; verdict = "rework" }
    Assert-True "approved 终态不可打回" ($null -ne $lateRework.error)
}

function Invoke-CoreChecks([string]$Base, [string]$AgentToken, [string]$WorkerToken, [string]$Env, [string]$HubKind) {
    $id = New-Task $Base $AgentToken $Env "python --version" @{ verify = "python --version"; max_retry = 1 }
    $done = Wait-Task $Base $AgentToken $id @("submitted") 60
    Assert-Equal "shell 退出码" 0 $done.exit_code
    Assert-True "验收命令通过" ($done.verify_ok -eq $true -or $done.verify_ok -eq 1)
    Assert-True "任务结果非空" (-not [string]::IsNullOrWhiteSpace($done.result))
    Assert-True "日志已上传" ($done.log_count -ge 1)
    $approved = Invoke-McpTool $Base $AgentToken "lan_review" @{ task_id = $id; verdict = "approve" }
    Assert-Equal "真实任务审批" "approved" $approved.status

    $utfCommand = 'python -u -c "print(''界面🙂'' * 20000)"'
    $utfId = New-Task $Base $AgentToken $Env $utfCommand @{}
    $utf8 = Wait-Task $Base $AgentToken $utfId @("submitted") 60
    Assert-True "中文 UTF-8 可读" ($utf8.result.Contains("界面") -or $utf8.log_tail.Contains("界面"))
    Assert-Equal "长输出任务正常提交" 0 $utf8.exit_code

    $cancelId = New-Task $Base $AgentToken $Env 'python -c "import time; time.sleep(60)"' @{}
    Wait-Task $Base $AgentToken $cancelId @("running") 30 | Out-Null
    Invoke-McpTool $Base $AgentToken "lan_cancel" @{ task_id = $cancelId; reason = "windows smoke cancel" } | Out-Null
    Assert-Equal "长任务取消" "cancelled" (Wait-Task $Base $AgentToken $cancelId @("cancelled") 30).status
}

function Invoke-RecoveryChecks([string]$Base, [string]$AgentToken, [string]$WorkerToken, [string]$Env, [string]$HubKind, $Worker, [string]$RunDir, [string]$DataDir) {
    $onceEnv = "$Env-recovery"
    $second = Start-WorkerProcess $HubKind $Base $WorkerToken $onceEnv $RunDir "win-smoke-$HubKind-second"
    $replacement = $null
    try {
        Wait-WorkerOnline $Base $AgentToken $second.Name
        $zeroId = New-Task $Base $AgentToken $Env 'python -c "import time; time.sleep(60)"' @{ lease_s = 5; max_recoveries = 0 }
        $onceId = New-Task $Base $AgentToken $onceEnv 'python -c "import time; time.sleep(60)"' @{ lease_s = 5; max_recoveries = 1 }
        Wait-Task $Base $AgentToken $zeroId @("running") 30 | Out-Null
        Wait-Task $Base $AgentToken $onceId @("running") 30 | Out-Null
        $beforeToken = Get-ClaimToken $DataDir $onceId
        Assert-True "恢复前取得 Claim Token" (-not [string]::IsNullOrWhiteSpace($beforeToken))
        Stop-RecordedTree $Worker.Process
        Stop-RecordedTree $second.Process
        Wait-Task $Base $AgentToken $zeroId @("failed") 45 | Out-Null
        Assert-Equal "零恢复首次过期失败" "failed" (Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $zeroId }).status
        $queued = Wait-Task $Base $AgentToken $onceId @("queued") 45
        Assert-Equal "一次恢复重新排队" 1 $queued.recoveries

        $replacement = Start-WorkerProcess $HubKind $Base $WorkerToken $onceEnv $RunDir "win-smoke-$HubKind-replacement"
        Wait-WorkerOnline $Base $AgentToken $replacement.Name
        Wait-Task $Base $AgentToken $onceId @("running") 45 | Out-Null
        $afterToken = Get-ClaimToken $DataDir $onceId
        Assert-True "恢复后 Claim Token 变化" ($beforeToken -ne $afterToken -and -not [string]::IsNullOrWhiteSpace($afterToken))
        $staleProgress = Invoke-WorkerPost $Base $WorkerToken "progress" @{ name = $second.Name; task_id = $onceId; claim_token = $beforeToken }
        Assert-True "旧 Claim 进度失效" (-not $staleProgress.ok)
        $staleLog = Invoke-WorkerPost $Base $WorkerToken "log" @{ name = $second.Name; task_id = $onceId; claim_token = $beforeToken; lines = @(@{seq = 99999; stream = "stdout"; text = "stale"}) }
        Assert-True "旧 Claim 日志失效" (-not $staleLog.ok)
        $staleSubmit = Invoke-WorkerPost $Base $WorkerToken "submit" @{ name = $second.Name; task_id = $onceId; claim_token = $beforeToken; status = "submitted"; result = "stale" }
        Assert-True "旧 Claim 提交失效" (-not $staleSubmit.ok -or $staleSubmit.PSObject.Properties["ignored"])
        Assert-Equal "旧提交不覆盖新执行" "running" (Invoke-McpTool $Base $AgentToken "lan_result" @{ task_id = $onceId }).status
        Invoke-McpTool $Base $AgentToken "lan_cancel" @{ task_id = $onceId; reason = "recovery smoke complete" } | Out-Null
        Wait-Task $Base $AgentToken $onceId @("cancelled") 30 | Out-Null
    } finally {
        if ($null -ne $replacement) { Stop-RecordedTree $replacement.Process }
        Stop-RecordedTree $second.Process
    }
}

function Invoke-HubValidation([string]$HubKind) {
    $script:CurrentHub = $HubKind
    $runDir = Join-Path $script:RunRoot "$HubKind-hub"
    $dataDir = Join-Path $runDir "data"
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $port = Get-FreePort
    $base = "http://127.0.0.1:$port"
    $agentToken = "agent-{0}" -f (Get-Random -Maximum 1000000000)
    $workerToken = "worker-{0}" -f (Get-Random -Maximum 1000000000)
    $env = "windows-smoke-$HubKind"
    $hubProcess = $null
    $worker = $null
    try {
        Write-Host "=== 验证 $HubKind Hub ==="
        $hubProcess = Start-HubProcess $HubKind $base $port $dataDir $agentToken $workerToken $runDir
        Wait-Health $base
        $worker = Start-WorkerProcess $HubKind $base $workerToken $env $runDir
        Wait-WorkerOnline $base $agentToken $worker.Name
        Assert-True "Worker 注册上线" $true
        Invoke-CoreChecks $base $agentToken $workerToken $env $HubKind
        Invoke-StateChecks $base $agentToken $workerToken $HubKind
        Invoke-SecurityChecks $base $agentToken $workerToken
        if ($IncludeRecovery) {
            Invoke-RecoveryChecks $base $agentToken $workerToken $env $HubKind $worker $runDir $dataDir
        }
    } catch {
        if (@($script:Results | Where-Object { $_.Hub -eq $HubKind -and -not $_.Passed }).Count -eq 0) {
            $script:Results.Add([pscustomobject]@{
                Hub = $HubKind; Name = "组合执行"; Passed = $false; Detail = $_.Exception.Message
                Time = [DateTime]::UtcNow.ToString("o")
            })
        }
        Write-Host "[FAIL] [$HubKind] $($_.Exception.Message)"
    } finally {
        try { if ($null -ne $worker) { Stop-RecordedTree $worker.Process } }
        catch { $script:CleanupFailed = $true; Write-Warning $_.Exception.Message }
        try { if ($null -ne $hubProcess) { Stop-RecordedTree $hubProcess } }
        catch { $script:CleanupFailed = $true; Write-Warning $_.Exception.Message }
        @($script:Results | Where-Object { $_.Hub -eq $HubKind }) |
            ConvertTo-Json -Depth 8 | Set-Content -Path (Join-Path $runDir "results.json") -Encoding UTF8
    }
}

function Write-Summary {
    $items = @($script:Results)
    $failed = @($items | Where-Object { -not $_.Passed })
    $summary = [pscustomobject]@{
        StartedAt = $script:StartedAt.ToString("o"); FinishedAt = [DateTime]::UtcNow.ToString("o")
        Platform = [Environment]::OSVersion.VersionString; Hub = $Hub; SkipBuild = [bool]$SkipBuild
        IncludeRecovery = [bool]$IncludeRecovery; Passed = ($failed.Count -eq 0 -and -not $script:CleanupFailed)
        Assertions = $items.Count; Failures = $failed.Count; CleanupFailed = $script:CleanupFailed
        Results = $items
    }
    $summary | ConvertTo-Json -Depth 10 | Set-Content -Path (Join-Path $script:RunRoot "summary.json") -Encoding UTF8
    $lines = @("# Windows 自动验证报告", "", "- 平台：$($summary.Platform)", "- 通过：$($summary.Passed)",
        "- 断言：$($summary.Assertions)", "- 失败：$($summary.Failures)", "- 清理失败：$($summary.CleanupFailed)", "",
        "日志可能包含测试程序输出，请按敏感信息处理。", "", "| Hub | 检查 | 结果 | 详情 |", "|---|---|---|---|")
    foreach ($item in $items) {
        $mark = if ($item.Passed) { "PASS" } else { "FAIL" }
        $detail = ($item.Detail -replace '\|', '\|' -replace "`r?`n", " ")
        $lines += "| $($item.Hub) | $($item.Name) | $mark | $detail |"
    }
    $lines | Set-Content -Path (Join-Path $script:RunRoot "summary.md") -Encoding UTF8
}

New-RunDirectory
$buildPassed = $true
try {
    if (-not $SkipBuild) {
        Write-Host "=== 构建与既有测试 ==="
        Invoke-Checked "python" @("-c", "import lanbus") $Root
        Invoke-Checked "python" @("-m", "compileall", "-q", "src", "tests") $Root
        Invoke-Checked "go" @("test", "-count=1", "./...") (Join-Path $Root "go")
        Invoke-Checked "go" @("vet", "./...") (Join-Path $Root "go")
        Invoke-Checked "go" @("build", "-trimpath", "-o", "../bin/lanbusd.exe", "./cmd/lanbusd") (Join-Path $Root "go")
        Invoke-Checked "go" @("build", "-trimpath", "-o", "../bin/lanbus-worker.exe", "./cmd/lanbus-worker") (Join-Path $Root "go")
        Invoke-Checked "go" @("build", "-trimpath", "-o", "../bin/lanbus-scan.exe", "./cmd/lanbus-scan") (Join-Path $Root "go")
        Invoke-Checked "python" @("-m", "pytest", "tests", "-q") $Root
        Invoke-Checked "python" @("-m", "pytest", "tests/test_go_hub.py", "tests/test_hub_regressions.py", "-v") $Root
    } else {
        Invoke-Checked "python" @("-c", "import lanbus") $Root
    }
    foreach ($required in @("bin\lanbusd.exe", "bin\lanbus-worker.exe")) {
        if (-not (Test-Path (Join-Path $Root $required))) { throw "缺少 $required；请先构建或移除 -SkipBuild" }
    }
} catch {
    $buildPassed = $false
    $script:Results.Add([pscustomobject]@{
        Hub = "setup"; Name = "构建与预检"; Passed = $false; Detail = $_.Exception.Message
        Time = [DateTime]::UtcNow.ToString("o")
    })
    Write-Host "[FAIL] $($_.Exception.Message)"
}

if ($buildPassed) {
    $kinds = if ($Hub -eq "both") { @("go", "python") } else { @($Hub) }
    foreach ($kind in $kinds) { Invoke-HubValidation $kind }
}

Stop-AllRecorded
Write-Summary
$failedItems = @($script:Results | Where-Object { -not $_.Passed })
if ($failedItems.Count -gt 0 -or $script:CleanupFailed -or $script:Results.Count -eq 0) {
    Write-Host "验证失败，报告保留于 $script:RunRoot"
    exit 1
}
if ($KeepReports) {
    Write-Host "验证通过，报告位于 $script:RunRoot"
} else {
    Remove-Item $script:RunRoot -Recurse -Force
    Write-Host "验证通过；临时报告已清理，可使用 -KeepReports 保留。"
}
exit 0
