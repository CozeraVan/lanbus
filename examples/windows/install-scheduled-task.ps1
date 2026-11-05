# 注册为登录自启的计划任务（不需要管理员时用 -AtLogOn；用 SYSTEM 才需要管理员）
# 用法: pwsh -File .\install-scheduled-task.ps1

$ErrorActionPreference = "Stop"

$Hub         = "http://HUB_LAN_IP:8788"
$WorkerToken = "REPLACE_WITH_WORKER_TOKEN"
$Cwd         = "D:\proj\webui"

$python = (Get-Command python).Source
$workerArgs = "-m lanbus worker --name win-ui --env windows --hub $Hub --token $WorkerToken --cwd $Cwd --agent claude --artifact-glob dist/**/*.png"

$action = New-ScheduledTaskAction -Execute $python -Argument $workerArgs -WorkingDirectory $Cwd
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable

Register-ScheduledTask -TaskName "lanbus-worker-win-ui" `
    -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null

Write-Host "registered: lanbus-worker-win-ui"
Write-Host "start now:  Start-ScheduledTask -TaskName lanbus-worker-win-ui"
Write-Host "logs:       Get-ScheduledTaskInfo -TaskName lanbus-worker-win-ui"