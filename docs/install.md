# 安装指南（成品产品）

lanbus 以 **Go 单二进制** 分发，默认无需 Python。Python 仅用于可选的本地 supervisor 与回退 hub。

## 方式一：Windows 安装器 / 便携包

1. 从 GitHub Releases 下载 `lanbus-<ver>-setup.exe`（Inno Setup 安装器）或 `lanbus-<ver>-windows-amd64.zip`（便携包）。
2. 安装器：双击运行，选择安装目录，完成后可选桌面快捷方式。
3. 便携包：解压后运行 `start-hub.cmd` 启动 hub（首次自动生成随机 token 到 `%USERPROFILE%\.lanbus\token.txt`），`start-gui.cmd` 启动托盘 GUI。

## 方式二：macOS .pkg

```bash
sudo installer -pkg lanbus-<ver>-macos-arm64.pkg -target /
```
安装 `/usr/local/bin/{lanbus,lanbusd,lanbus-worker,lanbus-scan}` 与 LaunchDaemon `com.lanbus.hub`（开机自启 hub）。

## 方式三：Linux .deb

```bash
sudo dpkg -i lanbus_<ver>_amd64.deb
systemctl --user enable --now lanbus-hub.service
```

## 服务化（推荐，免手工脚本）

原生 CLI 一键安装/卸载/查询 hub 自启服务：

```bash
lanbus daemon install                 # Windows 计划任务(登录自启) / macOS launchd / Linux systemd user
lanbus daemon install --port 8788 --data-dir ~/.lanbus --token-file ~/.lanbus/token.txt
lanbus daemon status
lanbus daemon uninstall
```

- Windows：优先注册当前用户登录任务；受限令牌下自动回退 `HKCU\...\Run` 键（均免管理员）。
- macOS：`~/Library/LaunchAgents/com.lanbus.lanbus-hub.plist`（KeepAlive）。
- Linux：`~/.config/systemd/user/lanbus-hub.service`。

## 健康检查与备份

```bash
lanbus healthcheck --url http://127.0.0.1:8788   # HTTP 200 且 JSON 中 ok=true 时 exit 0
lanbus backup                                     # 归档 bus.db(WAL)+token 到 ~/.lanbus/backups/
lanbus restore ~/.lanbus/backups/lanbus-<ts>.zip  # 恢复（先停 hub）
```

健康检查对普通文本、无效 JSON、非对象 JSON、缺少 `ok` 或 `ok=false` 的响应均返回非零，避免把其他 HTTP 服务误判为健康 hub。

## 直连 agent（零配置）

```bash
lanbus-worker --name my-agent --direct --direct-port 8790 --agent claude --cwd /path/proj
lanbus peer-ui --name my-agent     # 本机审批页（目标侧人工授权）
```

`lanbus-worker` 和 `lanbus peer-ui` 都是原生二进制命令，无需 Python；审批密钥仅通过浏览器 URL fragment 传递，不打印到终端。

详见 `docs/autostart.md` 与 `examples/`。