# 直连 worker 自启动（零配置 peering）

直连模式（`--direct`）不需要 Hub 与 token：worker 启动后在局域网通告
`/peer/health`，发起方配对、**目标机人工审批**后才能执行。自启动只负责把
worker 拉起来，审批边界不变。

## Windows（登录自启，无需管理员）

```powershell
# Go 原生 worker（推荐，无 Python 依赖）
pwsh -File examples\windows\install-direct-task.ps1 -Name go-agent -Port 8791 -Cwd D:\proj

# 立即启动 / 查看 / 移除
Start-ScheduledTask -TaskName lanbus-direct-go-agent
Get-ScheduledTask -TaskName lanbus-direct-go-agent | Get-ScheduledTaskInfo
pwsh -File examples\windows\install-direct-task.ps1 -Name go-agent -Remove
```

- 任务以当前用户 LIMITED 权限运行，失败自动重启（3 次 / 间隔 1 分钟），无运行时长上限。
- Python worker 变体：`-Exe python` 不可直接用（参数不同），改用
  `New-ScheduledTaskAction -Execute python -Argument "-m lanbus worker --name win-agent --env windows --direct --cwd D:\proj --agent claude"` 手动注册。
- 审批页面：`lanbus peer-ui --name go-agent`（本机浏览器，密钥经 URL 片段带入）。

## macOS（launchd 用户代理）

```bash
# 交叉编译（在任意有 Go 1.25+ 的机器上）
GOOS=darwin GOARCH=arm64 go build -o lanbus-worker-darwin-arm64 ./cmd/lanbus-worker
scp lanbus-worker-darwin-arm64 mac:~/bin/ && ssh mac chmod +x ~/bin/lanbus-worker-darwin-arm64

# 安装模板（先编辑路径/名字/端口/工作目录）
cp examples/macos/com.lanbus.peer.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.lanbus.peer.plist
# 卸载
launchctl bootout gui/$(id -u)/com.lanbus.peer
```

`KeepAlive` 保证崩溃自动拉起；日志在 `/tmp/lanbus-peer.{out,err}`。

## WSL / Linux（systemd）

```bash
sudo cp examples/wsl/lanbus-peer@.service /etc/systemd/system/
sudo mkdir -p /etc/lanbus && sudo cp examples/wsl/peer.env.example /etc/lanbus/peer.env
sudoedit /etc/lanbus/peer.env   # 填 LANBUS_ENV/PORT/CWD/AGENT
sudo systemctl enable --now lanbus-peer@wsl-agent
journalctl -u lanbus-peer@wsl-agent -f
```

WSL 内 systemd 需已在 `/etc/wsl.conf` 启用（`[boot] systemd=true`）。

## 验证

```bash
lanbus discover            # 应看到 kind=lanbus-peer（Go 扫描器原生探测）
lanbus peer-ui --name NAME # 本机审批页面
lanbus chat --to NAME --message "你好"   # 另一台机器发起，目标机审批后回复
```

## 安全边界

- 自启动不改变授权模型：没有目标机人工批准，任何远端请求都不能执行。
- 审批页面与审批 API 仅回环可达；密钥存于 `~/.lanbus/peers/<name>/approval.json`（0600）。
- `--agent-permission-mode bypassPermissions` 只在受信局域网测试时启用；
  去掉该参数即恢复默认 `acceptEdits`。