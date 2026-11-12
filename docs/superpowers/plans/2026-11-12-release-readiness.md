# Release Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复原生健康检查与审批闭环，让标签发布真实产生三平台安装器，并通过 PR 接受完整 CI。

**Architecture:** 保持现有 Hub/Worker 协议不变。原生 CLI 通过内部 `peerui` 包复用本机审批元数据解析；发布复用现有打包脚本，以测试和版本一致性作为发布门禁。

**Tech Stack:** Go 1.25、Python 3.9+、GitHub Actions、Inno Setup、pkgbuild/productbuild、dpkg-deb。

## Global Constraints

- 不引入 Python 作为原生安装包运行依赖，不改变 `/health` 或 peer 协议。
- 审批密钥只出现在 URL fragment，终端与日志不打印完整 fragment。
- 所有新运行时代码先写失败测试，再做最小实现；浏览器 opener 必须注入测试。
- 不处理备份、daemon 首启、签名证书或全量 Python CLI Go 重写。
- 当前会话采用 inline execution，不使用子代理，不在任务之间请求重复确认。

---

### Task 1: 严格健康检查

**Files:**
- Modify: `go/cmd/lanbus/main.go`
- Create: `go/cmd/lanbus/main_test.go`

**Interfaces:**
- Consumes: 现有 `GET /health`，成功 JSON 必须包含 `"ok":true`。
- Produces: `healthcheck(args []string) error`，非 200、无效 JSON 或 `ok != true` 均返回错误。

- [ ] **Step 1: 写失败测试**

在 `main_test.go` 使用表驱动 `httptest.Server`：

```go
tests := []struct{ name, body string; status int; wantError bool }{
    {"healthy", `{"ok":true,"version":"1.9.0"}`, 200, false},
    {"unhealthy", `{"ok":false}`, 200, true},
    {"missing ok", `{}`, 200, true},
    {"plain text", `not a lanbus hub`, 200, true},
    {"invalid json", `{`, 200, true},
    {"json array", `[]`, 200, true},
    {"http error", `{"ok":true}`, 500, true},
}
```

每个子测试调用 `healthcheck([]string{"--url", server.URL})`，比较 `err != nil` 与 `wantError`。

- [ ] **Step 2: 验证 RED**

Run: `cd go && go test -count=1 ./cmd/lanbus -run TestHealthcheck`
Expected: `plain text`、`invalid json`、`json array` 和 `missing ok` 至少一项失败，证明当前误判存在。

- [ ] **Step 3: 最小实现**

保留响应大小限制和成功输出，将解析段改为：

```go
var parsed map[string]any
if err := json.Unmarshal(body, &parsed); err != nil {
    return fmt.Errorf("healthcheck: invalid JSON response: %w", err)
}
if ok, _ := parsed["ok"].(bool); !ok {
    return fmt.Errorf("healthcheck: ok=false")
}
fmt.Println(string(body))
return nil
```

Go 的 `map[string]any` 目标会拒绝 JSON 数组；不增加新依赖。

- [ ] **Step 4: 验证 GREEN**

Run: `cd go && go test -count=1 ./cmd/lanbus`
Expected: 所有健康检查测试通过。

---

### Task 2: 原生审批入口与托盘复用

**Files:**
- Create: `go/internal/peerui/peerui.go`
- Create: `go/internal/peerui/peerui_test.go`
- Modify: `go/cmd/lanbus/main.go`
- Modify: `go/cmd/lanbus/main_test.go`
- Modify: `go/cmd/lanbus-gui/main.go`
- Modify: `go/cmd/lanbus-gui/main_test.go`

**Interfaces:**
- Produces: `type Entry struct { Name, URL string }`。
- Produces: `ApprovalURL(dataDir string) (string, error)`。
- Produces: `Entries(home string) []Entry`。
- Produces: `peerUI(args []string, opener func(string) error) error` 与 `openBrowser(string) error`。

- [ ] **Step 1: 写解析和 CLI 失败测试**

解析测试在临时目录写入：

```json
// peer.json
{"name":"go-agent","url":"http://127.0.0.1:8791","port":8791}
// approval.json
{"secret":"s3cr3t"}
```

断言：

```go
got, err := ApprovalURL(dir)
want := "http://127.0.0.1:8791/peer/ui#secret=s3cr3t"
```

另测缺失文件、空 secret、`https://127.0.0.1:8791` 和 `http://192.168.1.2:8791` 均拒绝。CLI 测试以 fake opener 捕获 URL：

```go
var opened string
err := peerUI([]string{"--name", "go-agent", "--data-dir", dir}, func(u string) error {
    opened = u
    return nil
})
```

同时测试 `--name ../escape` 返回错误，且 opener 未被调用。

- [ ] **Step 2: 验证 RED**

Run: `cd go && go test -count=1 ./internal/peerui ./cmd/lanbus`
Expected: 新包或 `peerUI` 尚不存在，构建失败；随后建立最小骨架也必须让行为断言先失败，再完成实现。

- [ ] **Step 3: 实现共用解析包**

`ApprovalURL` 读取两个 JSON 文件，使用 `net/url` 校验：

```go
parsed, err := url.Parse(strings.TrimSpace(info.URL))
if err != nil || parsed.Scheme != "http" || !isLoopback(parsed.Hostname()) {
    return "", fmt.Errorf("peerui: approval URL must be loopback HTTP")
}
return strings.TrimRight(info.URL, "/") + "/peer/ui#secret=" + url.QueryEscape(sec.Secret), nil
```

`isLoopback` 接受 `localhost`、`127.0.0.0/8` 和 `::1`。`Entries(home)` 枚举 `~/.lanbus/peers/*/`，仅追加 `ApprovalURL` 成功的目录。

- [ ] **Step 4: 实现 CLI 与平台 opener**

`peerUI` 使用独立 `flag.FlagSet`，要求非空 `--name`；默认目录为 `~/.lanbus/peers/NAME`，`--data-dir` 仅供显式覆盖。名称不得含路径分隔符、`..` 或绝对路径。 opener 按 `runtime.GOOS` 选择：

```go
case "darwin": exec.Command("open", rawURL)
case "windows": exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
default: exec.Command("xdg-open", rawURL)
```

调用 `Run()` 并返回启动错误。终端仅输出 `strings.Split(fullURL, "#")[0]`。在 main switch 和 usage 注册 `peer-ui`。

- [ ] **Step 5: 托盘复用**

Windows GUI 的 `peerUIEntries` 仅做类型适配：

```go
func peerUIEntries(home string) []peerEntry {
    items := peerui.Entries(home)
    out := make([]peerEntry, 0, len(items))
    for _, item := range items {
        out = append(out, peerEntry{name: item.Name, url: item.URL})
    }
    return out
}
```

删除 GUI 中重复 JSON 解析，保留现有托盘菜单行为和测试。

- [ ] **Step 6: 验证 GREEN**

Run: `cd go && go test -count=1 ./internal/peerui ./cmd/lanbus && go test -exec=true -count=1 ./cmd/lanbus-gui`
Expected: 原生包测试通过；Windows GUI 测试完成编译（非 Windows 不声称执行）。

---

### Task 3: 发布流水线与 Windows 原生启动器

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `scripts/package_windows.py`
- Create: `tests/test_release_assets.py`

**Interfaces:**
- Consumes: 现有 `packaging/macos/build-pkg.sh`、`packaging/linux/build-deb.sh`、`packaging/windows/lanbus.iss`。
- Produces: tag 对应的 Setup EXE、Windows ZIP、macOS PKG、Linux DEB、wheel、原始二进制与 SHA256SUMS。

- [ ] **Step 1: 写发布静态回归测试**

测试读取 workflow 和打包脚本，断言以下具体门禁存在：

```python
assert "go test ./..." in workflow
assert "python -m pytest tests -q" in workflow
assert "packaging/macos/build-pkg.sh" in workflow
assert "packaging/linux/build-deb.sh" in workflow
assert "packaging\\windows\\lanbus.iss" in workflow
assert "lanbus-${version}-setup.exe" in workflow
assert 'bin\\lanbus.exe %*' in package_script
assert 'python -m lanbus %*' not in package_script
```

测试只防止发布 wiring 回退，不把静态文本检查当作安装器实机验证。

- [ ] **Step 2: 验证 RED**

Run: `.venv/bin/python -m pytest tests/test_release_assets.py -q`
Expected: 缺少安装器步骤、测试门禁且 Windows 启动器仍引用 Python，因此失败。

- [ ] **Step 3: 修正 Windows 启动器**

将生成内容改为：

```bat
@echo off
"%~dp0bin\lanbus.exe" %*
```

保留 `start-local-workers.cmd` 的 Python 可选说明。

- [ ] **Step 4: 扩充 release workflow**

增加 `test` job：安装 `.[dev]`，运行 `python -m pytest tests -q`，并在 `go/` 运行 `go test ./...`。每个构建 job 从 `${GITHUB_REF_NAME#v}` 派生 `version`，与 `src/lanbus/__init__.py` 比较。

Windows `gui` job 下载 `cli-windows-amd64` 到 `bin/`，将四个带平台后缀的文件重命名为无后缀 `.exe`，构建 `lanbus-gui.exe`，运行 `scripts/package_windows.py`，用 Chocolatey 安装 Inno Setup 后执行：

```powershell
iscc /DMyAppVersion=$version packaging\windows\lanbus.iss
```

新增 `package-macos`（macos-latest）与 `package-linux`（ubuntu-latest），分别下载对应 CLI artifact 到 `bin/` 并执行现有打包脚本。各作业上传 `dist/` 中目标安装器。

`release` 的 `needs` 包含 `test, cli, gui, wheel, package-macos, package-linux`，汇总后显式检查：

```bash
test -f "release/lanbus-${version}-setup.exe"
test -f "release/lanbus-${version}-windows-amd64.zip"
test -f "release/lanbus-${version}-macos-arm64.pkg"
test -f "release/lanbus_${version}_amd64.deb"
```

随后才生成 `SHA256SUMS.txt` 和 GitHub Release。

- [ ] **Step 5: 验证 GREEN 与脚本语法**

Run: `.venv/bin/python -m pytest tests/test_release_assets.py -q && bash -n packaging/macos/build-pkg.sh packaging/linux/build-deb.sh`
Expected: 静态发布回归和 Shell 语法检查通过。

---

### Task 4: 文档、全量验证与 PR

**Files:**
- Modify: `docs/install.md`
- Modify: `docs/release.md`
- Modify: `docs/WORKLOG.md`
- Modify: `README.md`
- Modify: `README.zh-CN.md`

**Interfaces:**
- Consumes: Task 1–3 的实际命令、产物和验证结果。
- Produces: 用户可执行且与实现一致的安装/审批/发布说明及可审查 PR。

- [ ] **Step 1: 更新文档**

明确 `lanbus peer-ui --name my-agent` 是原生命令；发布需要标签版本与 `__version__` 一致；release workflow 现在生成三平台安装器。工作日志只记录真实执行结果，保留签名、公证和平台实机边界。

- [ ] **Step 2: 全量验证**

```bash
.venv/bin/python -m compileall -q src tests
.venv/bin/python -m pytest tests -q
.venv/bin/python -m pip check
(cd go && go test -count=1 ./... && go vet ./...)
git diff --check
```

Expected: Python 测试数量随新增发布测试增加，全部退出码为 0。

- [ ] **Step 3: 可行构建与黑盒验证**

构建原生 CLI，使用隔离 HTTP 服务验证普通文本返回非零；用隔离 peer 目录和 fake opener 单测验证审批 URL，不打开真实浏览器。执行 macOS PKG 构建（本机可用时）及脚本语法检查；Windows Setup 和 Linux DEB 未在本机执行时明确标注由 release job/后续实机验收。

- [ ] **Step 4: 提交与推送**

分别提交运行时修复、发布闭环和文档计划，保持提交聚焦。推送 `fix/hub-state-parity`，不直接推送 `main`，不创建 tag。

- [ ] **Step 5: 创建 PR 并核验**

创建 `fix/hub-state-parity` → `main` 的 PR，正文包含完成项、测试证据、发布影响和未验证边界。读取 PR 与 Actions 状态，报告 URL；CI 尚在运行时不得声称已通过或已合并。
