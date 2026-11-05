# lanbus worker — Windows 前台启动
# 用法: pwsh -File .\start-worker.ps1
#      或改下面的变量后在计划任务里调用

$ErrorActionPreference = "Stop"

$Hub         = "http://HUB_LAN_IP:8788"
$WorkerToken = "REPLACE_WITH_WORKER_TOKEN"   # 没有单独 worker token 时填主 agent token
$Name        = "win-ui"
$Env         = "windows"
$Cwd         = "D:\proj\webui"
$Agent       = "claude"          # claude | codex | shell | echo

# WSL 若用 mirrored 网络，$Hub 可写 http://localhost:8788
# 浏览器任务：先 pip install playwright && playwright install chromium（或装 Chrome/Edge）
# 自检：lanbus doctor

& lanbus worker --name $Name --env $Env --hub $Hub --token $WorkerToken `
    --cwd $Cwd --agent $Agent`
    --artifact-glob "dist/**/*.png" --artifact-glob "*.log"