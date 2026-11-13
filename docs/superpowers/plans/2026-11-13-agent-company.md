# Agent 公司成员网络 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.本项目禁止子代理，按用户已确认设计直接 Inline Execution，不逐步确认。

**Goal:** 实现无 host 的公司成员网络，人工批准加入一次后，成员通过对称 Agent Node 和本机 MCP 自由双向通信。

**Architecture:** 复用现有 pairing 的签名和 grant 加密通道。公司凭证只在人工批准后加入响应中以 AES-GCM 下发；成员向陌生成员发起带 HMAC 证明的 pairing，由目标自动签发加密 grant。默认 `company` 策略下公司请求立即执行，`each` 保留 request v2，`pair` 保留旧协议。

**Tech Stack:** Python 3.9+ 标准库、cryptography、Go 1.25、MCP JSON-RPC 2.0 stdio、pytest、Go httptest。

## Global Constraints

- 公司凭证不得出现在 health、URL、日志、通知标题/正文或 MCP 配置。
- 公司加入只能目标 loopback + approval secret 人工批准，不得自动伪造。
- 默认公司自由通信；非成员不得借 company 路径绕过原审批。
- `each` 继续每次批准且同密文只执行一次；旧客户端继续原协议。
- 不自动 commit、merge、push，不操作真实 agent 服务。

---

### Task 1: Python 公司凭证与自动成员会话

**Files:**
- Create: `src/lanbus/company.py`, `tests/test_company.py`
- Modify: `src/lanbus/peer.py`, `src/lanbus/cli.py`, `src/lanbus/worker.py`

**Interfaces:**
- `CompanyStore(data_dir).create(name)`, `.load()`, `.save(value)`, `.clear()`。
- `company_proof(secret, company_id, peer_id, nonce)` 返回 HMAC-SHA256。
- `request_company(url, identity, company, message, scope, cwd)` 复用 pairing 并附加 `company_id/company_proof`。
- `DirectClient.find_company_grant(health, message, scope, cwd)` 自动建立并保存 grant；无公司返回 None。

- [ ] **Step 1: 写失败测试**：创建 A/B/C，B 创建公司，A 加入必须 B 本地批准；A 获得凭证后与 C 自动建立会话并通信，第二次不产生 pending；错误证明不执行。
- [ ] **Step 2: RED**：`.venv/bin/python -m pytest tests/test_company.py -q`，预期模块不存在。
- [ ] **Step 3: 实现私密公司存储与证明**：`company.json` 原子写入、POSIX 0600；证明输入为 `company_id + ":" + peer_id + ":" + nonce`。
- [ ] **Step 4: 扩展现有 pairing**：`join_company=True` 创建 `kind=company` 人工申请；本地批准生成 mode=company grant，并以 `encrypt(key, company_payload, "company:"+grant_id)` 返回 `company_packet`。有效成员证明自动签发 grant，无 pending/通知。
- [ ] **Step 5: 接入节点和 CLI**：health 只公布 `company_id/company_name`；新增 `lanbus agent` 对称入口和 `company create/join/show/leave`；默认策略 `company`，无公司时保持 pair 审批。
- [ ] **Step 6: GREEN 与回归**：运行公司测试、peer 测试和 request v2 测试。

### Task 2: Go 公司互操作

**Files:**
- Create: `go/internal/peer/company.go`, `go/internal/peer/company_test.go`
- Modify: `go/internal/peer/server.go`, `go/internal/peer/client.go`, `go/internal/worker/types.go`, `go/internal/worker/direct.go`, `go/cmd/lanbus-worker/main.go`

**Interfaces:**
- `Company{ID,Name,Secret}`、`LoadCompany(dataDir)`、`CreateCompany(dataDir,name)`。
- `CompanyProof(secret,companyID,peerID,nonce)` 与 Python 使用相同 hex HMAC。
- `Client.RequestCompany(...)` 自动协商成员 grant 后调用现有请求逻辑。

- [ ] **Step 1: 写失败测试**：Go 公司创建/加载、人工加入、陌生成员自动会话、错误证明拒绝。
- [ ] **Step 2: RED**：`go test ./internal/peer -run Company -count=1`。
- [ ] **Step 3: 实现相同 JSON/HMAC/company_packet 协议**，Go map canonical 浮点规则保持现有 PyFloat。
- [ ] **Step 4: worker 默认 `company` 并注入公司存储**，health 字段与 Python 一致。
- [ ] **Step 5: GREEN**：`go test ./internal/peer ./internal/worker -count=1`。

### Task 3: 本机 MCP 与实例寻址

**Files:**
- Create: `src/lanbus/mcp_server.py`, `tests/test_mcp_server.py`
- Modify: `pyproject.toml`, `src/lanbus/cli.py`

**Interfaces:**
- stdio JSON-RPC 支持 `initialize`、`notifications/initialized`、`tools/list`、`tools/call`、`ping`。
- 工具：`lanbus_company_list`、`lanbus_agent_discover`、`lanbus_agent_send`、`lanbus_agent_delegate`、`lanbus_agent_result`、`lanbus_company_join`。
- MCP 从本机 data_dir 读取 secret，配置只包含命令和数据目录。

- [ ] **Step 1: 写 stdio 失败测试**：发送标准 MCP 换行分隔 JSON-RPC 消息，断言 initialize、六个工具和一次真实 echo send。
- [ ] **Step 2: RED**：`.venv/bin/python -m pytest tests/test_mcp_server.py -q`。
- [ ] **Step 3: 实现 MCP 帧解析与工具分发**；错误返回 JSON-RPC error/tool isError，不输出非 JSON 日志到 stdout。
- [ ] **Step 4: 实现结果查询**：节点结果表只暴露 request_id 对应来源，MCP 返回 pending/running/completed/denied/failed。
- [ ] **Step 5: GREEN 与命令入口验证**：测试 `lanbus-mcp` console script 和模块入口。

### Task 4: 三节点黑盒、文档与全量验收

**Files:**
- Create: `cross-version/company_test.py`
- Modify: `README.md`, `README.zh-CN.md`, `docs/troubleshooting.md`, `docs/security.md`

- [ ] **Step 1: 编写三节点黑盒**：B 创建公司，A 经 B 人工加入，C 经 A 人工加入；验证 A↔C、B↔C、C→A，第二次无审批，非成员仍需原审批。
- [ ] **Step 2: 验证 Python↔Go 公司加入与通信、v1.9.1/v1.9.2 兼容组合**。
- [ ] **Step 3: 文档提供 Claude/Kimi MCP 配置示例**，明确无 host、加入一次、严格模式和共享凭证泄露边界。
- [ ] **Step 4: 全量验证**：`.venv/bin/python -m pytest tests -q`；`cd go && ../.tools/go/bin/go test -count=1 ./... && ../.tools/go/bin/go vet ./...`；`git diff --check`。
- [ ] **Step 5: 交付边界**：报告实际验证与未验证平台，不提交、不合并、不发布。
