# Python 与 Go Hub 状态一致性优化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改变公共协议的前提下，修复 Python 与 Go Hub 的零值配置、状态机竞态、Host 校验和 UTF-8 截断问题。

**Architecture:** 保留现有 Hub 结构和 SQLite schema，以条件 UPDATE 和现有锁保护状态转移。Python 与 Go 使用相同的零值、审批、打回、恢复及安全语义，分别增加单元测试和协议回归测试。

**Tech Stack:** Python 3.9+ 标准库、pytest、Go 1.25、SQLite、httptest。

## Global Constraints

- 不改变 MCP、REST、数据库公共结构和默认部署方式。
- 不引入第三方 Python 依赖，不重构无关子系统。
- 显式 `max_retry=0` 与 `max_recoveries=0` 必须保留；负数按 `0` 处理。
- `approve` 与 `rework` 只接受 `submitted`。
- 不自动执行 `git commit`、`git push` 或修改远端仓库。
- Go 工具链仅在工作区隔离使用，不修改系统环境。

---

### Task 1: 用回归测试固定零值与审批边界

**Files:**
- Create: `tests/test_hub_regressions.py`
- Create: `go/internal/hub/regression_test.go`
- Test: `tests/test_hub_regressions.py`
- Test: `go/internal/hub/regression_test.go`

**Interfaces:**
- Consumes: Python fixture `hub`、`tests.bus.MCP`、Go `hub.New(Config)`、`toolDispatch(map[string]any)`。
- Produces: 可重复验证两套 Hub 零值和状态边界的测试入口。

- [ ] **Step 1: 编写 Python 零值与非法打回测试**

```python
import json
import urllib.request
from bus import MCP, TOKEN


def test_explicit_zero_limits_are_preserved(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = m.tool("lan_dispatch", prompt="zero limits", max_retry=0,
                 max_recoveries=0, wait_s=0)["task_id"]
    row = m.tool("lan_result", task_id=tid)
    assert row["max_retry"] == 0
    assert row["max_recoveries"] == 0


def test_rework_rejects_non_submitted_task(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = m.tool("lan_dispatch", prompt="not submitted", wait_s=0)["task_id"]
    out = m.tool("lan_review", task_id=tid, verdict="rework", feedback="bad")
    assert "error" in out
    assert m.tool("lan_result", task_id=tid)["status"] == "queued"
```

- [ ] **Step 2: 编写 Go 零值与非法打回测试**

```go
package hub

import "testing"

func newRegressionHub(t *testing.T) *Hub {
    t.Helper()
    h, err := New(Config{DataDir: t.TempDir(), Token: "agent", WorkerToken: "worker"})
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = h.Close() })
    return h
}

func TestExplicitZeroLimitsArePreserved(t *testing.T) {
    h := newRegressionHub(t)
    out := h.toolDispatch(map[string]any{"prompt": "zero", "max_retry": 0, "max_recoveries": 0})
    row, err := h.store.one("SELECT * FROM tasks WHERE id=?", out["task_id"])
    if err != nil { t.Fatal(err) }
    if asInt(row["max_retry"]) != 0 || asInt(row["max_recoveries"]) != 0 {
        t.Fatalf("zero limits lost: %#v", row)
    }
}

func TestReworkRejectsNonSubmittedTask(t *testing.T) {
    h := newRegressionHub(t)
    out := h.toolDispatch(map[string]any{"prompt": "queued"})
    tid := asString(out["task_id"])
    got := h.toolReview(map[string]any{"task_id": tid, "verdict": "rework"})
    if got["error"] == nil { t.Fatalf("expected error: %#v", got) }
    row, _ := h.store.one("SELECT status FROM tasks WHERE id=?", tid)
    if asString(row["status"]) != StatusQueued { t.Fatalf("status changed: %#v", row) }
}
```

- [ ] **Step 3: 运行测试并确认当前实现失败**

Run: `.venv/bin/python -m pytest tests/test_hub_regressions.py -q`
Expected: 零值或非法打回断言失败。

Run: `.tools/go/bin/go test ./internal/hub -run 'TestExplicitZeroLimitsArePreserved|TestReworkRejectsNonSubmittedTask' -v`
Expected: 零值或非法打回断言失败。

- [ ] **Step 4: 实现参数默认值辅助逻辑**

Python 在 `src/lanbus/hub.py` 使用键是否存在区分缺省与零值：

```python
retry = max(0, int(args["max_retry"])) if "max_retry" in args else 2
max_rec = (max(0, int(args["max_recoveries"]))
           if "max_recoveries" in args else MAX_RECOVERIES)
```

Go 在 `go/internal/hub/tasks.go` 使用同样规则：

```go
maxRetry := 2
if _, ok := a["max_retry"]; ok { maxRetry = max(0, asInt(a["max_retry"])) }
maxRec := 3
if _, ok := a["max_recoveries"]; ok { maxRec = max(0, asInt(a["max_recoveries"])) }
```

两套实现的恢复逻辑都直接读取任务中持久化的 `max_recoveries`，删除 `0` 时回退默认值的判断。Python `t_review` 和 Go `toolReview` 在 rework 前统一要求：

```text
if status != submitted: return error containing current status
```

- [ ] **Step 5: 重跑边界测试**

Run: `.venv/bin/python -m pytest tests/test_hub_regressions.py -q`
Expected: PASS。

Run: `.tools/go/bin/go test ./internal/hub -run 'TestExplicitZeroLimitsArePreserved|TestReworkRejectsNonSubmittedTask' -v`
Expected: PASS。

---

### Task 2: 原子化 Python 审批、打回、续租与恢复

**Files:**
- Modify: `src/lanbus/hub.py:795-840`
- Modify: `src/lanbus/hub.py:1644-1705`
- Modify: `src/lanbus/hub.py:1896-1922`
- Test: `tests/test_hub_regressions.py`

**Interfaces:**
- Consumes: 现有 `_lock`、`q`、`x`、`ACTIVE`、任务表字段。
- Produces: `t_review` 只在持锁且状态有效时转移；`_recover_task` 返回 `queued|failed|None`。

- [ ] **Step 1: 增加反馈累计与旧证据清理测试**

```python
def test_rework_accumulates_feedback_and_clears_evidence(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = m.tool("lan_dispatch", prompt="rounds", max_retry=2, wait_s=0)["task_id"]
    with sqlite3.connect(hub.data_dir + "/bus.db") as c:
        c.execute("UPDATE tasks SET status='submitted', feedback='old', result='stale', diff='stale', verify_out='stale' WHERE id=?", (tid,))
    out = m.tool("lan_review", task_id=tid, verdict="rework", feedback="new")
    assert out["status"] == "queued"
    row = m.tool("lan_result", task_id=tid)
    assert "old" in row["feedback"] and "new" in row["feedback"]
    assert not row["result"] and not row["diff"] and not row["verify_out"]
```

- [ ] **Step 2: 运行测试确认旧实现的问题**

Run: `.venv/bin/python -m pytest tests/test_hub_regressions.py -q`
Expected: 当前非法打回或状态边界用例在修复前失败；修复后本用例通过。

- [ ] **Step 3: 在现有锁内完成审批与打回**

`t_review` 的整体读取、条件更新和事件决策置于 `with _lock:` 中。审批使用：

```python
cur = x("UPDATE tasks SET status='approved', review_note=?, finished_at=?, updated_at=? WHERE id=? AND status='submitted'",
        (feedback, now(), now(), tid))
if not cur.rowcount:
    current = q("SELECT status FROM tasks WHERE id=?", (tid,), one=True)
    return {"error": "task state changed during review", "status": current["status"]}
```

打回使用 `WHERE id=? AND status='submitted'`，保留现有反馈合并和证据清理字段，并在同一临界区删除产物记录。只有 `rowcount == 1` 才发送事件。

- [ ] **Step 4: 约束续租、日志元数据与恢复更新**

续租 SQL 改为：

```sql
UPDATE tasks SET heartbeat_at=?, lease_expires_at=?, updated_at=?
WHERE id=? AND worker=? AND COALESCE(claim_token,'')=?
  AND status IN ('claimed','running')
```

日志事务在写入前重新读取任务并验证 Worker、Claim Token、活动状态，元数据 UPDATE 使用同样条件。`_recover_task` 在锁内比较 fresh 与传入快照的 Worker／Claim Token，所有恢复 UPDATE 增加相同条件；受影响行数为零时返回 `None`，不发送事件。

- [ ] **Step 5: 运行 Python 回归与协议测试**

Run: `.venv/bin/python -m pytest tests/test_hub_regressions.py tests/test_protocol.py tests/test_features.py -q`
Expected: PASS。

---

### Task 3: 原子化 Go 审批、打回、续租与恢复

**Files:**
- Modify: `go/internal/hub/tasks.go:379-429`
- Modify: `go/internal/hub/worker.go:192-287`
- Modify: `go/internal/hub/hub.go:349-369`
- Test: `go/internal/hub/regression_test.go`

**Interfaces:**
- Consumes: `Store.one`、`Store.exec`、任务状态常量和 `sql.Result.RowsAffected`。
- Produces: 与 Python 一致的审批、打回和恢复语义。

- [ ] **Step 1: 增加 Go 打回与零恢复测试**

```go
func TestReworkAccumulatesFeedbackAndClearsEvidence(t *testing.T) {
    h := newRegressionHub(t)
    out := h.toolDispatch(map[string]any{"prompt": "rounds", "max_retry": 2})
    tid := asString(out["task_id"])
    _, _ = h.store.exec("UPDATE tasks SET status='submitted',feedback='old',result='stale',diff='stale',verify_out='stale' WHERE id=?", tid)
    got := h.toolReview(map[string]any{"task_id": tid, "verdict": "rework", "feedback": "new"})
    if got["status"] != StatusQueued { t.Fatalf("rework: %#v", got) }
    row, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
    feedback := asString(row["feedback"])
    if !strings.Contains(feedback, "old") || !strings.Contains(feedback, "new") { t.Fatalf("feedback: %q", feedback) }
    if asString(row["result"]) != "" || asString(row["diff"]) != "" || asString(row["verify_out"]) != "" { t.Fatalf("evidence retained: %#v", row) }
}

func TestZeroRecoveriesFailsFirstExpiredClaim(t *testing.T) {
    h := newRegressionHub(t)
    out := h.toolDispatch(map[string]any{"prompt": "no recovery", "max_recoveries": 0})
    tid := asString(out["task_id"])
    _, _ = h.store.exec("INSERT INTO workers(name,env,caps,last_seen) VALUES('w','','[]',?)", nowText())
    task, err := h.claim("w", "")
    if err != nil || task == nil { t.Fatalf("claim: %#v %v", task, err) }
    if got := h.recoverTask(task, "expired"); got != StatusFailed { t.Fatalf("recover=%q", got) }
}
```

- [ ] **Step 2: 运行 Go 测试确认失败**

Run: `.tools/go/bin/go test ./internal/hub -run 'TestReworkAccumulatesFeedbackAndClearsEvidence|TestZeroRecoveriesFailsFirstExpiredClaim' -v`
Expected: 当前覆盖反馈或恢复上限回退导致 FAIL。

- [ ] **Step 3: 实现条件审批与完整打回**

`approve` 使用 `WHERE id=? AND status='submitted'`；`rework` 先拒绝非提交状态，再按当前行计算重试次数。更新包含反馈合并、证据字段清空和 `DELETE FROM artifacts`，并检查受影响行数。重试耗尽使用 `retry >= maxRetry` 判定，使显式零值第一次打回即失败。

- [ ] **Step 4: 实现条件续租与新鲜度校验恢复**

`progress` 的 UPDATE 增加 `id/worker/claim_token/status` 条件，影响行数为零时返回 `invalid`。`logLines` 在单个 SQL 事务内重新读取并验证任务，日志和元数据使用同一事务提交。

`recoverTask` 重新读取当前任务，拒绝状态、Worker 或 Claim Token 已变化的快照；两条恢复 UPDATE 都带相同条件，仅成功更新后发送事件。`recoverWorker` 只把返回 `queued` 的 ID 加入 `recovered`。

- [ ] **Step 5: 运行 Go Hub 测试**

Run: `.tools/go/bin/go test ./internal/hub -v`
Expected: PASS。

---

### Task 4: 对齐 Go Host 防护与 UTF-8 截断

**Files:**
- Modify: `go/internal/hub/hub.go:133-175`
- Modify: `go/internal/hub/worker.go:391-396`
- Test: `go/internal/hub/regression_test.go`

**Interfaces:**
- Consumes: `net/http`、`net.SplitHostPort`、`net.ParseIP`、`unicode/utf8`。
- Produces: `validHost(string) bool` 与字节限额内 UTF-8 安全的 `trunc(string, int) string`。

- [ ] **Step 1: 编写 Host 与 UTF-8 测试**

```go
func TestHostValidation(t *testing.T) {
    for _, host := range []string{"localhost", "localhost:8788", "127.0.0.1:8788", "192.168.1.8", "[::1]:8788"} {
        if !validHost(host) { t.Errorf("valid host rejected: %q", host) }
    }
    for _, host := range []string{"", "evil.example.com", "evil.example.com:8788"} {
        if validHost(host) { t.Errorf("invalid host accepted: %q", host) }
    }
}

func TestTruncPreservesUTF8(t *testing.T) {
    got := trunc("ab界面", 5)
    if !utf8.ValidString(got) || got != "ab" { t.Fatalf("trunc=%q", got) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `.tools/go/bin/go test ./internal/hub -run 'TestHostValidation|TestTruncPreservesUTF8' -v`
Expected: 当前缺少 `validHost`，且旧 `trunc` 会产生非法 UTF-8。

- [ ] **Step 3: 实现 Host 校验**

```go
func validHost(raw string) bool {
    raw = strings.TrimSpace(raw)
    if raw == "" { return false }
    host := raw
    if strings.HasPrefix(raw, "[") {
        if end := strings.Index(raw, "]"); end > 0 { host = raw[1:end] }
    } else if h, _, err := net.SplitHostPort(raw); err == nil {
        host = h
    }
    host = strings.TrimSuffix(strings.ToLower(host), ".")
    if host == "localhost" { return true }
    return net.ParseIP(host) != nil
}
```

在路由分发的认证边界前检查 `r.Host`；非法 Host 返回 403。健康检查和 UI 保持现有公开行为。

- [ ] **Step 4: 实现 UTF-8 安全截断**

```go
func trunc(s string, n int) string {
    if n < 0 { n = 0 }
    if len(s) <= n { return s }
    for n > 0 && !utf8.ValidString(s[:n]) { n-- }
    return s[:n]
}
```

- [ ] **Step 5: 运行 Go 回归测试**

Run: `.tools/go/bin/go test ./internal/hub -v`
Expected: PASS。

---

### Task 5: 全量验证与差异审查

**Files:**
- Modify: `docs/architecture.md`（仅在实际行为描述需要澄清时）
- Test: 全部 Python 与 Go 测试

**Interfaces:**
- Consumes: Tasks 1–4 的实现。
- Produces: 可交付的本地补丁与验证结果。

- [ ] **Step 1: 格式化与静态检查**

Run: `gofmt -w go/internal/hub/hub.go go/internal/hub/tasks.go go/internal/hub/worker.go go/internal/hub/regression_test.go`
Run: `.tools/go/bin/go vet ./...`
Run: `.venv/bin/python -m compileall -q src tests`
Expected: 无错误。

- [ ] **Step 2: 运行全量测试**

Run: `.venv/bin/python -m pytest tests -q`
Expected: 全部通过；只有明确依赖外部浏览器等环境的能力可按既有规则跳过。

Run: `.tools/go/bin/go test ./...`
Expected: 全部通过。

- [ ] **Step 3: 检查最终差异**

Run: `git diff --check && git status --short && git diff --stat`
Expected: 无空白错误；仅包含设计、计划、目标源码和测试文件。

- [ ] **Step 4: 汇总审查结论**

按严重程度列出已修复问题、验证命令和结果，以及未纳入本轮的架构建议。不声称未实际运行的检查已经通过，不自动提交或推送。
