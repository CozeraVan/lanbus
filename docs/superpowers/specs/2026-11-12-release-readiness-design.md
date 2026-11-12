# 原生审批与发布就绪性优化设计

日期：2026-11-12
分支：`fix/hub-state-parity`
基线：`558d2ef`

## 目标

在不引入 Python 运行依赖、不重写现有 Hub 或 Worker 的前提下，修复正式发布前的三个阻断项：健康检查误判、原生 CLI 缺少直连审批入口、发布流水线不生成文档承诺的安装器。最终通过 PR 将功能分支接受完整 CI 验证后合并到 `main`。

## 方案选择

采用最小原生补齐方案：

1. 严格校验现有健康接口，不改变 Hub `/health` 协议。
2. 抽取本机审批页发现逻辑供原生 CLI 与 Windows 托盘共用，不引入 Python。
3. 复用现有 Windows/macOS/Linux 打包脚本扩充 release workflow，不建立第二套打包系统。

未采用两个替代方案：让原生安装包调用 Python CLI 会违背免 Python 目标；一次性补齐全部 Python CLI 子命令会扩大本轮风险，超出发布阻断修复范围。

## 一、健康检查

`lanbus healthcheck` 保持现有命令和退出码约定，仅接受：

- HTTP 状态码 200；
- 响应体为合法 JSON 对象；
- `ok` 字段为布尔值 `true`。

普通文本、错误 JSON、JSON 数组、缺少 `ok`、`ok:false`、非 200 和网络错误均返回非零。保留成功时输出响应体的行为，错误信息不包含认证信息。

实现位于 `go/cmd/lanbus/main.go`，使用 `httptest` 覆盖上述边界，并验证真实 Hub 的健康响应结构保持兼容。

## 二、原生 `peer-ui`

新增命令：

```bash
lanbus peer-ui --name NAME [--data-dir DIR]
```

默认数据目录为 `~/.lanbus/peers/NAME`。命令读取 `peer.json` 和 `approval.json`，校验审批 URL 为本地回环 HTTP 地址，然后使用操作系统参数化进程调用打开浏览器：

- Windows：`rundll32 url.dll,FileProtocolHandler URL`；
- macOS：`open URL`；
- Linux：`xdg-open URL`。

完整密钥只放在 URL fragment 中，终端仅打印不含 fragment 的审批页地址。禁止 shell 拼接，避免密钥注入和日志泄漏。缺少文件、无效名称、非回环 URL 或浏览器启动失败时返回明确错误。

新增内部包 `go/internal/peerui`，提供单 peer 解析和全量枚举；Windows GUI 改为复用该包，删除重复解析实现。单元测试覆盖有效数据、缺失数据、无效名称、非回环地址和 opener 注入，不在测试环境打开真实浏览器。

## 三、发布流水线

扩展 `.github/workflows/release.yml`：

1. 增加测试门禁，发布前运行 Python 和 Go 全量测试。
2. 从标签派生版本，并与 `src/lanbus/__init__.py` 对齐；不一致立即失败。
3. 保留现有四平台原生命令矩阵和 Windows GUI 构建。
4. Windows 作业汇总五个二进制，生成便携 ZIP，并通过 Inno Setup 生成 Setup EXE。
5. macOS 作业使用 `packaging/macos/build-pkg.sh` 生成 arm64 PKG。
6. Linux 作业使用 `packaging/linux/build-deb.sh` 生成 amd64 DEB。
7. 发布前检查要求的安装器、便携包和 wheel 均存在，再生成 SHA256SUMS 并创建 Release。

CI 仍在 PR 上运行完整测试；release workflow 的打包作业通过 PR 静态审查及本地可行的构建验证，标签发布本身不伪称已实测。

## 四、Windows 便携包一致性

`scripts/package_windows.py` 生成的根目录 `lanbus.cmd` 改为转发到 `bin\lanbus.exe`，不再执行 `python -m lanbus`。可选 local supervisor 和 Python fallback 仍明确标为需要 Python，不改变其功能。

## 五、文档

更新安装与发行文档，使命令、产物名称、标签版本要求和实际流水线一致。工作日志区分：

- 本地已执行的测试与构建；
- 由 GitHub PR 执行的 CI；
- 尚需 Windows/macOS/Linux 实机完成的安装、卸载和跨机审批验收。

## 六、验证与交付

按以下顺序验证：

1. 新增测试先复现健康检查和原生审批缺失，再实现至通过。
2. `gofmt`、Go 全量测试和 `go vet ./...`。
3. Python 全量测试、编译检查、`pip check`。
4. 原生 CLI 黑盒测试：有效/无效健康响应、审批 URL 生成、错误路径。
5. Shell 脚本语法、release workflow 静态结构、可执行的本地安装器/便携包构建。
6. 检查差异和工作日志，提交并推送功能分支，创建合并到 `main` 的 PR，报告真实 PR 与 CI 状态。

## 非本轮范围

备份的热数据库一致性、artifacts 完整性、Windows daemon 安装后立即启动、TLS 部分配置处理、代码签名/公证证书、全量 Python CLI 的 Go 重写，以及三平台真实跨机 agent 复测。上述项保留为后续独立工作，不在本轮顺带修改。
