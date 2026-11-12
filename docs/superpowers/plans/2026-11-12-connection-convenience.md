# Connection Convenience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将跨机对话简化为首次 `lanbus connect HOST`、后续 `lanbus chat --message`，自动复用按 Hub 隔离的 token 和在线 Worker。

**Architecture:** 新增独立 `connections.py` 负责 URL、原子配置和连接解析；`cli.py` 只负责交互、健康检查和对话。默认发现端口补 8788，Hub／Worker 公共协议不变。

**Tech Stack:** Python 3.9+ 标准库、pytest、现有 Python／Go Hub 与 fake Worker。

## Global Constraints

- 不修改 MCP／REST／数据库／Worker 注册协议。
- 不尝试默认密码或猜测远端 token；保留人工确认。
- 显式参数与环境变量优先，禁止跨 Hub 串用 token。
- POSIX 配置目录 0700、文件 0600，原子替换；不打印 token。
- 不自动 commit、merge、push；继续使用现有隔离 worktree。

## File Structure

- Create `src/lanbus/connections.py`：URL、档案、默认连接、在线 env 选择。
- Modify `src/lanbus/cli.py`：`connect/disconnect`、chat 自动连接和 env。
- Modify `src/lanbus/discovery.py`：默认端口 8788。
- Create `tests/test_connections.py`：存储、优先级、CLI 单元与集成。
- Modify `tests/test_discovery.py`：默认端口契约。
- Modify `README.md`、`README.zh-CN.md`、`docs/troubleshooting.md`：两条命令流程。

---

### Task 1: 连接档案与地址解析

**Files:**
- Create: `src/lanbus/connections.py`
- Create: `tests/test_connections.py`

**Interfaces:**
- Produces: `normalize_hub(value)->str`、`load_store(path=None)->dict`、`save_store(store,path=None)->str`、`save_connection(hub,token,path=None)->dict`、`remove_connection(hub="",path=None)->bool`、`resolve_connection(hub="",token="",path=None)->tuple[str,str]`、`select_online_envs(response,limit=4)->list[str]`。

- [ ] **Step 1: 写地址、权限和 token 隔离失败测试**

```python
import json, os, stat
import pytest
from lanbus import connections


def test_normalize_hub():
    assert connections.normalize_hub("192.168.1.236") == "http://192.168.1.236:8788"
    assert connections.normalize_hub("192.168.1.236:8789/") == "http://192.168.1.236:8789"
    assert connections.normalize_hub("https://[::1]:8788/") == "https://[::1]:8788"
    assert connections.normalize_hub("::1") == "http://[::1]:8788"
    with pytest.raises(ValueError):
        connections.normalize_hub("http://host:8788/mcp")


def test_save_resolve_and_remove_per_hub(tmp_path, monkeypatch):
    path = str(tmp_path / "private" / "connections.json")
    connections.save_connection("192.168.1.236", "secret-a", path)
    connections.save_connection("192.168.1.240", "secret-b", path)
    assert connections.resolve_connection(path=path)[0] == "http://192.168.1.240:8788"
    assert connections.resolve_connection("192.168.1.236", path=path) == ("http://192.168.1.236:8788", "secret-a")
    assert connections.resolve_connection("192.168.1.241", path=path)[1] == ""
    if os.name != "nt":
        assert stat.S_IMODE(os.stat(path).st_mode) == 0o600
        assert stat.S_IMODE(os.stat(os.path.dirname(path)).st_mode) == 0o700
    assert connections.remove_connection(path=path)
    assert not os.path.exists(path)


def test_environment_and_explicit_token_priority(tmp_path, monkeypatch):
    path = str(tmp_path / "connections.json")
    connections.save_connection("host", "stored", path)
    monkeypatch.setenv("LANBUS_TOKEN", "from-env")
    assert connections.resolve_connection("host", path=path)[1] == "from-env"
    assert connections.resolve_connection("host", "explicit", path)[1] == "explicit"


def test_corrupt_store_is_not_overwritten(tmp_path):
    path = tmp_path / "connections.json"
    path.write_text("not-json", encoding="utf-8")
    with pytest.raises(ValueError, match="配置损坏"):
        connections.load_store(str(path))
    assert path.read_text(encoding="utf-8") == "not-json"


def test_select_online_envs():
    response = {"workers": [
        {"env": "a", "online": True}, {"env": "b", "online": False},
        {"env": "a", "online": True}, {"env": "c", "online": True},
        {"env": "d", "online": True}, {"env": "e", "online": True},
        {"env": "f", "online": True}]}
    assert connections.select_online_envs(response) == ["a", "c", "d", "e"]
```

- [ ] **Step 2: 验证 RED**

Run: `.venv/bin/python -m pytest tests/test_connections.py -q`
Expected: `ModuleNotFoundError: lanbus.connections`。

- [ ] **Step 3: 实现连接模块**

使用 `urllib.parse.urlsplit/urlunsplit`；裸 IPv6 先加方括号。空路径或 `/` 合法，其他 path/query/fragment 抛 `ValueError`。配置写入采用同目录临时文件：

```python
def save_store(store, path=None):
    target = config_path(path)
    parent = os.path.dirname(target)
    os.makedirs(parent, mode=0o700, exist_ok=True)
    if os.name != "nt":
        os.chmod(parent, 0o700)
    fd, temporary = tempfile.mkstemp(prefix=".connections-", dir=parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(store, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        if os.name != "nt":
            os.chmod(temporary, 0o600)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return target
```

`load_store` 对缺失文件返回 `{"version":1,"default":"","connections":{}}`，JSON／结构错误抛 `ValueError("配置损坏：...")`。`resolve_connection` 顺序为显式 token、`LANBUS_TOKEN`、显式 Hub、`LANBUS_HUB`、默认档案、本机回退；仅当最终 Hub 与档案 key 完全相同才取档案 token，最后才读既有 `~/.lanbus/token.txt`。

- [ ] **Step 4: 验证 GREEN**

Run: `.venv/bin/python -m pytest tests/test_connections.py -q`
Expected: 全部通过。

---

### Task 2: connect／disconnect 与自动对话

**Files:**
- Modify: `src/lanbus/cli.py`
- Modify: `tests/test_connections.py`

**Interfaces:**
- Consumes: Task 1 全部函数。
- Produces: `lanbus connect [HOST] [--token T] [--no-save]`、`lanbus connect --status`、`lanbus disconnect`、无 `--envs` 的自动 chat。

- [ ] **Step 1: 写 CLI 失败测试**

使用 `monkeypatch` 替换健康与 MCP，避免单元测试依赖网络：

```python
from lanbus import cli


def test_chat_uses_saved_hub_token_and_online_envs(monkeypatch, capsys):
    seen = {}
    monkeypatch.setattr(cli, "_mcp", lambda hub, token, method, params, **kw: seen.update(
        hub=hub, token=token, params=params) or {
            "ok": True, "conversation_id": "c1", "turns": []})
    monkeypatch.setattr("lanbus.connections.resolve_connection", lambda **kw: ("http://remote:8788", "secret"))
    monkeypatch.setattr(cli, "_online_envs", lambda hub, token: ["mac-a", "win-b"])
    assert cli.main(["chat", "--message", "hello"]) == 0
    assert seen["hub"] == "http://remote:8788"
    assert seen["token"] == "secret"
    assert seen["params"]["arguments"]["envs"] == ["mac-a", "win-b"]


def test_connect_noninteractive_missing_token(monkeypatch, capsys):
    monkeypatch.setattr(cli.sys.stdin, "isatty", lambda: False)
    monkeypatch.setattr(cli, "_get", lambda *a, **k: {"ok": True, "version": "1.9.0-go"})
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    assert cli.main(["connect", "192.168.1.236", "--no-save"]) == 1
    assert "agent token" in capsys.readouterr().out
```

另测：状态不泄露 token；disconnect 调用删除；自动 env 少于 2 返回 1；显式 env 不调用自动选择；`human_confirmation_required` 输出 `lanbus web`。

- [ ] **Step 2: 验证 RED**

Run: `.venv/bin/python -m pytest tests/test_connections.py -q`
Expected: `unknown command: connect`，自动 chat 单测因当前参数不足失败。

- [ ] **Step 3: 实现连接命令**

`_get(hub+"/health", "", timeout=5)` 需允许空鉴权头；或新增 `_health` 不带 Authorization。无地址时调用 `discovery.discover({})`，从 `agents` 中选择 `kind=lanbus` 的 endpoint，去重；一个直连，多个列出并返回 2。

token 缺失且 `sys.stdin.isatty()` 时用 `getpass.getpass("Agent token: ")`；否则输出缺少 agent token 并返回 1。保存时调用 `save_connection`，`--no-save` 跳过。状态只输出 `hub/reachable/version/online_envs/has_saved_token`。

- [ ] **Step 4: 改造 chat**

用 `resolve_connection` 替换当前 Hub／token 两行。仅在新对话、未显式 env 时调用 `_online_envs`，不足两个则输出在线数量与 `lan_envs` 检查提示。`human_confirmation_required` 时输出 JSON 和授权提示，其他格式保持兼容。

- [ ] **Step 5: 验证 GREEN**

Run: `.venv/bin/python -m pytest tests/test_connections.py tests/test_chat.py -q`
Expected: 全部通过。

---

### Task 3: 真实双 Hub 集成、发现与文档

**Files:**
- Modify: `tests/test_connections.py`
- Modify: `tests/test_discovery.py`
- Modify: `src/lanbus/discovery.py:39`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `docs/troubleshooting.md`

**Interfaces:**
- Consumes: Task 2 CLI。
- Produces: 可复现的真实 Hub 集成证据和用户文档。

- [ ] **Step 1: 写真实 Hub 集成测试**

使用 `Hub`、两个 fake Worker、隔离 `LANBUS_CONNECTIONS_FILE`：

```python
import os
from bus import ROOT, PY, TOKEN, start_worker, stop_worker

FAKE = os.path.join(ROOT, "tests", "fake_agent.py")
```

启动 `chat-a/chat-b` 后：

```python
monkeypatch.setenv("LANBUS_CONNECTIONS_FILE", str(tmp_path/"connections.json"))
assert cli.main(["connect", hub.url, "--token", TOKEN]) == 0
assert cli.main(["chat", "--message", "互相问好", "--rounds", "1", "--timeout", "30", "--total-timeout", "90"]) == 0
```

捕获输出确认包含 conversation；finally 停止两个 Worker。Go Hub 使用现有 `GoHub` 做同等集成，若二进制缺失遵循既有 skip 规则，但本地验证必须实际构建并运行。

- [ ] **Step 2: 默认端口测试先行**

```python
def test_default_ports_include_lanbus_hub():
    assert 8788 in discovery.BASE_PORTS
```

Run 指定测试确认 RED，再在 `BASE_PORTS` 添加 `8788, 8789`，运行测试 GREEN。

- [ ] **Step 3: 更新文档**

README 和中文说明加入：

```bash
lanbus connect 192.168.1.236
lanbus chat --message "请互相介绍并给出协作方案"
```

说明首次隐藏输入 agent token、配置路径与权限、`--no-save`、显式参数优先、在线 Worker 不足两个及人工确认。Troubleshooting 增加 401 时重新 connect、不同 Hub token 不共享、默认扫描含 8788。

- [ ] **Step 4: 全量验证**

Run:

```bash
.venv/bin/python -m compileall -q src tests
.venv/bin/python -m pytest tests -q
(cd go && GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn ../.tools/go/bin/go test -count=1 ./... && ../.tools/go/bin/go vet ./...)
git diff --check
```

Expected: 全部通过。另运行默认 `lanbus discover --json`，确认不自定义端口也能发现 `192.168.1.236:8788`；无有效 token 时仅验证连接错误提示，不声称真实跨机对话成功。

- [ ] **Step 5: 保留工作区并汇报边界**

列出新增命令、测试结果、配置安全属性和未完成的 token 授权跨机对话。保持未提交、未推送。
