# lanbus 直连 worker 登录自启（无需管理员；当前用户 LIMITED 权限）
# 用法:
#   pwsh -File .\install-direct-task.ps1 -Name go-agent -Port 8791 -Cwd D:\proj
#   pwsh -File .\install-direct-task.ps1 -Name go-agent -Remove
# 默认使用仓库 bin\lanbus-worker-go.exe（Go 原生，无 Python 依赖）。

param(
    [string]$Name = "go-agent",
    [int]$Port = 8791,
    [string]$Cwd = "$PWD",
    [string]$Agent = "claude",
    [string]$Exe = "",
    [string]$PermissionMode = "",
    [switch]$Remove
)

$ErrorActionPreference = "Stop"
$taskName = "lanbus-direct-$Name"

if ($Remove) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name $taskName -ErrorAction SilentlyContinue
    Write-Host "removed: $taskName"
    return
}

if (-not $Exe) {
    $repoBin = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) "bin\lanbus-worker-go.exe"
    if (Test-Path $repoBin) { $Exe = $repoBin }
    else { throw "找不到 lanbus-worker-go.exe，请用 -Exe 指定" }
}

$workerArgs = "--name $Name --env windows --direct --direct-port $Port --cwd `"$Cwd`" --agent $Agent"
if ($PermissionMode) { $workerArgs += " --agent-permission-mode $PermissionMode" }

$action = New-ScheduledTaskAction -Execute $Exe -Argument $workerArgs -WorkingDirectory $Cwd
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero)

try {
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
        -Settings $settings -Description "lanbus zero-config direct peer ($Name)" -Force -ErrorAction Stop | Out-Null
    Write-Host "registered scheduled task: $taskName"
    Write-Host "start now:  Start-ScheduledTask -TaskName $taskName"
    Write-Host "status:     Get-ScheduledTask -TaskName $taskName | Get-ScheduledTaskInfo"
} catch {
    # 无管理员权限（UAC 过滤令牌）时回退 HKCU Run 键：登录自启，同样免管理员。
    $runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
    $cmd = "`"$Exe`" $workerArgs"
    Set-ItemProperty -Path $runKey -Name $taskName -Value $cmd
    Write-Host "registered HKCU Run autostart (no admin): $taskName"
    Write-Host "command:    $cmd"
    Write-Host "start now:  Start-Process `"$Exe`" -ArgumentList '$workerArgs' -WorkingDirectory `"$Cwd`""
}
Write-Host "approval:   lanbus peer-ui --name $Name   (本机审批页面)"