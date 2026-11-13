# Targeted Request Approval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This project forbids subagents unless the user explicitly requests them.

**Goal:** 为目标机器提供可辨识实例的主动通知，并在新版节点之间对每次对话/项目任务进行人工审批。

**Architecture:** peer 核心继续负责签名、加密、权限和状态机；独立 notifier 负责桌面副作用。request v2 将“已批准”与“允许相同密文重发并执行一次”连接起来，旧 direct 请求保持原路径。Python 与 Go 共用字段和状态，Python CLI 负责目标选择与发起端等待。

**Tech Stack:** Python 3.9+ 标准库、cryptography、Go 1.25、pytest、Go httptest、macOS osascript、Windows PowerShell。

## Global Constraints

- 未批准前不得调用 executor；审批只能 loopback + 本地 secret。
- 不用模拟键鼠注入 AI 窗口，不自动批准，不在日志输出 secret。
- 旧目标没有 `request_v2` 时维持原协议；旧来源在新目标上维持原协议。
- 所有用户可见新增标识为 `display_name`、`instance_label`、`request_policy`、`approval_features`。
- 一次完整计划后连续执行，不逐步请求确认；不操作真实 agent 服务做自动化测试。

---

### Task 1: 实例元数据与本机通知边界

**Files:**
- Create: `src/lanbus/peer_notify.py`
- Create: `go/internal/peer/notify.go`
- Modify: `src/lanbus/peer.py`, `src/lanbus/cli.py`
- Modify: `go/internal/peer/server.go`, `go/internal/worker/direct.go`, `go/internal/worker/config.go`
- Test: `tests/test_peer.py`, `tests/test_peer_notify.py`, `go/internal/peer/peer_test.go`

**Interfaces:**
- Python `PeerServer(..., display_name="", instance_label="", request_policy="each", notifier=None, open_approval=True)`。
- Python `notify_pending(url, secret, title, body, open_page=True) -> None`；平台命令由参数列表构造，不经过 shell。
- Go `ServerConfig` 增加同义字段及 `Notifier func(Notification)`；`Notification{URL, Secret, Title, Body string, OpenPage bool}`。
- 健康响应增加 `display_name/instance_label/cwd_display/request_policy/approval_features`；`each` 时 features 含 `request_v2`。

- [ ] **Step 1: 写失败测试**：构造两个不同实例，断言 health 保留旧字段并公布身份与 `request_v2`；注入 notifier，pair 后断言恰好一次通知且 secret 不出现在 title/body。
- [ ] **Step 2: 运行 RED**：`python -m pytest tests/test_peer.py tests/test_peer_notify.py -q` 与 `cd go && go test ./internal/peer ./internal/worker`，预期字段/参数不存在。
- [ ] **Step 3: 最小实现**：增加配置、health 字段和 notifier 回调；worker 注入平台 notifier，CLI 增加 `--display-name/--instance-label/--request-policy/--no-open-approval`。
- [ ] **Step 4: 验证 GREEN**：重跑 Step 2；通知器测试替换 runner，不断言真实桌面弹窗。
- [ ] **Step 5: 提交**：`git commit -m "feat: add targeted peer notifications"`。

### Task 2: Python request v2 逐次审批状态机

**Files:**
- Create: `src/lanbus/peer_requests.py`
- Modify: `src/lanbus/peer.py`, `src/lanbus/cli.py`
- Modify: `src/lanbus/static/peer_approval.html`
- Test: `tests/test_peer_requests.py`, `tests/test_peer.py`

**Interfaces:**
- `ExecutionStore` 保存 `pending/approved/running/completed/denied/expired`，记录 `request_id/grant_id/packet_digest/source_peer_id/payload/result`。
- `DirectClient.request(..., request_policy="each", status_timeout=600)`：首次收到 `approval_required` 后轮询，approved 后以原 body 重发；running 时继续等待，completed 解密缓存结果。
- `ExecutionStore.approve(request_id)` 只改状态，不执行；相同 digest 才允许 approved 重发进入 executor。

- [ ] **Step 1: 写失败测试**：计数 executor 在 pair 后第二次 request 仍为 0；本地 approve 后同 body 重发为 1；再次重发仍为 1 且结果相同；deny、改 digest、chat 提升 project、cwd 逃逸均不执行。
- [ ] **Step 2: 运行 RED**：`python -m pytest tests/test_peer_requests.py -q`，预期 request v2 尚未实现。
- [ ] **Step 3: 实现服务端**：保留旧 `seen_requests` 路径；v2 解密和 scope/cwd 校验后存 pending，本地 inbox 合并 `execution_requests`，approve/deny 根据 `kind=execution` 走 ExecutionStore。
- [ ] **Step 4: 实现客户端**：`json_request` 增加可读取 202 的 `allow_status=True`；request body 固定复用，状态 denied/expired 抛错，approved 重发，completed 直接解密。
- [ ] **Step 5: 更新审批页**：显示来源/目标身份卡、“批准一次/拒绝”，execution 不显示 pair/session 按钮；旧 pairing 保持原按钮。
- [ ] **Step 6: 验证**：`python -m pytest tests/test_peer_requests.py tests/test_peer.py -q`，旧测试显式使用 `request_policy="pair"`，新测试使用 each。
- [ ] **Step 7: 提交**：`git commit -m "feat: require per-request approval in Python peers"`。

### Task 3: Go request v2 与跨语言一致

**Files:**
- Create: `go/internal/peer/requests.go`
- Modify: `go/internal/peer/server.go`, `go/internal/peer/client.go`, `go/internal/peer/approval.html`
- Test: `go/internal/peer/requests_test.go`, `go/internal/peer/peer_test.go`

**Interfaces:**
- Go `ExecutionStore` 与 Python 状态/字段一致；`Client.Request` 保持旧签名，`Client.RequestV2(...)` 暴露逐次审批等待。
- HTTP 202 不视为传输错误；`JSONRequest` 保持公开旧行为，新增内部 `jsonRequestStatus(...) (map[string]any, int, error)`。

- [ ] **Step 1: 写失败测试**：`TestRequestV2RequiresApprovalAndRunsOnce` 使用原子计数；覆盖 approve、deny、digest 改变、缓存结果和旧 `request_policy=pair`。
- [ ] **Step 2: 运行 RED**：`cd go && go test ./internal/peer -run RequestV2 -count=1`。
- [ ] **Step 3: 实现 Go 状态机**：复用现有解密、授权、canonical 和加密函数；同 request_id 不同 digest 返回 409；approved 同 digest 才执行。
- [ ] **Step 4: 同步 Go 审批 HTML**：execution 使用批准一次，不显示 pair/session；目标实例卡使用 textContent/esc。
- [ ] **Step 5: 验证 Go**：`go test ./internal/peer ./internal/worker -count=1`。
- [ ] **Step 6: 跨语言黑盒**：复用 `cross-version/cross_version_test.py` 的隔离目录模式，新版 Python/Go 双向均验证第二次请求需要 approve；旧版组合继续通过。
- [ ] **Step 7: 提交**：`git commit -m "feat: add Go per-request approval protocol"`。

### Task 4: 重名目标选择、文档与全量验收

**Files:**
- Modify: `src/lanbus/cli.py`, `src/lanbus/discovery.py`
- Modify: `README.md`, `README.zh-CN.md`, `docs/troubleshooting.md`
- Test: `tests/test_peer.py`, `tests/test_release_assets.py`

**Interfaces:**
- `_resolve_peer` 保持返回 URL；新增 `_peer_candidates(value)` 返回候选字典，重名错误文本包含 hostname/agent/display_name/instance_label/cwd_display/endpoint。

- [ ] **Step 1: 写失败测试**：两个同名不同实例时断言抛出包含两个实例标签的错误，不发起 HTTP；唯一实例仍自动解析。
- [ ] **Step 2: 实现与文档**：明确“Lanbus 审批窗口而非 AI 私有页面”、每实例启动示例、旧版兼容提示和通知权限排障。
- [ ] **Step 3: 全量验证**：`python -m pytest tests -q`、`cd go && go test -count=1 ./... && go vet ./...`、`git diff --check`。
- [ ] **Step 4: 版本与发布边界**：不在功能 PR 中提前创建标签；PR CI 全绿后单独请求合并/发布授权。
- [ ] **Step 5: 提交**：`git commit -m "docs: explain targeted per-request approvals"`。
