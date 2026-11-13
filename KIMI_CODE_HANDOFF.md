# Lanbus · Kimi Code 接续说明

更新时间：2026-11-13

## 1. 项目位置与分支

- 当前开发工作区：`/Users/vancoldzera/Documents/kimi/tasks/2026-11-11/19-36-49-3764d4dd/lanbus-hub-state-fix`
- 主仓库：`/Users/vancoldzera/Documents/kimi/tasks/2026-11-11/19-36-49-3764d4dd/lanbus`
- 当前分支：`feature/targeted-request-approval`
- 基线分支：`main`
- 状态：功能代码和文档已于 2026-11-13 commit 并 push 到 `origin/feature/targeted-request-approval`；**尚未 merge 到 main**。
- 约束：遵守根目录 `AGENTS.md`；一次性列出计划后连续执行，不逐步请求确认；不伪造目标机器人工授权；未经用户明确要求，不自动 commit、merge、push。

进入项目：

```bash
cd "/Users/vancoldzera/Documents/kimi/tasks/2026-11-11/19-36-49-3764d4dd/lanbus-hub-state-fix"
```

## 2. 当前完成度

核心架构已经从 host／worker 直连模型升级为**对称 Agent 公司成员网络**。Python、Go、MCP、审批页、三节点黑盒和跨版本验证均已完成。

### 已完成

1. **对称公司成员通信**
   - 每个节点都可发现、询问、接收消息和派发任务。
   - 新节点可向任一现有成员申请加入，不再必须连接创建者或 host。
   - 加入时由被请求成员所在机器人工批准一次。
   - 同公司成员默认自由通信，无需两两配对和逐次审批。
   - `lanbus agent` 为对称入口，`lanbus worker --direct` 保持兼容。

2. **三种审批策略**
   - `company`（默认）：公司成员自由通信；非成员仍需人工配对。
   - `each`：严格模式，每次对话或任务均需“批准一次”。
   - `pair`：兼容旧持续配对模式。

3. **凭证与加密**
   - `company.json` 保存 `company_id/company_name/secret/members`。
   - 成员证明：`HMAC-SHA256(secret, company_id + ":" + peer_id + ":" + nonce)`。
   - 公司凭证通过加入响应中的 AES-GCM `company_packet` 下发。
   - secret 不进入 health、URL、日志、桌面通知或 MCP 配置。
   - 非成员不能借 company 路径绕过审批，错误证明会被拒绝。

4. **MCP 本机入口**
   - 命令：`lanbus-mcp`，模块入口：`python -m lanbus.mcp_server`。
   - 使用标准 MCP stdio：换行分隔 JSON-RPC，不使用 Content-Length 帧。
   - 工具：`lanbus_company_list`、`lanbus_agent_discover`、`lanbus_agent_send`、`lanbus_agent_delegate`、`lanbus_agent_result`、`lanbus_company_join`。
   - `LANBUS_PEER_DIR` 必须指向对应 agent 的数据目录，配置中不得写 secret。

5. **审批和实例识别**
   - 支持桌面通知、显示名、实例标签和重名候选。
   - 公司申请显示“请求加入公司／批准加入”。
   - 严格模式请求显示“批准一次”；拒绝不会执行。
   - 同一 request_id 和内容使用缓存结果，不重复执行。

6. **文档**
   - 已更新 `README.md`、`README.zh-CN.md`、`docs/security.md`、`docs/troubleshooting.md`。
   - 设计：`docs/superpowers/specs/2026-11-13-agent-company-design.md`。
   - 计划：`docs/superpowers/plans/2026-11-13-agent-company.md`。

## 3. 已实际验证

以下命令已在当前工作区成功执行：

```bash
.venv/bin/python -m pip install -e . --no-deps
.venv/bin/python -m pytest tests -q

cd go
../.tools/go/bin/go test -count=1 ./...
../.tools/go/bin/go vet ./...
cd ..

.venv/bin/python cross-version/cross_version_test.py
.venv/bin/python cross-version/company_test.py
git diff --check
```

结果：

- Python：**107 项测试通过**。
- Go：全部模块测试和 `go vet ./...` 通过。
- 旧版兼容：10 组 Python／Go、新旧版本双向测试通过。
- 公司黑盒：A 经 B 加入、C 经 A 加入；A↔C、C→B 及第二次免审批通信通过。
- Python→Go 自动成员会话、经 Go 节点加入公司通过。
- 非成员审批拦截、伪造证明拒绝通过。
- `lanbus-mcp` 安装入口验证通过。

## 4. 主要实现文件

- Python 公司凭证：`src/lanbus/company.py`
- Python 节点与加密协议：`src/lanbus/peer.py`
- MCP：`src/lanbus/mcp_server.py`
- CLI：`src/lanbus/cli.py`
- Python worker 入口：`src/lanbus/worker.py`
- 审批页：`src/lanbus/static/peer_approval.html`
- Go 公司协议：`go/internal/peer/company.go`
- Go 节点：`go/internal/peer/server.go`
- Go 客户端：`go/internal/peer/client.go`
- Go 审批页：`go/internal/peer/approval.html`
- 单元测试：`tests/test_company.py`、`tests/test_mcp_server.py`、`tests/test_peer_requests.py`
- 公司黑盒：`cross-version/company_test.py`
- 跨版本黑盒：`cross-version/cross_version_test.py`

## 5. 后续建议与边界

1. 功能分支已 push；是否 merge 到 `main`（以及是否打 tag 发版）由用户决定。
2. 在真实 MCP 客户端中做一次端到端人工验收：配置 `lanbus-mcp`，发现节点、加入公司、双向 send／delegate。
3. 在 Windows 实机验证桌面通知和审批页；目前只有适配器与模拟测试，不得声称已实机验证。
4. 评估公司退出和成员撤销体验：当前 `company leave` 只删除本机公司文件，不会撤销其他节点已经签发的 grant；需要在目标本机 `lanbus revoke PEER_ID`。
5. 评估公司成员清单同步。目前自由通信依赖共享公司凭证和节点发现，`members` 尚不是完整、实时同步的成员目录。
6. 共享 secret 的信任边界必须保留：任一成员被攻陷可能危及整个公司，不应把该模型描述为逐成员独立密钥或完整零信任系统。

## 6. 接续时的第一条检查

```bash
cd "/Users/vancoldzera/Documents/kimi/tasks/2026-11-11/19-36-49-3764d4dd/lanbus-hub-state-fix"
git status --short
git branch --show-current
```

先保留现有未提交改动，不要重新拉取覆盖工作区，也不要另起一套实现。
