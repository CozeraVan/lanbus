# Zero-Config Agent Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This project forbids subagents unless the user explicitly requests them.

**Goal:** 交付无需用户管理 token、无需共同 Hub、由目标机器本地授权的 Agent 直连交流与项目任务。

**Architecture:** 新增独立 `peer.py`，以 Ed25519 身份、X25519 协商和 AES-GCM 请求加密实现 pairing/grant/direct-request 三个边界。目标审批只接受 loopback 和每实例本地审批凭证；持续授权持久化，会话授权仅存内存。CLI 增加 `peer/peers/inbox/allow/deny/revoke`，`chat/run --to` 走直连。

**Tech Stack:** Python 3.9+、cryptography>=42、标准库 ThreadingHTTPServer、pytest；现有 fake agent、Python/Go 回归套件。

## Global Constraints

- 正常流程无 `connect`、无共同 Hub、无用户 token。
- 未批准请求只能 pending，不能读取项目或执行命令。
- 审批/拒绝/撤销仅 loopback；远程不能伪造本地审批。
- 持续配对或 30 分钟空闲会话授权由目标用户选择；scope 为 chat/project，cwd 限制为 canonical path。
- POSIX 私密目录 0700、文件 0600，原子替换；秘密不进日志。
- 现有 Hub/token/MCP/Worker 注册协议保持兼容。
- 一次性完整计划后连续执行，不逐步请求确认；不自动 commit/merge/push。

## File Structure

- Create `AGENTS.md`：一次性计划与连续执行硬规范。
- Create `src/lanbus/peer.py`：身份、加密存储、配对 HTTP 服务、审批、直连客户端和执行。
- Modify `src/lanbus/cli.py`：peer 生命周期、目标端 inbox/allow/deny/revoke、发起端 `chat/run --to`。
- Modify `pyproject.toml`：cryptography 运行依赖。
- Create `tests/test_peer.py`：身份、配对、权限、重放、HTTP 和零 Hub 集成。
- Modify `README.md`、`README.zh-CN.md`、`docs/troubleshooting.md`：零配置目标端和发起端流程。
- Go direct peer 在 Python 参考协议稳定后按同一测试向量实现；未完成前明确标记，不伪称支持。

---

### Task 1: 身份、密钥与安全授权存储

**Files:** `src/lanbus/peer.py`, `tests/test_peer.py`, `pyproject.toml`

**Interfaces:**
- `Identity.load(data_dir)->Identity`；属性 `peer_id/sign_public_b64/x_public_b64`。
- `GrantStore(data_dir)`；`put_grant/grant/revoke/list_grants`。
- `encrypt(key, plaintext, aad)->dict`、`decrypt(key, packet, aad)->dict`。
- `local_request(port, path, payload, secret)->dict`。

- [ ] 写身份稳定、文件权限、持续授权重载、会话重启失效、错误密文失败测试。
- [ ] 运行 `.venv/bin/python -m pytest tests/test_peer.py -q`，确认模块缺失 RED。
- [ ] 用 Ed25519/X25519/HKDF-SHA256/AESGCM 实现，身份私钥 PEM 0600；grant JSON 原子替换。
- [ ] 加 `dependencies = ["cryptography>=42"]`，在隔离 venv 安装后验证 GREEN。

### Task 2: 配对服务与仅本机审批

**Files:** `src/lanbus/peer.py`, `tests/test_peer.py`

**Interfaces:**
- `PeerServer(name, agent, cwd, command, data_dir, host, port)`；`start()/stop()/url`。
- 公共 `GET /peer/health`、`POST /peer/pair`。
- 本机 `GET /peer/inbox`、`POST /peer/approve|deny|revoke`，要求 `X-Lanbus-Approval`。
- `request_pairing(url, identity, message, scope, cwd)->dict`，返回 pending 或 approved grant。

- [ ] 写 pending 不执行、非 loopback 审批拒绝、错误审批凭证拒绝、持续/会话两种授权和拒绝测试。
- [ ] 运行指定测试确认 RED。
- [ ] 实现 64 KiB body、10 分钟 pending、每来源 8/全局 64 上限和请求频率限制；签名覆盖 canonical pairing payload。
- [ ] 审批后返回 target 公钥、grant_id、双方 nonce，HKDF 得到相同会话密钥；验证 GREEN。

### Task 3: 加密直连请求、scope/cwd 和防重放

**Files:** `src/lanbus/peer.py`, `tests/test_peer.py`

**Interfaces:**
- `DirectClient(url, data_dir).request(grant, message, scope, cwd, timeout)->dict`。
- `POST /peer/request` 接收 grant_id/nonce/ciphertext，仅返回 JSON 结果。
- `PeerServer.execute(message, scope, cwd, timeout)->dict`。

- [ ] 写未授权 grant、错误 scope、逃逸 cwd、重复 request_id、撤销后访问、agent 成功/失败测试。
- [ ] 验证 RED 后实现 AES-GCM AAD=`grant_id:request_id`、目标 request_id 去重、canonical cwd containment。
- [ ] chat 只调用受控 agent adapter；project 才允许 cwd 项目任务。返回 exit_code/output，不返回秘密。
- [ ] 验证 GREEN。

### Task 4: 零配置 CLI 与本地审批体验

**Files:** `src/lanbus/cli.py`, `src/lanbus/worker.py`, `tests/test_peer.py`

**Interfaces:**
- `lanbus peer --name NAME --agent ADAPTER --cwd DIR [--port 8790]`。
- `lanbus peers [--url URL]`、`lanbus inbox --url URL`、`lanbus allow ID --pair|--session --scope chat|project [--cwd DIR]`、`lanbus deny ID`、`lanbus revoke PEER_ID`。
- `lanbus chat --to URL --message TEXT`、`lanbus run --to URL --message TEXT --cwd DIR`。

- [ ] 写命令分派、无 token 环境、pair/session 选择、等待/拒绝/重名/权限不足提示测试。
- [ ] 实现首次请求 pending 时输出 request_id 和目标本机提示；已配对后续 chat 自动复用 grant，不重复审批。
- [ ] `worker --direct` 复用同一 PeerServer；现有 `worker --hub` 默认路径不变。
- [ ] 验证 GREEN 和旧 `tests/test_connections.py`。

### Task 5: 真实双进程验收与文档

**Files:** `tests/test_peer.py`, `README.md`, `README.zh-CN.md`, `docs/troubleshooting.md`

- [ ] 启动两个独立 PeerServer 和 fake agent，清空 `LANBUS_TOKEN/LANBUS_HUB`，不启动 Hub。
- [ ] 验证发现→pending 未执行→目标本地 pair 批准→后续免审批对话；另一实例 session 项目任务；撤销、重启会话失效、伪造审批和重放失败。
- [ ] 文档给出目标端一条启动命令、发起端一条 chat/run 命令、本地 inbox/allow；说明项目任务不是完整 OS 沙箱。
- [ ] 运行 compileall、Python 全量、Go test/vet、`git diff --check`。
- [ ] 尝试 macOS 默认发现真实远端；没有目标端本地审批则不宣称真实跨机执行成功。
